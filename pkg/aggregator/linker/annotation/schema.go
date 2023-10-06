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

package annotationlinker

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

const LinkAnnotation = "kelemetry.kubewharf.io/parent-link"

type ParentLink struct {
	Cluster string `json:"cluster,omitempty"`

	metav1.GroupVersionResource

	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`

	Uid types.UID `json:"uid"`
}

func (ln ParentLink) ToRich() utilobject.Rich {
	return utilobject.Rich{
		VersionedKey: utilobject.VersionedKey{
			Key: utilobject.Key{
				Cluster:   ln.Cluster,
				Group:     ln.Group,
				Resource:  ln.Resource,
				Namespace: ln.Namespace,
				Name:      ln.Name,
			},
			Version: ln.Version,
		},
		Uid: ln.Uid,
	}
}
