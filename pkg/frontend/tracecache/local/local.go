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

package local

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	tracecache "github.com/kubewharf/kelemetry/pkg/frontend/tracecache"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideMuxImpl("jaeger-trace-cache/local", manager.Ptr(&localCache{
		data: map[uint64]tracecache.EntryValue{},
	}), tracecache.Cache.Persist)
}

var _ tracecache.Cache = &localCache{}

type localCache struct {
	manager.MuxImplBase

	Logger logrus.FieldLogger

	data     map[uint64]tracecache.EntryValue
	dataLock sync.RWMutex
}

func (_ *localCache) MuxImplName() (name string, isDefault bool) { return "local", true }

func (cache *localCache) Options() manager.Options { return &manager.NoOptions{} }

func (cache *localCache) Init() error { return nil }

func (cache *localCache) Start(ctx context.Context) error { return nil }

func (cache *localCache) Close(ctx context.Context) error { return nil }

func (cache *localCache) Persist(ctx context.Context, entries []tracecache.Entry) error {
	cache.dataLock.Lock()
	defer cache.dataLock.Unlock()

	for _, entry := range entries {
		cache.data[entry.LowId] = entry.Value
	}

	return nil
}

func (cache *localCache) Fetch(ctx context.Context, lowId uint64) (*tracecache.EntryValue, error) {
	cache.dataLock.RLock()
	defer cache.dataLock.RUnlock()

	if value, exists := cache.data[lowId]; exists {
		return &value, nil
	}

	return nil, fmt.Errorf("No trace cache for key %x", lowId)
}
