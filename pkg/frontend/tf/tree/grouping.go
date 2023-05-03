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

package tftree

import (
	"github.com/jaegertracing/jaeger/model"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type GroupingKey struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Field     string `json:"field"`
}

func GroupingKeyFromMap(tags map[string]string) (key GroupingKey, ok bool) {
	for mapKey, field := range map[string]*string{
		"cluster":   &key.Cluster,
		"group":     &key.Group,
		"resource":  &key.Resource,
		"namespace": &key.Namespace,
		"name":      &key.Name,
	} {
		*field, ok = tags[mapKey]
		if !ok {
			return
		}
	}

	if field, hasField := tags["field"]; hasField {
		key.Field = field
	} else {
		key.Field = "object"
	}

	return key, true
}

func GroupingKeyFromSpan(span *model.Span) (GroupingKey, bool) {
	tags := model.KeyValues(span.Tags)
	traceSource, hasTraceSource := tags.FindByKey(zconstants.TraceSource)
	if !hasTraceSource || traceSource.VStr != zconstants.TraceSourceObject {
		return GroupingKey{}, false
	}

	cluster, _ := tags.FindByKey("cluster")
	group, _ := tags.FindByKey("group")
	resource, _ := tags.FindByKey("resource")
	namespace, _ := tags.FindByKey("namespace")
	name, _ := tags.FindByKey("name")
	field, _ := tags.FindByKey(zconstants.NestLevel)
	key := GroupingKey{
		Cluster:   cluster.VStr,
		Group:     group.VStr,
		Resource:  resource.VStr,
		Namespace: namespace.VStr,
		Name:      name.VStr,
		Field:     field.VStr,
	}
	return key, true
}

func GroupingKeysFromSpans(spans []*model.Span) sets.Set[GroupingKey] {
	keys := sets.New[GroupingKey]()

	for _, span := range spans {
		if key, ok := GroupingKeyFromSpan(span); ok {
			keys.Insert(key)
		}
	}
	return keys
}
