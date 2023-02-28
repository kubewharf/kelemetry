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

package jaegerreader

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	"github.com/kubewharf/kelemetry/pkg/frontend/clusterlist"
	transform "github.com/kubewharf/kelemetry/pkg/frontend/tf"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	"github.com/kubewharf/kelemetry/pkg/frontend/tracecache"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("jaeger-span-reader", newSpanReader)
}

type Interface interface {
	spanstore.Reader
}

type options struct{}

func (options *options) Setup(fs *pflag.FlagSet) {
}

func (options *options) EnableFlag() *bool { return nil }

type spanReader struct {
	options          options
	logger           logrus.FieldLogger
	backend          jaegerbackend.Backend
	traceCache       tracecache.Cache
	clusterList      clusterlist.Lister
	transformer      *transform.Transformer
	transformConfigs tfconfig.Provider
}

func newSpanReader(
	logger logrus.FieldLogger,
	backend jaegerbackend.Backend,
	traceCache tracecache.Cache,
	clusterList clusterlist.Lister,
	transformer *transform.Transformer,
	transformConfigs tfconfig.Provider,
) Interface {
	return &spanReader{
		logger:           logger,
		backend:          backend,
		traceCache:       traceCache,
		clusterList:      clusterList,
		transformer:      transformer,
		transformConfigs: transformConfigs,
	}
}

func (reader *spanReader) Options() manager.Options           { return &reader.options }
func (reader *spanReader) Init(ctx context.Context) error     { return nil }
func (reader *spanReader) Start(stopCh <-chan struct{}) error { return nil }
func (reader *spanReader) Close() error                       { return nil }

func (reader *spanReader) GetServices(ctx context.Context) ([]string, error) {
	configNames := []string{
		reader.transformConfigs.DefaultName(),
	}

	for _, name := range reader.transformConfigs.Names() {
		if name != reader.transformConfigs.DefaultName() {
			configNames = append(configNames, name)
		}
	}

	sort.Strings(configNames[1:])
	reader.logger.WithField("services", configNames).Info("query display mode list")

	return configNames, nil
}

func (reader *spanReader) GetOperations(ctx context.Context, query spanstore.OperationQueryParameters) ([]spanstore.Operation, error) {
	clusterNames := reader.clusterList.List()
	operations := make([]spanstore.Operation, 0, len(clusterNames))
	for _, verb := range clusterNames {
		operations = append(operations, spanstore.Operation{
			SpanKind: query.SpanKind,
			Name:     verb,
		})
	}

	return operations, nil
}

func (reader *spanReader) FindTraceIDs(ctx context.Context, query *spanstore.TraceQueryParameters) ([]model.TraceID, error) {
	traces, err := reader.FindTraces(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("FindTrace error: %w", err)
	}

	traceIds := make([]model.TraceID, 0, len(traces))
	for _, trace := range traces {
		if len(trace.Spans) > 0 {
			traceIds = append(traceIds, trace.Spans[0].TraceID)
		}
	}
	return traceIds, nil
}

func (reader *spanReader) FindTraces(ctx context.Context, query *spanstore.TraceQueryParameters) ([]*model.Trace, error) {
	thumbnails, err := reader.backend.List(ctx, query)
	if err != nil {
		return nil, err
	}

	configName := strings.TrimPrefix(query.ServiceName, "* ")
	config := reader.transformConfigs.GetByName(configName)
	if config == nil {
		return nil, fmt.Errorf("invalid display mode %q", query.ServiceName)
	}

	cacheEntries := []tracecache.Entry{}
	traces := []*model.Trace{}
	for _, thumbnail := range thumbnails {
		cacheId := generateCacheId(config.Id)
		cacheEntries = append(cacheEntries, tracecache.Entry{
			LowId:      cacheId.Low,
			Identifier: thumbnail.Identifier,
		})

		for _, span := range thumbnail.Spans {
			span.TraceID = cacheId
		}

		trace := &model.Trace{
			ProcessMap: []model.Trace_ProcessMapping{{
				ProcessID: "0",
				Process:   model.Process{ServiceName: fmt.Sprintf("%s %s", thumbnail.Cluster, thumbnail.Resource)},
			}},
			Spans: thumbnail.Spans,
		}

		displayMode := extractDisplayMode(cacheId)

		reader.transformer.Transform(trace, thumbnail.RootSpan, displayMode)
		traces = append(traces, trace)
	}

	if len(cacheEntries) > 0 {
		if err := reader.traceCache.Persist(ctx, cacheEntries); err != nil {
			return nil, fmt.Errorf("cannot persist trace cache: %w", err)
		}
	}

	return traces, nil
}

func (reader *spanReader) GetTrace(ctx context.Context, cacheId model.TraceID) (*model.Trace, error) {
	identifier, err := reader.traceCache.Fetch(ctx, cacheId.Low)
	if err != nil {
		return nil, fmt.Errorf("cannot lookup trace: %w", err)
	}

	trace, rootSpan, err := reader.backend.Get(ctx, identifier, cacheId)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch trace pointed by the cache: %w", err)
	}

	displayMode := extractDisplayMode(cacheId)

	reader.transformer.Transform(trace, rootSpan, displayMode)

	reader.logger.WithField("numTransformedSpans", len(trace.Spans)).Info("query trace tree")

	return trace, nil
}

const (
	CacheIdHighMask     uint64 = 0xFF00000000E1E3E7
	CacheIdHighBitShift uint64 = 6 * 4
)

func generateCacheId(mode tfconfig.Id) model.TraceID {
	// Format:
	// Low = random number
	// High = Prefix + mode + Suffix

	return model.TraceID{
		Low:  rand.Uint64(),
		High: CacheIdHighMask | (uint64(mode) << CacheIdHighBitShift),
	}
}

func extractDisplayMode(cacheId model.TraceID) tfconfig.Id {
	displayMode := cacheId.High >> CacheIdHighBitShift
	return tfconfig.Id(uint32(displayMode))
}
