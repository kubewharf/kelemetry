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

package tracecache

import (
	"context"
	"encoding/json"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("jaeger-trace-cache", manager.Ptr[Cache](&mux{
		Mux: manager.NewMux("jaeger-trace-cache", false),
	}))
}

type Cache interface {
	Persist(ctx context.Context, entries []Entry) error
	Fetch(ctx context.Context, lowId uint64) (json.RawMessage, error)
}

type Entry struct {
	LowId      uint64
	Identifier any
}

type mux struct {
	*manager.Mux
}

func (mux *mux) Persist(ctx context.Context, entries []Entry) error {
	return mux.Impl().(Cache).Persist(ctx, entries)
}

func (mux *mux) Fetch(ctx context.Context, lowId uint64) (json.RawMessage, error) {
	return mux.Impl().(Cache).Fetch(ctx, lowId)
}
