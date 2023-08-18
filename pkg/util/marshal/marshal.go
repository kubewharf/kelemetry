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

// Utilities for unmarshalling in config files
package utilmarshal

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

type Optional[T any] struct {
	IsSet bool
	Value T
}

func (opt Optional[T]) GetOr(defaultValue T) T {
	if opt.IsSet {
		return opt.Value
	} else {
		return defaultValue
	}
}

func IsSetTo[T comparable](opt Optional[T], t T) bool {
	return opt.IsSet && opt.Value == t
}

func (v *Optional[T]) UnmarshalJSON(buf []byte) error {
	if err := json.Unmarshal(buf, &v.Value); err != nil {
		return err
	}

	v.IsSet = true
	return nil
}

type ObjectFilter struct {
	Cluster   Optional[StringFilter] `json:"cluster"`
	Group     Optional[StringFilter] `json:"group"`
	Resource  Optional[StringFilter] `json:"resource"`
	Namespace Optional[StringFilter] `json:"namespace"`
	Name      Optional[StringFilter] `json:"name"`
}

func (filter *ObjectFilter) Matches(key utilobject.Key) bool {
	if filter.Cluster.IsSet {
		if !filter.Cluster.Value.Matches(key.Cluster) {
			return false
		}
	}

	if filter.Group.IsSet {
		if !filter.Group.Value.Matches(key.Group) {
			return false
		}
	}

	if filter.Resource.IsSet {
		if !filter.Resource.Value.Matches(key.Resource) {
			return false
		}
	}

	if filter.Namespace.IsSet {
		if !filter.Namespace.Value.Matches(key.Namespace) {
			return false
		}
	}

	if filter.Name.IsSet {
		if !filter.Name.Value.Matches(key.Name) {
			return false
		}
	}

	return true
}

type StringFilter struct {
	fn func(s string) bool
}

func (f *StringFilter) UnmarshalJSON(buf []byte) error {
	var pattern string
	if err := json.Unmarshal(buf, &pattern); err == nil {
		f.fn = func(s string) bool { return s == pattern }
		return nil
	}

	var value struct {
		Exact    Optional[string] `json:"exact"`
		CaseFold Optional[string] `json:"caseInsensitive"`
		Regex    Optional[string] `json:"regex"`
	}

	if err := json.Unmarshal(buf, &value); err != nil {
		return err
	}

	if value.Exact.IsSet {
		f.fn = func(s string) bool { return s == value.Exact.Value }
	} else if value.CaseFold.IsSet {
		f.fn = func(s string) bool { return strings.EqualFold(value.CaseFold.Value, value.CaseFold.Value) }
	} else if value.Regex.IsSet {
		regex, err := regexp.Compile(value.Regex.Value)
		if err != nil {
			return fmt.Errorf("pattern contains invalid regex: %w", err)
		}

		f.fn = regex.MatchString
	} else {
		return fmt.Errorf("no filter selected")
	}

	return nil
}

func (f *StringFilter) Matches(subject string) bool {
	return f.fn(subject)
}
