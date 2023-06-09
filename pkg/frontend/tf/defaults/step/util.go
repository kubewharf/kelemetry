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

package tfstep

import (
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

type StringFilter struct {
	OneOf      []string `json:"oneOf"`
	Negate     bool     `json:"negate"`
	IgnoreCase bool     `json:"ignoreCase"`
}

// Test whether a string matches the filter.
func (f *StringFilter) Test(s string) bool {
	for _, choice := range f.OneOf {
		if (f.IgnoreCase && strings.EqualFold(s, choice)) || s == choice {
			return !f.Negate
		}
	}

	return f.Negate
}

type JsonStringSet struct {
	set sets.Set[string]
}

func (set *JsonStringSet) UnmarshalJSON(buf []byte) error {
	strings := []string{}
	if err := json.Unmarshal(buf, &strings); err != nil {
		return err
	}
	set.set = sets.New[string](strings...)
	return nil
}
