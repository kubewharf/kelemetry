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
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	"github.com/kubewharf/kelemetry/pkg/frontend/clusterlist"
	"github.com/kubewharf/kelemetry/pkg/frontend/reader/merge"
	transform "github.com/kubewharf/kelemetry/pkg/frontend/tf"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/frontend/tracecache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("jaeger-span-reader", manager.Ptr[Interface](&spanReader{}))
}

type Interface interface {
	spanstore.Reader
}

type options struct {
	cacheExtensions       bool
	followLinkConcurrency int
	followLinkLimit       int32
	followLinksInList     bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(
		&options.cacheExtensions,
		"jaeger-extension-cache",
		false,
		"cache extension trace search result, otherwise trace is searched again every time result is reloaded",
	)
	fs.IntVar(
		&options.followLinkConcurrency,
		"frontend-follow-link-concurrency",
		20,
		"number of concurrent trace per request to follow links",
	)
	fs.Int32Var(
		&options.followLinkLimit,
		"frontend-follow-link-limit",
		10,
		"maximum number of linked objects to follow per search result",
	)
	fs.BoolVar(
		&options.followLinksInList,
		"frontend-follow-links-in-list",
		true,
		"whether links should be recursed into when listing traces",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type spanReader struct {
	options          options
	Logger           logrus.FieldLogger
	Clock            clock.Clock
	Backend          jaegerbackend.Backend
	TraceCache       tracecache.Cache
	ClusterList      clusterlist.Lister
	Transformer      *transform.Transformer
	TransformConfigs tfconfig.Provider

	GetServicesMetric         *metrics.Metric[*GetServicesMetric]
	GetOperationsMetric       *metrics.Metric[*GetOperationsMetric]
	FindTracesMetric          *metrics.Metric[*FindTracesMetric]
	FindTracesTimeRangeMetric *metrics.Metric[*FindTracesTimeRangeMetric]
	GetTraceMetric            *metrics.Metric[*GetTraceMetric]
}

type GetServicesMetric struct{}

func (*GetServicesMetric) MetricName() string { return "frontend_get_services" }

type GetOperationsMetric struct {
	Service string
}

func (*GetOperationsMetric) MetricName() string { return "frontend_get_operations" }

type FindTracesMetric struct{}

func (*FindTracesMetric) MetricName() string { return "frontend_find_traces" }

type FindTracesTimeRangeMetric struct{}

func (*FindTracesTimeRangeMetric) MetricName() string { return "frontend_find_traces_time_range" }

type GetTraceMetric struct{}

func (*GetTraceMetric) MetricName() string { return "frontend_get_trace" }

func (reader *spanReader) Options() manager.Options        { return &reader.options }
func (reader *spanReader) Init() error                     { return nil }
func (reader *spanReader) Start(ctx context.Context) error { return nil }
func (reader *spanReader) Close(ctx context.Context) error { return nil }

func (reader *spanReader) GetServices(ctx context.Context) ([]string, error) {
	defer reader.GetServicesMetric.DeferCount(reader.Clock.Now(), &GetServicesMetric{})

	configNames := []string{
		reader.TransformConfigs.DefaultName(),
	}

	for _, name := range reader.TransformConfigs.Names() {
		if name != reader.TransformConfigs.DefaultName() {
			configNames = append(configNames, name)
		}
	}

	reader.Logger.WithField("services", configNames).Info("query display mode list")

	return configNames, nil
}

func (reader *spanReader) GetOperations(ctx context.Context, query spanstore.OperationQueryParameters) ([]spanstore.Operation, error) {
	defer reader.GetOperationsMetric.DeferCount(reader.Clock.Now(), &GetOperationsMetric{Service: query.ServiceName})

	clusterNames := reader.ClusterList.List()
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
	defer reader.FindTracesMetric.DeferCount(reader.Clock.Now(), &FindTracesMetric{})

	reader.FindTracesTimeRangeMetric.With(&FindTracesTimeRangeMetric{}).Summary(query.StartTimeMax.Sub(query.StartTimeMin).Seconds())

	configName := strings.TrimPrefix(query.ServiceName, "* ")
	config := reader.TransformConfigs.GetByName(configName)
	if config == nil {
		return nil, fmt.Errorf("invalid display mode %q", query.ServiceName)
	}

	reader.Logger.WithField("query", query).
		WithField("config", config.Name).
		Debug("start trace list")

	if len(query.OperationName) > 0 {
		if query.Tags == nil {
			query.Tags = map[string]string{}
		}
		query.Tags["cluster"] = query.OperationName
	}

	tts, err := reader.Backend.List(ctx, query)
	if err != nil {
		return nil, err
	}

	twmList := make([]merge.TraceWithMetadata[any], len(tts))
	for i, tt := range tts {
		twmList[i] = merge.TraceWithMetadata[any]{
			Tree:     tt.Spans,
			Metadata: tt.Identifier,
		}
	}

	merger := merge.Merger[any]{}
	if _, err := merger.AddTraces(twmList); err != nil {
		return nil, fmt.Errorf("group traces by object: %w", err)
	}

	if reader.options.followLinksInList {
		if err := merger.FollowLinks(
			ctx,
			config.LinkSelector,
			query.StartTimeMin, query.StartTimeMax,
			mergeListWithBackend[any](reader.Backend, reflectutil.Identity[any], OriginalTraceRequest{FindTraces: query}),
			reader.options.followLinkConcurrency, reader.options.followLinkLimit, false,
		); err != nil {
			return nil, fmt.Errorf("follow links: %w", err)
		}
	}

	mergeTrees, err := merger.MergeTraces()
	if err != nil {
		return nil, fmt.Errorf("merging split and linked traces: %w", err)
	}

	var rootKey *utilobject.Key
	if rootKeyValue, ok := utilobject.FromMap(query.Tags); ok {
		rootKey = &rootKeyValue
	}

	cacheEntries := []tracecache.Entry{}
	traces := []*model.Trace{}
	for _, mergeTree := range mergeTrees {
		cacheId := generateCacheId(config.Id)

		trace, extensionCache, err := reader.prepareEntry(ctx, rootKey, query, mergeTree.Tree, cacheId)
		if err != nil {
			return nil, err
		}

		traces = append(traces, trace)

		cacheEntry, err := reader.prepareCache(rootKey, query, mergeTree.Metadata, cacheId, extensionCache)
		if err != nil {
			return nil, err
		}

		cacheEntries = append(cacheEntries, cacheEntry)
	}

	if len(cacheEntries) > 0 {
		if err := reader.TraceCache.Persist(ctx, cacheEntries); err != nil {
			return nil, fmt.Errorf("cannot persist trace cache: %w", err)
		}
	}

	reader.Logger.WithField("numTraces", len(traces)).Info("query trace list")

	return traces, nil
}

func (reader *spanReader) prepareEntry(
	ctx context.Context,
	rootKey *utilobject.Key,
	query *spanstore.TraceQueryParameters,
	tree *tftree.SpanTree,
	cacheId model.TraceID,
) (*model.Trace, []tracecache.ExtensionCache, error) {
	spans := tree.GetSpans()

	for _, span := range spans {
		span.TraceID = cacheId
		for i := range span.References {
			span.References[i].TraceID = cacheId
		}
	}

	spans = filterTimeRange(spans, query.StartTimeMin, query.StartTimeMax)

	trace := &model.Trace{
		ProcessMap: []model.Trace_ProcessMapping{{
			ProcessID: "0",
			Process:   model.Process{},
		}},
		Spans: spans,
	}

	displayMode := extractDisplayMode(cacheId)

	extensions := &transform.FetchExtensionsAndStoreCache{}

	if err := reader.Transformer.Transform(
		ctx, trace, rootKey, displayMode,
		extensions,
		query.StartTimeMin, query.StartTimeMax,
	); err != nil {
		return nil, nil, fmt.Errorf("trace transformation failed: %w", err)
	}

	return trace, extensions.Cache, nil
}

func (reader *spanReader) prepareCache(
	rootKey *utilobject.Key,
	query *spanstore.TraceQueryParameters,
	identifiers []any,
	cacheId model.TraceID,
	extensionCache []tracecache.ExtensionCache,
) (tracecache.Entry, error) {
	identifiersJson := make([]json.RawMessage, len(identifiers))
	for i, identifier := range identifiers {
		idJson, err := json.Marshal(identifier)
		if err != nil {
			return tracecache.Entry{}, fmt.Errorf("thumbnail identifier marshal: %w", err)
		}

		identifiersJson[i] = json.RawMessage(idJson)
	}

	cacheEntry := tracecache.Entry{
		LowId: cacheId.Low,
		Value: tracecache.EntryValue{
			Identifiers: identifiersJson,
			StartTime:   query.StartTimeMin,
			EndTime:     query.StartTimeMax,
			RootObject:  rootKey,
		},
	}
	if reader.options.cacheExtensions {
		cacheEntry.Value.Extensions = extensionCache
	}

	return cacheEntry, nil
}

func (reader *spanReader) GetTrace(ctx context.Context, cacheId model.TraceID) (*model.Trace, error) {
	defer reader.GetTraceMetric.DeferCount(reader.Clock.Now(), &GetTraceMetric{})

	entry, err := reader.TraceCache.Fetch(ctx, cacheId.Low)
	if err != nil {
		return nil, fmt.Errorf("cannot lookup trace: %w", err)
	}
	if entry == nil {
		return nil, fmt.Errorf("trace %v not found", cacheId)
	}

	displayMode := extractDisplayMode(cacheId)

	traces := make([]merge.TraceWithMetadata[struct{}], 0, len(entry.Identifiers))
	for _, identifier := range entry.Identifiers {
		trace, err := reader.Backend.Get(ctx, identifier, cacheId, entry.StartTime, entry.EndTime)
		if err != nil {
			return nil, fmt.Errorf("cannot fetch trace pointed by the cache: %w", err)
		}

		clipped := filterTimeRange(trace.Spans, entry.StartTime, entry.EndTime)
		traces = append(traces, merge.TraceWithMetadata[struct{}]{Tree: tftree.NewSpanTree(clipped)})
	}

	merger := merge.Merger[struct{}]{}
	if _, err := merger.AddTraces(traces); err != nil {
		return nil, fmt.Errorf("grouping traces by object: %w", err)
	}

	displayConfig := reader.TransformConfigs.GetById(displayMode)
	if displayConfig == nil {
		return nil, fmt.Errorf("display mode %x does not exist", displayMode)
	}

	if err := merger.FollowLinks(
		ctx,
		displayConfig.LinkSelector,
		entry.StartTime, entry.EndTime,
		mergeListWithBackend[struct{}](reader.Backend, func(any) struct{} { return struct{}{} }, OriginalTraceRequest{GetTrace: &cacheId}),
		reader.options.followLinkConcurrency, reader.options.followLinkLimit, true,
	); err != nil {
		return nil, fmt.Errorf("cannot follow links: %w", err)
	}
	mergedTrees, err := merger.MergeTraces()
	if err != nil {
		return nil, fmt.Errorf("merging linked trees: %w", err)
	}

	// if spans were connected, they should continue to be connected since link spans cannot be deleted, so assume there is only one trace
	if len(mergedTrees) != 1 {
		return nil, fmt.Errorf("inconsistent linked trace count %d", len(mergedTrees))
	}
	mergedTree := mergedTrees[0]
	aggTrace := &model.Trace{
		ProcessMap: []model.Trace_ProcessMapping{{
			ProcessID: "0",
		}},
		Spans: mergedTree.Tree.GetSpans(),
	}

	var extensions transform.ExtensionProcessor = &transform.FetchExtensionsAndStoreCache{}
	if reader.options.cacheExtensions && len(entry.Extensions) > 0 {
		extensions = &transform.LoadExtensionCache{Cache: entry.Extensions}
	}

	if err := reader.Transformer.Transform(
		ctx, aggTrace, entry.RootObject, displayMode,
		extensions,
		entry.StartTime, entry.EndTime,
	); err != nil {
		return nil, fmt.Errorf("trace transformation failed: %w", err)
	}

	reader.Logger.WithField("numTransformedSpans", len(aggTrace.Spans)).Info("query trace tree")

	return aggTrace, nil
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

func filterTimeRange(spans []*model.Span, startTime, endTime time.Time) []*model.Span {
	var retained []*model.Span

	for _, span := range spans {
		traceSource, exists := model.KeyValues(span.Tags).FindByKey(zconstants.TraceSource)
		if exists && traceSource.VStr == zconstants.TraceSourceObject {
			// pseudo span, timestamp has no meaning
			span.StartTime = startTime
			span.Duration = endTime.Sub(startTime)
			retained = append(retained, span)
		} else {
			// normal span, filter away if start time is out of bounds
			if startTime.Before(span.StartTime) && span.StartTime.Before(endTime) {
				retained = append(retained, span)
			}
		}
	}

	return retained
}

type (
	OriginalTraceRequestKey struct{}
	OriginalTraceRequest    struct {
		GetTrace   *model.TraceID
		FindTraces *spanstore.TraceQueryParameters
	}
)

func mergeListWithBackend[M any](backend jaegerbackend.Backend, convertMetadata func(any) M, otr OriginalTraceRequest) merge.ListFunc[M] {
	return func(
		ctx context.Context,
		key utilobject.Key,
		startTime time.Time, endTime time.Time,
		limit int,
	) ([]merge.TraceWithMetadata[M], error) {
		tags := zconstants.KeyToSpanTags(key)

		tts, err := backend.List(
			context.WithValue(ctx, OriginalTraceRequestKey{}, otr),
			&spanstore.TraceQueryParameters{
				Tags:         tags,
				StartTimeMin: startTime,
				StartTimeMax: endTime,
				NumTraces:    limit,
			},
		)
		if err != nil {
			return nil, err
		}

		twmList := make([]merge.TraceWithMetadata[M], len(tts))
		for i, tt := range tts {
			twmList[i] = merge.TraceWithMetadata[M]{
				Tree:     tt.Spans,
				Metadata: convertMetadata(tt.Identifier),
			}
		}

		return twmList, nil
	}
}
