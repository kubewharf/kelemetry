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

package aggregator

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/aggregator/eventdecorator"
	linkjob "github.com/kubewharf/kelemetry/pkg/aggregator/linker/job"
	"github.com/kubewharf/kelemetry/pkg/aggregator/objectspandecorator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache"
	"github.com/kubewharf/kelemetry/pkg/aggregator/tracer"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("aggregator", manager.Ptr[Aggregator](&aggregator{}))
}

type options struct {
	reserveTtl       time.Duration
	spanTtl          time.Duration
	spanExtraTtl     time.Duration
	globalPseudoTags map[string]string
	globalEventTags  map[string]string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.DurationVar(
		&options.reserveTtl,
		"aggregator-reserve-ttl",
		time.Second*10,
		"if an object span has not been created after this duration, "+
			"another goroutine/process will try to create it",
	)
	fs.DurationVar(&options.spanTtl,
		"aggregator-span-ttl",
		time.Minute*30,
		"duration of each span",
	)
	fs.DurationVar(&options.spanExtraTtl,
		"aggregator-span-extra-ttl",
		0,
		"duration for which an object span is retained in cache after its duration has elapsed",
	)
	fs.StringToStringVar(&options.globalPseudoTags,
		"aggregator-pseudo-span-global-tags",
		map[string]string{},
		"tags applied to all object/spec/status/deletion pseudo-spans",
	)
	fs.StringToStringVar(&options.globalEventTags,
		"aggregator-event-span-global-tags",
		map[string]string{},
		"tags applied to all event spans",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type Aggregator interface {
	manager.Component

	// Send sends an event to the tracer backend.
	Send(ctx context.Context, object utilobject.Rich, event *aggregatorevent.Event) error

	// EnsureObjectSpan creates a pseudospan for the object, and triggers any possible relevant linkers.
	EnsureObjectSpan(
		ctx context.Context,
		object utilobject.Rich,
		eventTime time.Time,
	) (tracer.SpanContext, error)

	// GetOrCreatePseudoSpan creates a span following the pseudospan standard with the required tags.
	GetOrCreatePseudoSpan(
		ctx context.Context,
		object utilobject.Rich,
		pseudoType zconstants.PseudoTypeValue,
		eventTime time.Time,
		parent tracer.SpanContext,
		followsFrom tracer.SpanContext,
		extraTags map[string]string,
		dedupId string,
	) (span tracer.SpanContext, isNew bool, err error)
}

type aggregator struct {
	options          options
	Clock            clock.Clock
	Logger           logrus.FieldLogger
	SpanCache        spancache.Cache
	Tracer           tracer.Tracer
	Metrics          metrics.Client
	LinkJobPublisher linkjob.Publisher

	EventDecorators      *manager.List[eventdecorator.Decorator]
	ObjectSpanDecorators *manager.List[objectspandecorator.Decorator]

	SendMetric               *metrics.Metric[*sendMetric]
	SinceEventMetric         *metrics.Metric[*sinceEventMetric]
	LazySpanMetric           *metrics.Metric[*lazySpanMetric]
	LazySpanRetryCountMetric *metrics.Metric[*lazySpanRetryCountMetric]
}

type sendMetric struct {
	Cluster     string
	TraceSource string
	Success     bool
	Error       metrics.LabeledError
}

func (*sendMetric) MetricName() string { return "aggregator_send" }

type sinceEventMetric struct {
	Cluster     string
	TraceSource string
}

func (*sinceEventMetric) MetricName() string { return "aggregator_send_since_event" }

type lazySpanMetric struct {
	Cluster    string
	PseudoType zconstants.PseudoTypeValue
	Result     string
}

func (*lazySpanMetric) MetricName() string { return "aggregator_lazy_span" }

type lazySpanRetryCountMetric lazySpanMetric

func (*lazySpanRetryCountMetric) MetricName() string { return "aggregator_lazy_span_retry_count" }

func (aggregator *aggregator) Options() manager.Options {
	return &aggregator.options
}

func (aggregator *aggregator) Init() error { return nil }

func (aggregator *aggregator) Start(ctx context.Context) error { return nil }

func (aggregator *aggregator) Close(ctx context.Context) error { return nil }

func (aggregator *aggregator) Send(
	ctx context.Context,
	object utilobject.Rich,
	event *aggregatorevent.Event,
) (err error) {
	sendMetric := &sendMetric{Cluster: object.Cluster, TraceSource: event.TraceSource}
	defer aggregator.SendMetric.DeferCount(aggregator.Clock.Now(), sendMetric)

	aggregator.SinceEventMetric.
		With(&sinceEventMetric{Cluster: object.Cluster, TraceSource: event.TraceSource}).
		Summary(float64(aggregator.Clock.Since(event.Time).Nanoseconds()))

	parentSpan, err := aggregator.EnsureObjectSpan(ctx, object, event.Time)
	if err != nil {
		sendMetric.Error = metrics.LabelError(err, "EnsureObjectSpan")
		return fmt.Errorf("%w during ensuring object span", err)
	}

	for _, decorator := range aggregator.EventDecorators.Impls {
		decorator.Decorate(ctx, object, event)
	}

	tags := zconstants.VersionedKeyToSpanTags(object.VersionedKey)
	tags[zconstants.NotPseudo] = zconstants.NotPseudo
	tags[zconstants.TraceSource] = event.TraceSource

	span := tracer.Span{
		Type:       event.TraceSource,
		Name:       event.Title,
		StartTime:  event.Time,
		FinishTime: event.GetEndTime(),
		Parent:     parentSpan,
		Tags:       tags,
		Logs:       event.Logs,
	}
	for tagKey, tagValue := range event.Tags {
		span.Tags[tagKey] = fmt.Sprint(tagValue)
	}
	for tagKey, tagValue := range aggregator.options.globalPseudoTags {
		span.Tags[tagKey] = tagValue
	}

	_, err = aggregator.Tracer.CreateSpan(span)
	if err != nil {
		sendMetric.Error = metrics.LabelError(err, "CreateSpan")
		return fmt.Errorf("cannot create span: %w", err)
	}

	sendMetric.Success = true

	aggregator.Logger.WithFields(object.AsFields("object")).
		WithField("event", event.Title).
		WithField("logs", len(event.Logs)).
		Debug("CreateSpan")

	return nil
}

func (agg *aggregator) EnsureObjectSpan(
	ctx context.Context,
	object utilobject.Rich,
	eventTime time.Time,
) (tracer.SpanContext, error) {
	span, isNew, err := agg.GetOrCreatePseudoSpan(ctx, object, zconstants.PseudoTypeObject, eventTime, nil, nil, nil, "object")
	if err != nil {
		return nil, err
	}

	if isNew {
		agg.LinkJobPublisher.Publish(&linkjob.LinkJob{
			Object:    object,
			EventTime: eventTime,
			Span:      span,
		})
	}

	return span, nil
}

type spanCreator struct {
	cacheKey string

	retries     int32
	fetchedSpan tracer.SpanContext
	reserveUid  spancache.Uid
}

func (c *spanCreator) fetchOrReserve(
	ctx context.Context,
	agg *aggregator,
) error {
	c.retries += 1

	entry, err := agg.SpanCache.FetchOrReserve(ctx, c.cacheKey, agg.options.reserveTtl)
	if err != nil {
		return metrics.LabelError(fmt.Errorf("%w during fetch-or-reserve of object span", err), "FetchOrReserve")
	}

	if entry.Value != nil {
		// the entry already exists, no additional logic required
		span, err := agg.Tracer.ExtractCarrier(entry.Value)
		if err != nil {
			return metrics.LabelError(fmt.Errorf("persisted span contains invalid data: %w", err), "BadCarrier")
		}

		c.fetchedSpan = span
		return nil
	}

	// else, a new reservation was created
	c.reserveUid = entry.LastUid
	return nil
}

func (agg *aggregator) GetOrCreatePseudoSpan(
	ctx context.Context,
	object utilobject.Rich,
	pseudoType zconstants.PseudoTypeValue,
	eventTime time.Time,
	parent tracer.SpanContext,
	followsFrom tracer.SpanContext,
	extraTags map[string]string,
	dedupId string,
) (_span tracer.SpanContext, _isNew bool, _err error) {
	lazySpanMetric := &lazySpanMetric{
		Cluster:    object.Cluster,
		PseudoType: pseudoType,
		Result:     "error",
	}
	defer agg.LazySpanMetric.DeferCount(agg.Clock.Now(), lazySpanMetric)

	cacheKey := agg.expiringSpanCacheKey(object.Key, eventTime, dedupId)

	logger := agg.Logger.
		WithField("step", "GetOrCreatePseudoSpan").
		WithField("dedupId", dedupId).
		WithFields(object.AsFields("object"))

	defer func() {
		logger.WithField("cacheKey", cacheKey).WithField("result", lazySpanMetric.Result).Debug("GetOrCreatePseudoSpan")
	}()

	creator := &spanCreator{cacheKey: cacheKey}

	if err := retry.OnError(
		retry.DefaultBackoff,
		spancache.ShouldRetry,
		func() error { return creator.fetchOrReserve(ctx, agg) },
	); err != nil {
		return nil, false, metrics.LabelError(fmt.Errorf("cannot reserve or fetch span %q: %w", cacheKey, err), "ReserveRetryLoop")
	}

	retryCountMetric := lazySpanRetryCountMetric(*lazySpanMetric)
	defer func() {
		agg.LazySpanRetryCountMetric.With(&retryCountMetric).Summary(float64(creator.retries))
	}() // take the value of lazySpanMetric later

	logger = logger.
		WithField("returnSpan", creator.fetchedSpan != nil).
		WithField("reserveUid", creator.reserveUid)

	if creator.fetchedSpan != nil {
		lazySpanMetric.Result = "fetch"
		return creator.fetchedSpan, false, nil
	}

	// we have a new reservation, need to initialize it now
	startTime := agg.Clock.Now()

	span, err := agg.CreatePseudoSpan(ctx, object, pseudoType, eventTime, parent, followsFrom, extraTags)
	if err != nil {
		return nil, false, metrics.LabelError(fmt.Errorf("cannot create span: %w", err), "CreateSpan")
	}

	entryValue, err := agg.Tracer.InjectCarrier(span)
	if err != nil {
		return nil, false, metrics.LabelError(fmt.Errorf("cannot serialize span context: %w", err), "InjectCarrier")
	}

	totalTtl := agg.options.spanTtl + agg.options.spanExtraTtl
	err = agg.SpanCache.SetReserved(ctx, cacheKey, entryValue, creator.reserveUid, totalTtl)
	if err != nil {
		return nil, false, metrics.LabelError(fmt.Errorf("cannot persist reserved value: %w", err), "PersistCarrier")
	}

	logger.WithField("duration", agg.Clock.Since(startTime)).Debug("Created new span")

	lazySpanMetric.Result = "create"

	return span, true, nil
}

func (agg *aggregator) CreatePseudoSpan(
	ctx context.Context,
	object utilobject.Rich,
	pseudoType zconstants.PseudoTypeValue,
	eventTime time.Time,
	parent tracer.SpanContext,
	followsFrom tracer.SpanContext,
	extraTags map[string]string,
) (tracer.SpanContext, error) {
	remainderSeconds := eventTime.Unix() % int64(agg.options.spanTtl.Seconds())
	startTime := eventTime.Add(-time.Duration(remainderSeconds) * time.Second)

	tags := zconstants.VersionedKeyToSpanTags(object.VersionedKey)
	tags[zconstants.TraceSource] = zconstants.TraceSourceObject
	tags[zconstants.PseudoType] = string(pseudoType)
	tags["timeStamp"] = startTime.Format(time.RFC3339)

	span := tracer.Span{
		Type:       string(pseudoType),
		Name:       fmt.Sprintf("%s/%s", object.Resource, object.Name),
		StartTime:  startTime,
		FinishTime: startTime.Add(agg.options.spanTtl),
		Parent:     parent,
		Follows:    followsFrom,
		Tags:       tags,
	}
	for tagKey, tagValue := range agg.options.globalPseudoTags {
		span.Tags[tagKey] = tagValue
	}
	for tagKey, tagValue := range extraTags {
		span.Tags[tagKey] = tagValue
	}

	if pseudoType == zconstants.PseudoTypeObject {
		for _, decorator := range agg.ObjectSpanDecorators.Impls {
			decorator.Decorate(ctx, object, span.Type, span.Tags)
		}
	}

	spanContext, err := agg.Tracer.CreateSpan(span)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot create span: %w", err), "CreateSpan")
	}

	agg.Logger.
		WithFields(object.AsFields("object")).
		WithField("parent", parent).
		Debug("CreateSpan")

	return spanContext, nil
}

func (aggregator *aggregator) expiringSpanCacheKey(
	object utilobject.Key,
	timestamp time.Time,
	subObject string,
) string {
	expiringWindow := timestamp.Unix() / int64(aggregator.options.spanTtl.Seconds())
	return aggregator.spanCacheKey(object, fmt.Sprintf("field=%s,window=%d", subObject, expiringWindow))
}

func (aggregator *aggregator) spanCacheKey(object utilobject.Key, window string) string {
	return fmt.Sprintf("%s/%s", object.String(), window)
}
