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

	"k8s.io/apimachinery/pkg/util/sets"

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

type stringPredicate = func(string) bool

type StringFilter struct {
	fn stringPredicate
}

type fields struct {
	Exact    Optional[string]   `json:"exact"`
	OneOf    Optional[[]string] `json:"oneOf"`
	CaseFold Optional[string]   `json:"caseInsensitive"`
	Regex    Optional[string]   `json:"regex"`
	Then     Optional[bool]     `json:"then"`
}

func (value fields) getBasePredicate() (stringPredicate, error) {
	isSet := 0
	for _, b := range []bool{value.Exact.IsSet, value.OneOf.IsSet, value.CaseFold.IsSet, value.Regex.IsSet} {
		if b {
			isSet += 1
		}
	}

	if isSet > 1 {
		return nil, fmt.Errorf("string filter must set exactly one of `exact`, `oneOf`, `caseInsensitive` or `regex`")
	}

	if value.Exact.IsSet {
		return func(s string) bool { return s == value.Exact.Value }, nil
	} else if value.OneOf.IsSet {
		options := sets.New[string](value.OneOf.Value...)
		return options.Has, nil
	} else if value.CaseFold.IsSet {
		return func(s string) bool { return strings.EqualFold(value.CaseFold.Value, value.CaseFold.Value) }, nil
	} else if value.Regex.IsSet {
		regex, err := regexp.Compile(value.Regex.Value)
		if err != nil {
			return nil, fmt.Errorf("pattern contains invalid regex: %w", err)
		}

		return regex.MatchString, nil
	} else {
		return func(string) bool { return true }, nil // caller will change value to `then`
	}
}

func (f *StringFilter) UnmarshalJSON(buf []byte) error {
	var pattern string
	if err := json.Unmarshal(buf, &pattern); err == nil {
		f.fn = func(s string) bool { return s == pattern }
		return nil
	}

	var value fields
	if err := json.Unmarshal(buf, &value); err != nil {
		return err
	}

	predicate, err := value.getBasePredicate()
	if err != nil {
		return err
	}

	then := value.Then.GetOr(true)
	f.fn = func(s string) bool {
		base := predicate(s)
		if base {
			return then
		} else {
			return !then
		}
	}

	return nil
}

func (f *StringFilter) Matches(subject string) bool {
	if f.fn == nil {
		// string filter not set, take default then = true
		return true
	}

	return f.fn(subject)
}
