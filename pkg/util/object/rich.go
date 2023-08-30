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
	"strings"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
)

type Rich struct {
	VersionedKey

	Uid types.UID

	Raw *unstructured.Unstructured `json:"-"`
}

// Returns a new Rich reference with all strings cloned again
// to ensure this object does not reference more data than it needs.
//
// Does not copy the Raw field.
func (ref Rich) Clone() Rich {
	return Rich{
		VersionedKey: ref.VersionedKey.Clone(),
		Uid:          types.UID(strings.Clone(string(ref.Uid))),
	}
}

func (ref Rich) String() string {
	return ref.Key.String()
}

func (ref Rich) AsFields(prefix string) logrus.Fields {
	fields := ref.Key.AsFields(prefix)
	fields[prefix+"Uid"] = ref.Uid
	return fields
}

func RichFromUnstructured(
	uns *unstructured.Unstructured,
	cluster string,
	gvr schema.GroupVersionResource,
) Rich {
	return Rich{
		VersionedKey: FromObject(uns, cluster, gvr),
		Uid:          uns.GetUID(),
		Raw:          uns,
	}
}

func RichFromAudit(object *auditv1.ObjectReference, cluster string) Rich {
	return Rich{
		VersionedKey: VersionedKey{
			Key: Key{
				Cluster:   cluster,
				Group:     object.APIGroup,
				Resource:  object.Resource,
				Namespace: object.Namespace,
				Name:      object.Name,
			},
			Version: object.APIVersion,
		},
		Uid: object.UID,
	}
}
