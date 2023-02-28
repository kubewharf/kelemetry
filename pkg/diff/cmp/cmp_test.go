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

package diffcmp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	diffcmp "github.com/kubewharf/kelemetry/pkg/diff/cmp"
)

type testCase struct {
	name     string
	old, new interface{}
	diff     []diffcmp.Diff
}

func TestCompare(t *testing.T) {
	tests := []testCase{
		{name: "nil equal", old: nil, new: nil, diff: []diffcmp.Diff{}},
		{name: "nil differ from primitive", old: nil, new: "b", diff: []diffcmp.Diff{
			{JsonPath: "", Old: nil, New: "b"},
		}},
		{name: "primitive differ from nil", old: "a", new: nil, diff: []diffcmp.Diff{
			{JsonPath: "", Old: "a", New: nil},
		}},

		{name: "string equal", old: "a", new: "a", diff: []diffcmp.Diff{}},
		{name: "string change", old: "a", new: "b", diff: []diffcmp.Diff{
			{JsonPath: "", Old: "a", New: "b"},
		}},

		{name: "bool equal", old: true, new: true, diff: []diffcmp.Diff{}},
		{name: "bool change", old: true, new: false, diff: []diffcmp.Diff{
			{JsonPath: "", Old: true, New: false},
		}},

		{name: "int equal", old: int64(1), new: int64(1), diff: []diffcmp.Diff{}},
		{name: "int change", old: int64(1), new: int64(2), diff: []diffcmp.Diff{
			{JsonPath: "", Old: int64(1), New: int64(2)},
		}},

		{name: "float equal", old: float64(1.0), new: float64(1.0), diff: []diffcmp.Diff{}},
		{name: "float change", old: 1.0, new: 2.0, diff: []diffcmp.Diff{
			{JsonPath: "", Old: float64(1.0), New: float64(2.0)},
		}},

		{name: "empty map equal", old: map[string]interface{}{}, new: map[string]interface{}{}, diff: []diffcmp.Diff{}},
		{name: "empty slice equal", old: []interface{}{}, new: []interface{}{}, diff: []diffcmp.Diff{}},
		{name: "map slice differ", old: map[string]interface{}{}, new: []interface{}{}, diff: []diffcmp.Diff{
			{JsonPath: "", Old: map[string]interface{}{}, New: []interface{}{}},
		}},

		{
			name: "nested string map equal",
			old:  map[string]interface{}{"a": map[string]interface{}{"b": "c"}},
			new:  map[string]interface{}{"a": map[string]interface{}{"b": "c"}},
			diff: []diffcmp.Diff{},
		},
		{
			name: "map slice nested differ",
			old:  map[string]interface{}{"a": []interface{}{"b"}},
			new:  map[string]interface{}{"a": []interface{}{true}},
			diff: []diffcmp.Diff{{JsonPath: "a.[0]", Old: "b", New: true}},
		},
		{
			name: "slice map nested differ",
			old:  map[string]interface{}{"a": []interface{}{"b"}},
			new:  map[string]interface{}{"a": []interface{}{true}},
			diff: []diffcmp.Diff{{JsonPath: "a.[0]", Old: "b", New: true}},
		},

		{name: "slice order differ", old: []interface{}{"a", "b"}, new: []interface{}{"b", "a"}, diff: []diffcmp.Diff{
			{JsonPath: "[0]", Old: "a", New: "b"},
			{JsonPath: "[1]", Old: "b", New: "a"},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			diffList := diffcmp.Compare(test.old, test.new)

			// unordered equality test
			assert.Equal(len(diffList.Diffs), len(test.diff))
			for _, diff := range diffList.Diffs {
				assert.Contains(test.diff, diff)
			}
		})
	}
}
