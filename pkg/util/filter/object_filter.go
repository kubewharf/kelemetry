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

package filter

import (
	"github.com/dlclark/regexp2"

	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

type ObjectFilters struct {
	Cluster   Regex `json:"cluster"`
	Group     Regex `json:"group"`
	Version   Regex `json:"version"`
	Resource  Regex `json:"resource"`
	Namespace Regex `json:"namespace"`
	Name      Regex `json:"name"`
}

type Regex struct {
	// golang regex not support (?!..), use third party regular engine for go
	Pattern *regexp2.Regexp
}

func (regex *Regex) UnmarshalText(text []byte) (err error) {
	regex.Pattern, err = regexp2.Compile(string(text), 0)
	return err
}

func (regex *Regex) MatchString(s string) bool {
	match, _ := regex.Pattern.MatchString(s)
	return match
}

func (f *ObjectFilters) Check(object utilobject.VersionedKey) bool {
	if f.Cluster.Pattern != nil && !f.Cluster.MatchString(object.Cluster) {
		return false
	}

	if f.Group.Pattern != nil && !f.Group.MatchString(object.Group) {
		return false
	}

	if f.Version.Pattern != nil && !f.Version.MatchString(object.Version) {
		return false
	}

	if f.Resource.Pattern != nil && !f.Resource.MatchString(object.Resource) {
		return false
	}

	if f.Namespace.Pattern != nil && !f.Namespace.MatchString(object.Namespace) {
		return false
	}

	if f.Name.Pattern != nil && !f.Name.MatchString(object.Name) {
		return false
	}

	return true
}

type TagFilters map[string]Regex

func (f TagFilters) Check(tags map[string]string) bool {
	for key, filter := range f {
		val := tags[key]
		if filter.Pattern != nil && !filter.MatchString(val) {
			return false
		}
	}

	return true
}
