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

package extension

import (
	"context"
	"time"

	"github.com/jaegertracing/jaeger/model"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

// Produces new extension providers based on the configuration.
//
// This interface is used for manager.List.
type ProviderFactory interface {
	manager.IndexedListImpl

	Configure(jsonBuf []byte) (Provider, error)
}

// An instance of extension provider as specified in the config.
// Loads spans from a specific source, typically from one specific component.
type Provider interface {
	// Kind of this extension provider, same as the one from `ProviderFactory.Kind()`
	Kind() string

	// The raw JSON config buffer used to configure this provider.
	RawConfig() []byte

	// Maximum wall time to execute all fetch calls.
	// Only successuful queries before this timestamp will be included in the output.
	TotalTimeout() time.Duration
	// Maximum concurrent fetch calls to execute for the same trace.
	MaxConcurrency() int

	// Searches for spans associated with an object.
	//
	// This method is invoked during the first time a main trace is loaded.
	// Subsequent invocations will call `LoadCache` instead.
	//
	// Returned traces are attached as subtrees under the object pseudospan.
	//
	// `tags` contains the tags in the object pseudospan in the main trace.
	FetchForObject(
		ctx context.Context,
		object util.ObjectRef,
		tags model.KeyValues,
		start, end time.Time,
	) (*FetchResult, error)

	// Searches for spans associated with an object version.
	//
	// This method is invoked during the first time a main trace is loaded.
	// Subsequent invocations will call `LoadCache` instead.
	//
	// Returned traces are attached as subtrees under the audit span.
	//
	// `tags` contains the tags in the object pseudospan in the main trace.
	FetchForVersion(
		ctx context.Context,
		object util.ObjectRef,
		resourceVersion string,
		tags model.KeyValues,
		start, end time.Time,
	) (*FetchResult, error)

	// Restores the FetchResult from the identifier
	// persisted from the result of a previous call to `Fetch`.
	LoadCache(ctx context.Context, identifier []byte) ([]*model.Span, error)
}

type FetchResult struct {
	// A JSON-serializable object used to persist the parameters of this fetch,
	// such as the extension trace ID.
	Identifier any
	// The spans in the extension trace.
	//
	// Orphan spans (with no or invalid parent reference) are treated as root spans.
	Spans []*model.Span
}
