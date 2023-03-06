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

package metrics

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func NewMock(clock clock.Clock) (Client, *Mock) {
	recorder := &Mock{
		clock: clock,
	}

	return &mux{
		Mux: manager.NewMockMux("metrics", "mock", recorder),
	}, recorder
}

type Mock struct {
	manager.MuxImplBase
	manager.BaseComponent

	clock   clock.Clock
	metrics sync.Map // map[string]*mockEntry
}

func (mock *Mock) Get(name string, tags map[string]string) *MockEntry {
	key := keyOf(name, tags)

	entryAny, loaded := mock.metrics.Load(key)
	if !loaded {
		return &MockEntry{}
	}

	entry := entryAny.(*MockEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	i := entry.Int
	list := make([]int64, len(entry.Hist))
	copy(list, entry.Hist)
	return &MockEntry{Int: i, Hist: list}
}

func (mock *Mock) PrintAll() string {
	var s string
	mock.metrics.Range(func(key, valueAny any) bool {
		value := valueAny.(*MockEntry)
		value.mu.Lock()
		defer value.mu.Unlock()
		s += fmt.Sprintf("\n%s: %d %v", key.(string), value.Int, value.Hist)
		return true
	})
	return s
}

var _ Impl = &Mock{}

func (mock *Mock) MuxImplName() (name string, isDefault bool) { panic("unreachable") }

func (mock *Mock) New(name string, tagNames []string) MetricImpl {
	return &mockImpl{
		metrics:  &mock.metrics,
		clock:    mock.clock,
		name:     name,
		tagNames: tagNames,
	}
}

type mockImpl struct {
	metrics  *sync.Map
	clock    clock.Clock
	name     string
	tagNames []string
}

type MockEntry struct {
	mu   sync.Mutex
	Int  int64
	Hist []int64
}

var _ MetricImpl = &mockImpl{}

func (mi *mockImpl) Count(value int64, tags []string) {
	mi.record(tags, func(entry *MockEntry) { entry.Int += value })
}

func (mi *mockImpl) Histogram(value int64, tags []string) {
	mi.record(tags, func(entry *MockEntry) { entry.Hist = append(entry.Hist, value) })
}

func (mi *mockImpl) Gauge(value int64, tags []string) {
	mi.record(tags, func(entry *MockEntry) { entry.Int = value })
}

func (mi *mockImpl) Defer(start time.Time, tags []string) {
	mi.record(tags, func(me *MockEntry) {})
}

func (mi *mockImpl) record(tags []string, modifier func(*MockEntry)) {
	tagsMap := map[string]string{}
	if len(mi.tagNames) != len(tags) {
		panic("tags are inconsistent")
	}
	for i := 0; i < len(tags); i++ {
		tagsMap[mi.tagNames[i]] = tags[i]
	}

	key := keyOf(mi.name, tagsMap)
	entryAny, _ := mi.metrics.LoadOrStore(key, &MockEntry{})
	entry := entryAny.(*MockEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	modifier(entry)
}

func keyOf(name string, tags map[string]string) string {
	jsonBuf, err := json.Marshal(tags)
	if err != nil {
		panic(fmt.Errorf("invalid tag values: %w", err))
	}

	return name + string(jsonBuf)
}
