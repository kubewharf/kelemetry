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

package util

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
)

type ObjectRef struct {
	Cluster string

	schema.GroupVersionResource

	Namespace string
	Name      string

	Uid types.UID

	Raw *unstructured.Unstructured `json:"-"`
}

// Returns a new ObjectRef with all strings cloned again
// to ensure this object does not reference more data than it needs.
//
// Does not copy the Raw field.
func (ref ObjectRef) Clone() ObjectRef {
	return ObjectRef{
		Cluster: strings.Clone(ref.Cluster),
		GroupVersionResource: schema.GroupVersionResource{
			Group:    strings.Clone(ref.Group),
			Version:  strings.Clone(ref.Version),
			Resource: strings.Clone(ref.Resource),
		},
		Namespace: strings.Clone(ref.Namespace),
		Name:      strings.Clone(ref.Name),
		Uid:       types.UID(strings.Clone(string(ref.Uid))),
	}
}

func (ref ObjectRef) String() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", ref.Cluster, ref.Group, ref.Resource, ref.Namespace, ref.Name)
}

func (ref ObjectRef) AsFields(prefix string) logrus.Fields {
	return logrus.Fields{
		prefix + "Cluster":   ref.Cluster,
		prefix + "Group":     ref.Group,
		prefix + "Resource":  ref.Resource,
		prefix + "Namespace": ref.Namespace,
		prefix + "Name":      ref.Name,
		prefix + "Uid":       ref.Uid,
	}
}

func ObjectRefFromUnstructured(
	uns *unstructured.Unstructured,
	cluster string,
	gvr schema.GroupVersionResource,
) ObjectRef {
	return ObjectRef{
		Cluster:              cluster,
		GroupVersionResource: gvr,
		Namespace:            uns.GetNamespace(),
		Name:                 uns.GetName(),
		Uid:                  uns.GetUID(),
		Raw:                  uns,
	}
}

func ObjectRefFromAudit(object *auditv1.ObjectReference, cluster string, uid types.UID) ObjectRef {
	return ObjectRef{
		Cluster: cluster,
		GroupVersionResource: schema.GroupVersionResource{
			Group:    object.APIGroup,
			Version:  object.APIVersion,
			Resource: object.Resource,
		},
		Namespace: object.Namespace,
		Name:      object.Name,
		Uid:       uid,
	}
}
