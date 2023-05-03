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
	"strings"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/pkg/metrics"
	jaegerstorage "github.com/jaegertracing/jaeger/plugin/storage"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/util/sets"

	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
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
	TraceId model.TraceID `json:"traceId"`
	SpanId  model.SpanID  `json:"spanId"`
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

func (backend *Backend) Init() error {
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

func (backend *Backend) Start(ctx context.Context) error { return nil }

func (backend *Backend) Close(ctx context.Context) error { return nil }

func (backend *Backend) List(
	ctx context.Context,
	params *spanstore.TraceQueryParameters,
	exclusive bool,
) ([]*jaegerbackend.TraceThumbnail, error) {
	filterTags := map[string]string{}
	for key, val := range params.Tags {
		filterTags[key] = val
	}
	if len(params.OperationName) > 0 {
		filterTags["cluster"] = params.OperationName
	}

	// TODO support additional user-defined trace sources
	var traces []*model.Trace
	for _, traceSource := range zconstants.KnownTraceSources(false) {
		if len(traces) >= params.NumTraces {
			break
		}

		newParams := &spanstore.TraceQueryParameters{
			ServiceName:  traceSource,
			Tags:         filterTags,
			StartTimeMin: params.StartTimeMin,
			StartTimeMax: params.StartTimeMax,
			DurationMin:  params.DurationMin,
			DurationMax:  params.DurationMax,
			NumTraces:    params.NumTraces - len(traces),
		}

		newTraces, err := backend.reader.FindTraces(ctx, newParams)
		if err != nil {
			return nil, fmt.Errorf("find traces from backend err: %w", err)
		}

		traces = append(traces, newTraces...)
	}

	var traceThumbnails []*jaegerbackend.TraceThumbnail

	// a stateful function that determines only returns true for each valid resultant root span the first time
	var deduplicator func(*model.Span) bool
	if exclusive {
		// exclusive mode, each object under trace should have a list entry
		type objectInTrace struct {
			traceId model.TraceID
			key     tftree.GroupingKey
		}
		seenObjects := sets.New[objectInTrace]()
		deduplicator = func(span *model.Span) bool {
			key, hasKey := tftree.GroupingKeyFromSpan(span)
			if !hasKey {
				return false // not a root
			}

			field, hasField := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel)
			if !hasField || field.VStr != zconstants.NestLevelObject {
				return false // not an object root
			}

			for reqKey, reqValue := range filterTags {
				if value, exists := model.KeyValues(span.Tags).FindByKey(reqKey); !exists || value.VStr != reqValue {
					return false // not a matched root
				}
			}

			fullKey := objectInTrace{
				traceId: span.TraceID,
				key:     key,
			}

			if seenObjects.Has(fullKey) {
				return false // a known root
			}

			seenObjects.Insert(fullKey)
			return true
		}
	} else {
		// non exclusive mode, display full trace, so we want each full trace to display exactly once.
		seenTraces := sets.New[model.TraceID]()
		deduplicator = func(span *model.Span) bool {
			if len(span.References) > 0 {
				return false // we only want the root
			}

			if seenTraces.Has(span.TraceID) {
				return false
			}

			seenTraces.Insert(span.TraceID)
			return true
		}
	}

	for _, trace := range traces {
		if len(trace.Spans) == 0 {
			continue
		}

		for _, span := range trace.Spans {
			if deduplicator(span) {
				tree := tftree.NewSpanTree(trace)
				if err := tree.SetRoot(span.SpanID); err != nil {
					return nil, fmt.Errorf("unexpected SetRoot error for span ID from trace: %w", err)
				}

				thumbnail := &jaegerbackend.TraceThumbnail{
					Identifier: identifier{
						TraceId: span.TraceID,
						SpanId:  span.SpanID,
					},
					Spans: tree.GetSpans(),
				}
				traceThumbnails = append(traceThumbnails, thumbnail)

				backend.Logger.WithField("ident", thumbnail.Identifier).
					WithField("filteredSpans", len(thumbnail.Spans)).
					Debug("matched trace")
			}
		}
	}

	return traceThumbnails, nil
}

func (backend *Backend) Get(
	ctx context.Context,
	identifierJson json.RawMessage,
	traceId model.TraceID,
	startTime, endTime time.Time,
) (*model.Trace, error) {
	var ident identifier
	if err := json.Unmarshal(identifierJson, &ident); err != nil {
		return nil, fmt.Errorf("persisted invalid trace identifier: %w", err)
	}

	trace, err := backend.reader.GetTrace(ctx, ident.TraceId)
	if err != nil {
		return nil, fmt.Errorf("failed to get trace from backend: %w", err)
	}

	if len(trace.Spans) == 0 {
		return nil, fmt.Errorf("jaeger storage backend returned empty trace")
	}

	tree := tftree.NewSpanTree(trace)
	if err := tree.SetRoot(ident.SpanId); err != nil {
		if errors.Is(err, tftree.ErrRootDoesNotExist) {
			return nil, fmt.Errorf("cached span does not exist in refreshed trace result: %w", err)
		}

		return nil, fmt.Errorf("error calling SetRoot: %w", err)
	}

	trace.Spans = tree.GetSpans()
	backend.Logger.WithField("filteredSpans", len(trace.Spans)).Debug("fetched trace")

	return trace, nil
}
