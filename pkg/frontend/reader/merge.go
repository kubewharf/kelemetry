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
	"github.com/jaegertracing/jaeger/model"
	"k8s.io/apimachinery/pkg/util/sets"

	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

type mergeMap struct {
	ptrSet   sets.Set[*mergeEntry]
	fromKeys map[utilobject.Key]*mergeEntry
}

type mergeEntry struct {
	keys        sets.Set[utilobject.Key]
	identifiers []any
	spans       []*model.Span
}

func singletonMerged(keys sets.Set[utilobject.Key], thumbnail *jaegerbackend.TraceThumbnail) *mergeEntry {
	return &mergeEntry{
		keys:        keys,
		identifiers: []any{thumbnail.Identifier},
		spans:       thumbnail.Spans,
	}
}

func (entry *mergeEntry) join(other *mergeEntry) {
	for key := range other.keys {
		entry.keys.Insert(key)
	}

	entry.identifiers = append(entry.identifiers, other.identifiers...)
	entry.spans = append(entry.spans, other.spans...)
}

// add a thumbnail with a preferred root key.
func (m *mergeMap) add(keys sets.Set[utilobject.Key], thumbnail *jaegerbackend.TraceThumbnail) {
	entry := singletonMerged(keys.Clone(), thumbnail)
	m.ptrSet.Insert(entry)

	dups := sets.New[*mergeEntry]()

	for key := range keys {
		if prev, hasPrev := m.fromKeys[key]; hasPrev {
			dups.Insert(prev)
		}
	}

	for dup := range dups {
		entry.join(dup)
		m.ptrSet.Delete(dup)
	}

	for key := range entry.keys {
		// including all new and joined keys
		m.fromKeys[key] = entry
	}
}

func mergeSegments(thumbnails []*jaegerbackend.TraceThumbnail) []*mergeEntry {
	m := mergeMap{
		ptrSet:   sets.New[*mergeEntry](),
		fromKeys: map[utilobject.Key]*mergeEntry{},
	}

	for _, thumbnail := range thumbnails {
		keys := utilobject.FromSpans(thumbnail.Spans)
		m.add(keys, thumbnail)
	}

	return m.ptrSet.UnsortedList()
}
