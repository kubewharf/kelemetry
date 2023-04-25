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

package jaeger_storage

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/pkg/metrics"
	jaegerstorage "github.com/jaegertracing/jaeger/plugin/storage"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideMuxImpl("jaeger-backend/jaeger-storage", manager.Ptr(&Backend{
		viper: viper.New(),
	}), jaegerbackend.Backend.List)
}

var spanStorageTypesToAddFlag = []string{
	"cassandra",
	"elasticsearch",
	"kafka",
	"grpc-plugin",
	"badger",
}

type Backend struct {
	manager.MuxImplBase
	Logger logrus.FieldLogger

	storageFactory *jaegerstorage.Factory
	viper          *viper.Viper
	reader         spanstore.Reader
}

type identifier struct {
	TraceId string `json:"traceId"`
	SpanId  uint64 `json:"spanId"`
}

var _ jaegerbackend.Backend = &Backend{}

func (backend *Backend) Setup(fs *pflag.FlagSet) {
	// SpanWriterTypes here is used to add flags only, the actual storageFactory is initialized in Backend.Init function
	factoryConfig := jaegerstorage.FactoryConfig{
		SpanWriterTypes:         spanStorageTypesToAddFlag,
		SpanReaderType:          "elasticsearch",
		DependenciesStorageType: "elasticsearch",
	}
	storageFactory, err := jaegerstorage.NewFactory(factoryConfig)
	if err != nil {
		backend.Logger.Fatalf("Cannot initialize storage factory: %v", err)
		return
	}

	// flagSet for storageFactory to populate
	goFs := &flag.FlagSet{}
	storageFactory.AddFlags(goFs)
	// intermediate pflag.FlagSet for viper to bind
	rawPfs := &pflag.FlagSet{}
	rawPfs.AddGoFlagSet(goFs)

	rawPfs.String(
		"span-storage.type",
		"",
		"The type of backend used for service dependencies storage. "+
			"Possible values are [elasticsearch, cassandra, opensearch, kafka, badger, grpc-plugin]",
	)

	if err := backend.viper.BindPFlags(rawPfs); err != nil {
		backend.Logger.Fatalf("Cannot bind storage factory flags: %w", err)
	}

	// rename flags after viper bind
	rawPfs.VisitAll(func(f *pflag.Flag) {
		f.Name = fmt.Sprintf("jaeger-storage.%s", f.Name)
	})

	fs.AddFlagSet(rawPfs)

	backend.viper.AutomaticEnv()
	backend.viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
}

func (backend *Backend) EnableFlag() *bool { return nil }

func (backend *Backend) MuxImplName() (name string, isDefault bool) {
	return "jaeger-storage", true
}

func (backend *Backend) Options() manager.Options { return backend }

func (backend *Backend) Init(ctx context.Context) error {
	storageType := backend.viper.GetString("span-storage.type")
	if len(storageType) == 0 {
		return fmt.Errorf("option --jaeger-storage.span-storage.type or env JAEGER_STORAGE_SPAN_STORAGE_TYPE is required")
	}

	factoryConfig := jaegerstorage.FactoryConfig{
		SpanWriterTypes:         []string{storageType},
		SpanReaderType:          storageType,
		DependenciesStorageType: storageType,
	}

	storageFactory, err := jaegerstorage.NewFactory(factoryConfig)
	if err != nil {
		return fmt.Errorf("can not initialize storage factory: %w", err)
	}
	backend.storageFactory = storageFactory

	zapLogger := zap.NewExample()
	backend.storageFactory.InitFromViper(backend.viper, zapLogger)
	if err := backend.storageFactory.Initialize(metrics.NullFactory, zapLogger); err != nil {
		return err
	}

	reader, err := backend.storageFactory.CreateSpanReader()
	if err != nil {
		return err
	}
	backend.reader = reader
	return nil
}

func (backend *Backend) Start(stopCh <-chan struct{}) error { return nil }

func (backend *Backend) Close() error { return nil }

func (backend *Backend) List(
	ctx context.Context,
	params *spanstore.TraceQueryParameters,
) ([]*jaegerbackend.TraceThumbnail, error) {
	filterTags := map[string]string{}
	for key, val := range params.Tags {
		filterTags[key] = val
	}
	if len(params.OperationName) > 0 {
		filterTags["cluster"] = params.OperationName
	}

	newParams := &spanstore.TraceQueryParameters{
		ServiceName:  "object",
		Tags:         filterTags,
		StartTimeMin: params.StartTimeMin,
		StartTimeMax: params.StartTimeMax,
		DurationMin:  params.DurationMin,
		DurationMax:  params.DurationMax,
		NumTraces:    params.NumTraces,
	}

	traces, err := backend.reader.FindTraces(ctx, newParams)
	if err != nil {
		return nil, fmt.Errorf("find traces from backend err: %w", err)
	}

	var traceThumbnails []*jaegerbackend.TraceThumbnail
	for _, trace := range traces {
		for _, span := range trace.Spans {
			resource, _ := model.KeyValues(span.Tags).FindByKey("resource")
			span.Process = &model.Process{ServiceName: resource.GetVStr()}
			slice := strings.Split(span.OperationName, "/")
			if len(slice) > 1 {
				span.OperationName = slice[1]
			}
		}
		var traceId string
		if len(trace.Spans) > 0 {
			traceId = trace.Spans[0].TraceID.String()
		}
		traceSpans := trace.Spans
		if strings.Contains(params.ServiceName, "exclusive") {
			traceSpans = filterSpanByTags(trace.Spans, newParams.Tags)
		}
		rootSpans := getRootSpan(traceSpans)

		for _, rootSpan := range rootSpans {
			cluster, _ := model.KeyValues(rootSpan.Tags).FindByKey("cluster")
			resource, _ := model.KeyValues(rootSpan.Tags).FindByKey("resource")
			rootSpan.Process = &model.Process{ServiceName: fmt.Sprintf("%s %s", cluster.GetVStr(), resource.GetVStr())}

			rootSpan.References = nil
			tree := tftree.NewSpanTree(trace)

			if err := tree.SetRoot(rootSpan.SpanID); err != nil {
				if errors.Is(err, tftree.ErrRootDoesNotExist) {
					return nil, fmt.Errorf(
						"trace data does not contain desired root span %v as indicated by the exclusive flag",
						rootSpan.SpanID,
					)
				}

				return nil, fmt.Errorf("cannot set root: %w", err)
			}

			spans := tree.GetSpans()

			traceThumbnails = append(traceThumbnails, &jaegerbackend.TraceThumbnail{
				Identifier: identifier{TraceId: traceId, SpanId: uint64(rootSpan.SpanID)},
				Cluster:    cluster.GetVStr(),
				Resource:   resource.GetVStr(),
				RootSpan:   rootSpan.SpanID,
				Spans:      spans,
			})
		}
	}

	return traceThumbnails, nil
}

func (backend *Backend) Get(
	ctx context.Context,
	identifierJson json.RawMessage,
	traceId model.TraceID,
) (*model.Trace, model.SpanID, error) {
	var id identifier
	if err := json.Unmarshal(identifierJson, &id); err != nil {
		return nil, 0, fmt.Errorf("persisted invalid trace identifier: %w", err)
	}

	mTraceID, err := model.TraceIDFromString(id.TraceId)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to convert the provided trace id: %w", err)
	}

	trace, err := backend.reader.GetTrace(ctx, mTraceID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get trace from backend: %w", err)
	}

	for _, span := range trace.Spans {
		cluster, _ := model.KeyValues(span.Tags).FindByKey("cluster")
		resource, _ := model.KeyValues(span.Tags).FindByKey("resource")
		if span.SpanID == model.SpanID(id.SpanId) {
			span.Process = &model.Process{ServiceName: fmt.Sprintf("%s %s", cluster.GetVStr(), resource.GetVStr())}
		} else {
			span.Process = &model.Process{ServiceName: resource.GetVStr()}
		}
	}

	return trace, model.SpanID(id.SpanId), nil
}

func filterSpanByTags(spans []*model.Span, tags map[string]string) []*model.Span {
	if len(tags) == 0 {
		return spans
	}
	var result []*model.Span
	spanMap := map[model.SpanID]*model.Span{}
	for _, span := range spans {
		tagsKeyVals := model.KeyValues(span.Tags)
		match := true
		for filterTagKey, filterTagVal := range tags {
			if val, ok := tagsKeyVals.FindByKey(filterTagKey); ok {
				if strings.Contains(filterTagVal, "*") {
					if matchTag, err := regexp.MatchString(filterTagVal, val.GetVStr()); err != nil || !matchTag {
						match = false
						break
					}
				} else if val.GetVStr() != filterTagVal {
					match = false
					break
				}
			}
		}
		if match {
			result = append(result, span)
			spanMap[span.SpanID] = span
		}
	}

	return result
}

func getRootSpan(spans []*model.Span) []*model.Span {
	spanMap := map[model.SpanID]*model.Span{}
	for _, span := range spans {
		spanMap[span.SpanID] = span
	}

	var rootSpans []*model.Span
	for _, span := range spans {
		if len(span.References) == 0 {
			rootSpans = append(rootSpans, span)
		} else if _, ok := spanMap[span.References[0].SpanID]; !ok {
			rootSpans = append(rootSpans, span)
		}
	}

	return rootSpans
}
