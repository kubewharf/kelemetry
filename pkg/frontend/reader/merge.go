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
	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
	"k8s.io/apimachinery/pkg/util/sets"
)

type mergeKey struct {
	cluster string
	resource string
	namespace string
	name string
}

type merger struct {
	childToRoot map[mergeKey]mergeKey
	roots map[mergeKey]*merged
}

type merged struct {
	identifier []any
	cluster  string
	resource string

	spans []*model.Span
	rootSpan model.SpanID
}

func singletonMerged(thumbnail *jaegerbackend.TraceThumbnail) *merged {
	return &merged{
		identifier: []any{thumbnail.Identifier},
		cluster: thumbnail.Cluster,
		resource: thumbnail.Resource,
		spans: thumbnail.Spans,
		rootSpan: thumbnail.RootSpan,
	}
}

func (merged *merged) add(thumbnail *jaegerbackend.TraceThumbnail) {
	merged.identifier = append(merged.identifier, thumbnail.Identifier)
	merged.spans = append(merged.spans, thumbnail.Spans...)
}

func (merge *merger) add(rootKey mergeKey, thumbnail *jaegerbackend.TraceThumbnail) mergeKey {
	if newRoot, hasNewRoot := merge.childToRoot[rootKey]; hasNewRoot {
		rootKey = newRoot
	}

	if prev, hasPrev := merge.roots[rootKey]; hasPrev {
		prev.add(thumbnail)
	} else {
		merge.roots[rootKey] = singletonMerged(thumbnail)
	}

	return rootKey
}

func mergeSegments(thumbnails []*jaegerbackend.TraceThumbnail) []*merged {
	merger := merger {
		childToRoot : map[mergeKey]mergeKey{},
		roots :map[mergeKey]*merged{},
	}

	for _, thumbnail := range thumbnails {
		rootKey, keys := getMergeKeys(thumbnail.Spans)

		rootKey = merger.add(rootKey, thumbnail)

		for key := range keys {
			merger.childToRoot[key] = rootKey
		}
	}

	traces := make([]*merged, 0, len(merger.childToRoot))
	for _, trace := range merger.roots {
		traces = append(traces, trace)
	}
	return traces
}

func getMergeKeys(spans []*model.Span) (rootKey mergeKey, keys sets.Set[mergeKey]) {
	keys = sets.Set[mergeKey]{}

	for _, span := range spans {
		tags := model.KeyValues(span.Tags)
		traceSource, hasTraceSource := tags.FindByKey(zconstants.TraceSource)
		if !hasTraceSource || traceSource.VStr != zconstants.TraceSourceObject {
			continue
		}

		cluster, _ := tags.FindByKey("cluster")
		resource, _ := tags.FindByKey("resource")
		namespace, _ := tags.FindByKey("namespace")
		name, _ := tags.FindByKey("name")
		key := mergeKey{cluster:cluster.VStr,resource:resource.VStr,namespace:namespace.VStr,name:name.VStr}
		keys.Insert(key)

		if len(span.References) == 0 {
			rootKey = key
		}
	}
	return rootKey, keys
}
