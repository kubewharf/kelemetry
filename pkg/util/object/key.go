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

package utilobject

import (
	"fmt"
	"strings"

	"github.com/jaegertracing/jaeger/model"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type Key struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (key Key) Clone() Key {
	return Key{
		Cluster:   strings.Clone(key.Cluster),
		Group:     strings.Clone(key.Group),
		Resource:  strings.Clone(key.Resource),
		Namespace: strings.Clone(key.Namespace),
		Name:      strings.Clone(key.Name),
	}
}

func (key Key) GroupResource() schema.GroupResource {
	return schema.GroupResource{Group: key.Group, Resource: key.Resource}
}

func (key Key) String() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", key.Cluster, key.Group, key.Resource, key.Namespace, key.Name)
}

func (key Key) AsFields(prefix string) logrus.Fields {
	return logrus.Fields{
		prefix + "Cluster":   key.Cluster,
		prefix + "Group":     key.Group,
		prefix + "Resource":  key.Resource,
		prefix + "Namespace": key.Namespace,
		prefix + "Name":      key.Name,
	}
}

func FromMap(tags map[string]string) (key Key, ok bool) {
	for mapKey, field := range map[string]*string{
		"cluster":   &key.Cluster,
		"group":     &key.Group,
		"resource":  &key.Resource,
		"namespace": &key.Namespace,
		"name":      &key.Name,
	} {
		*field, ok = tags[mapKey]
		if !ok {
			return key, false
		}
	}

	return key, true
}

func FromSpan(span *model.Span) (Key, bool) {
	tags := model.KeyValues(span.Tags)
	traceSource, hasTraceSource := tags.FindByKey(zconstants.TraceSource)
	if !hasTraceSource || traceSource.VStr != zconstants.TraceSourceObject {
		return Key{}, false
	}

	cluster, _ := tags.FindByKey("cluster")
	group, _ := tags.FindByKey("group")
	resource, _ := tags.FindByKey("resource")
	namespace, _ := tags.FindByKey("namespace")
	name, _ := tags.FindByKey("name")
	key := Key{
		Cluster:   cluster.VStr,
		Group:     group.VStr,
		Resource:  resource.VStr,
		Namespace: namespace.VStr,
		Name:      name.VStr,
	}
	return key, true
}

func FromSpans(spans []*model.Span) sets.Set[Key] {
	keys := sets.New[Key]()

	for _, span := range spans {
		if key, ok := FromSpan(span); ok {
			keys.Insert(key)
		}
	}
	return keys
}

type VersionedKey struct {
	Key
	Version string `json:"version"`
}

func (key VersionedKey) Clone() VersionedKey {
	return VersionedKey{
		Key:     key.Key.Clone(),
		Version: strings.Clone(key.Version),
	}
}

func (key VersionedKey) GroupVersionResource() schema.GroupVersionResource {
	return key.GroupResource().WithVersion(key.Version)
}

func (key VersionedKey) GroupVersion() schema.GroupVersion {
	return schema.GroupVersion{Group: key.Group, Version: key.Version}
}

func FromObject(object metav1.Object, cluster string, gvr schema.GroupVersionResource) VersionedKey {
	return VersionedKey{
		Key: Key{
			Cluster:   cluster,
			Group:     gvr.Group,
			Resource:  gvr.Resource,
			Namespace: object.GetNamespace(),
			Name:      object.GetName(),
		},
		Version: gvr.Version,
	}
}
