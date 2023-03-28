// Copyright 2023 The Kelemetry Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	otelsdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/kubewharf/kelemetry/pkg/aggregator/tracer"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideMuxImpl("tracer/otel", newOtel, tracer.Tracer.CreateSpan)
}

type options struct {
	endpoint   string
	insecure   bool
	attributes map[string]string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.endpoint, "tracer-otel-endpoint", "127.0.0.1:4317", "otel endpoint")
	fs.BoolVar(&options.insecure, "tracer-otel-insecure", false, "allow insecure otel connections")
	fs.StringToStringVar(&options.attributes, "tracer-otel-resource-attributes", map[string]string{
		string(semconv.ServiceVersionKey): "dev",
	}, "otel resource service attributes")
}

func (options *options) EnableFlag() *bool { return nil }

type otelTracer struct {
	manager.MuxImplBase

	options   options
	logger    logrus.FieldLogger
	deferList *shutdown.DeferList

	// Tracers are instantiated lazily.
	// They persist as long as Start ctx does because they are module-based.
	ctx context.Context //nolint:containedctx

	exporter   *otlptrace.Exporter
	tracers    sync.Map // equiv. map[string]*onceTracer
	propagator propagation.TextMapPropagator
}

func newOtel(logger logrus.FieldLogger) *otelTracer {
	return &otelTracer{
		logger:    logger,
		deferList: shutdown.NewDeferList(),
	}
}

type onceTracer struct {
	readyCh <-chan struct{}
	value   oteltrace.Tracer
	err     error
}

func (_ *otelTracer) MuxImplName() (name string, isDefault bool) { return "otel", true }

func (otel *otelTracer) Options() manager.Options {
	return &otel.options
}

func (otel *otelTracer) Init(ctx context.Context) error {
	otel.ctx = ctx

	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(otel.options.endpoint)}
	if otel.options.insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	}
	client := otlptracegrpc.NewClient(options...)
	exporter, err := otlptrace.New(ctx, client)
	if err != nil {
		return fmt.Errorf("cannot start trace exporter: %w", err)
	}
	otel.exporter = exporter

	otel.propagator = propagation.TraceContext{}

	return nil
}

func (otel *otelTracer) getTracer(serviceName string) (oteltrace.Tracer, error) {
	ch := make(chan struct{})
	defer close(ch)

	onceAny, loaded := otel.tracers.LoadOrStore(serviceName, &onceTracer{readyCh: ch})
	once := onceAny.(*onceTracer)

	if loaded {
		<-once.readyCh
		return once.value, once.err
	}

	once.value, once.err = otel.createTracer(serviceName)
	if once.err != nil {
		otel.tracers.Delete(serviceName)
	}

	return once.value, once.err
}

func (otel *otelTracer) createTracer(serviceName string) (oteltrace.Tracer, error) {
	attributes := []attribute.KeyValue{}
	attributes = append(attributes, attribute.String(string(semconv.ServiceNameKey), serviceName))
	for k, v := range otel.options.attributes {
		attributes = append(attributes, attribute.String(k, v))
	}

	resource, err := resource.New(
		otel.ctx,
		resource.WithAttributes(attributes...),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot create resource: %w", err)
	}

	tp := otelsdktrace.NewTracerProvider(
		otelsdktrace.WithSampler(otelsdktrace.AlwaysSample()),
		otelsdktrace.WithBatcher(otel.exporter),
		otelsdktrace.WithResource(resource),
	)
	otel.deferList.LockedDefer("close otel tracer provider", func() error {
		ctx, cancelFunc := context.WithTimeout(otel.ctx, time.Second*10)
		defer cancelFunc()
		return tp.Shutdown(ctx)
	})

	return tp.Tracer(serviceName), nil
}

func (otel *otelTracer) Start(ctx context.Context) error { return nil }

func (otel *otelTracer) Close(ctx context.Context) error {
	if name, err := otel.deferList.LockedRun(otel.logger); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

func (otel *otelTracer) CreateSpan(span tracer.Span) (tracer.SpanContext, error) {
	var ctx context.Context
	if span.Parent != nil {
		ctx = span.Parent.(context.Context)
	} else {
		ctx = context.Background() // this is a throwaway context only used for passing context values
	}

	tracer, err := otel.getTracer(span.Type)
	if err != nil {
		return nil, err
	}

	newCtx, otelSpan := tracer.Start(ctx, span.Name, oteltrace.WithTimestamp(span.StartTime))
	defer otelSpan.End(oteltrace.WithTimestamp(span.FinishTime))

	attributes := []attribute.KeyValue{}
	for tagName, tagValue := range span.Tags {
		attributes = append(attributes, attribute.String(tagName, tagValue))
	}
	otelSpan.SetAttributes(attributes...)

	for _, log := range span.Logs {
		attrs := []attribute.KeyValue{
			attribute.String(zconstants.LogTypeAttr, string(log.Type)),
		}
		for _, attr := range log.Attrs {
			attrs = append(attrs, attribute.String(attr[0], attr[1]))
		}
		otelSpan.AddEvent(log.Message, oteltrace.WithTimestamp(span.StartTime), oteltrace.WithAttributes(attrs...))
	}

	return newCtx, nil
}

func (otel *otelTracer) InjectCarrier(spanContext tracer.SpanContext) ([]byte, error) {
	ctx := spanContext.(context.Context)

	carrier := propagation.MapCarrier{}
	otel.propagator.Inject(ctx, carrier)

	if len(carrier) == 0 {
		return nil, fmt.Errorf("otel propagator injected nothing into the carrier")
	}

	marshalled, err := json.Marshal(carrier)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal span carrier: %w", err)
	}

	return marshalled, nil
}

func (otel *otelTracer) ExtractCarrier(textMap []byte) (tracer.SpanContext, error) {
	carrier := propagation.MapCarrier{}
	err := json.Unmarshal(textMap, &carrier)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal span carrier: %w", err)
	}

	ctx := otel.propagator.Extract(context.Background(), carrier)

	return ctx, nil
}
