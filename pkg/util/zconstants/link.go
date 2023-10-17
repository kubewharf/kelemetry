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

package zconstants

import (
	"github.com/jaegertracing/jaeger/model"

	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

type LinkRef struct {
	Key   utilobject.Key
	Role  LinkRoleValue
	Class string
}

// Tags for TraceSourceLink spans that indicate the linked object.
const (
	LinkedObjectCluster   = "linkedCluster"
	LinkedObjectGroup     = "linkedGroup"
	LinkedObjectResource  = "linkedResource"
	LinkedObjectNamespace = "linkedNamespace"
	LinkedObjectName      = "linkedName"

	// Indicates how the linked trace interacts with the current trace.
	LinkRole = "linkRole"

	// If this tag is nonempty, a virtual span is inserted between the linked objects with the tag value as the name.
	LinkClass = "linkClass"
)

func TagLinkedObject(tags map[string]string, ln LinkRef) {
	tags[LinkedObjectCluster] = ln.Key.Cluster
	tags[LinkedObjectGroup] = ln.Key.Group
	tags[LinkedObjectResource] = ln.Key.Resource
	tags[LinkedObjectNamespace] = ln.Key.Namespace
	tags[LinkedObjectName] = ln.Key.Name
	tags[LinkRole] = string(ln.Role)
	tags[LinkClass] = ln.Class
}

func ObjectKeyFromSpan(span *model.Span) utilobject.Key {
	tags := model.KeyValues(span.Tags)

	cluster, _ := tags.FindByKey("cluster")
	group, _ := tags.FindByKey("group")
	resource, _ := tags.FindByKey("resource")
	namespace, _ := tags.FindByKey("namespace")
	name, _ := tags.FindByKey("name")
	key := utilobject.Key{
		Cluster:   cluster.VStr,
		Group:     group.VStr,
		Resource:  resource.VStr,
		Namespace: namespace.VStr,
		Name:      name.VStr,
	}
	return key
}

func LinkedKeyFromSpan(span *model.Span) (utilobject.Key, bool) {
	tags := model.KeyValues(span.Tags)
	pseudoType, isPseudo := tags.FindByKey(PseudoType)
	if !isPseudo || pseudoType.VStr != string(PseudoTypeLink) {
		return utilobject.Key{}, false
	}

	cluster, _ := tags.FindByKey(LinkedObjectCluster)
	group, _ := tags.FindByKey(LinkedObjectGroup)
	resource, _ := tags.FindByKey(LinkedObjectResource)
	namespace, _ := tags.FindByKey(LinkedObjectNamespace)
	name, _ := tags.FindByKey(LinkedObjectName)
	key := utilobject.Key{
		Cluster:   cluster.VStr,
		Group:     group.VStr,
		Resource:  resource.VStr,
		Namespace: namespace.VStr,
		Name:      name.VStr,
	}
	return key, true
}

func KeyToSpanTags(key utilobject.Key) map[string]string {
	return map[string]string{
		"cluster":   key.Cluster,
		"group":     key.Group,
		"resource":  key.Resource,
		"namespace": key.Namespace,
		"name":      key.Name,
	}
}

func VersionedKeyToSpanTags(key utilobject.VersionedKey) map[string]string {
	m := KeyToSpanTags(key.Key)
	m["version"] = key.Version
	return m
}

type LinkRoleValue string

const (
	// The current trace is a child trace under the linked trace
	LinkRoleParent LinkRoleValue = "parent"

	// The linked trace is a child trace under the current trace.
	LinkRoleChild LinkRoleValue = "child"
)

// Determines the role of the reverse link.
func ReverseLinkRole(role LinkRoleValue) LinkRoleValue {
	switch role {
	case LinkRoleParent:
		return LinkRoleChild
	case LinkRoleChild:
		return LinkRoleParent
	default:
		return role
	}
}
