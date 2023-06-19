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
	"time"

	"github.com/jaegertracing/jaeger/model"

	"github.com/kubewharf/kelemetry/pkg/util"
)

// Produces new extension providers based on the configuration.
//
// This interface is used for manager.List.
type ProviderFactory interface {
	Kind() string

	Configure(jsonBuf []byte) (Provider, error)
}

// An instance of extension provider as specified in the config.
// Loads spans from a specific source, typically from one specific component.
type Provider interface {
	// Searches for spans associated with the parameters.
	//
	// This method is invoked during the first time a main trace is loaded.
	// Subsequent invocations will call `LoadCache` instead.
	//
	// `tags` contains the tags in the object pseudospan in the main trace.
	Fetch(
		object util.ObjectRef,
		tags model.KeyValues,
		start, end time.Duration,
	) (FetchResult, error)

	// Restores the FetchResult from the identifier
	// persisted from the result of a previous call to `Fetch`.
	LoadCache(identifier any) (FetchResult, error)
}

type FetchResult struct {
	// A JSON-serializable object used to presist the parameters of this fetch,
	// such as the extension trace ID.
	Identifier any
	// The spans in the extension trace.
	// Root spans are relocated as direct descendents of the object pseudospan.
	Spans []model.Span
}
