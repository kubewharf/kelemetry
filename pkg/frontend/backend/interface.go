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

package jaegerbackend

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

func init() {
	manager.Global.Provide("jaeger-backend", manager.Ptr[Backend](&mux{
		Mux: manager.NewMux("jaeger-backend", false),
	}))
}

type Backend interface {
	// Lists the thumbnail previews of all traces.
	List(
		ctx context.Context,
		query *spanstore.TraceQueryParameters,
		exclusive bool,
	) ([]*TraceThumbnail, error)

	// Gets the full tree of a trace based on the identifier returned from a prvious call to List.
	//
	// traceId is the fake trace ID that should be presented to the user.
	//
	// startTime and endTime are only for optimization hint.
	// The implementation is allowed to return spans beyond the range.
	Get(
		ctx context.Context,
		identifier json.RawMessage,
		traceId model.TraceID,
		startTime, endTime time.Time,
	) (*model.Trace, error)
}

type TraceThumbnail struct {
	// Identifier is a serializable object that identifies the trace in GetTrace calls.
	Identifier any

	Spans []*model.Span
}

type mux struct {
	*manager.Mux
	Clock clock.Clock

	ListMetric *metrics.Metric[*listMetric]
	GetMetric  *metrics.Metric[*getMetric]
}

type (
	listMetric struct{}
	getMetric  struct{}
)

func (*listMetric) MetricName() string { return "jaeger_backend_list" }
func (*getMetric) MetricName() string  { return "jaeger_backend_get" }

func (mux *mux) List(
	ctx context.Context,
	query *spanstore.TraceQueryParameters,
	exclusive bool,
) ([]*TraceThumbnail, error) {
	defer mux.ListMetric.DeferCount(mux.Clock.Now(), &listMetric{})
	return mux.Impl().(Backend).List(ctx, query, exclusive)
}

func (mux *mux) Get(
	ctx context.Context,
	identifier json.RawMessage,
	traceId model.TraceID,
	startTime, endTime time.Time,
) (*model.Trace, error) {
	defer mux.GetMetric.DeferCount(mux.Clock.Now(), &getMetric{})
	return mux.Impl().(Backend).Get(ctx, identifier, traceId, startTime, endTime)
}
