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

package diffcmp

import (
	"fmt"
	"sort"
	"strings"
)

type DiffList struct {
	Diffs []Diff `json:"diffs"`
}

type Diff struct {
	JsonPath string `json:"jsonPath"`
	Old      any    `json:"old,omitempty"`
	New      any    `json:"new,omitempty"`
}

func pushDiff(diffs *[]Diff, jsonPath []string, oldObj, newObj any) {
	*diffs = append(*diffs, Diff{
		JsonPath: strings.Join(jsonPath, "."),
		Old:      oldObj,
		New:      newObj,
	})
}

func Compare(oldObj, newObj any) DiffList {
	diffs := []Diff{}
	compare(&diffs, []string{}, oldObj, newObj)
	return DiffList{Diffs: diffs}
}

func compare(diffs *[]Diff, jsonPath []string, oldObj, newObj any) {
	if conclusive, equal := compareMaybePrimitive(oldObj, newObj); conclusive {
		if !equal {
			pushDiff(diffs, jsonPath, oldObj, newObj)
		}
		return
	}

	if oldMap, ok := oldObj.(map[string]any); ok {
		if newMap, ok := newObj.(map[string]any); ok {
			compareMaps(diffs, jsonPath, oldMap, newMap)
			return
		}
	}

	if oldSlice, ok := oldObj.([]any); ok {
		if newSlice, ok := newObj.([]any); ok {
			compareSlices(diffs, jsonPath, oldSlice, newSlice)
			return
		}
	}

	pushDiff(diffs, jsonPath, oldObj, newObj)
}

func compareMaybePrimitive(oldObj, newObj any) (conclusive, equal bool) {
	if oldObj == nil || newObj == nil {
		return true, oldObj == nil && newObj == nil
	}

	if oldValue, oldOk := oldObj.(string); oldOk {
		if newValue, newOk := newObj.(string); newOk {
			return true, oldValue == newValue
		}
	}

	if oldValue, oldOk := oldObj.(bool); oldOk {
		if newValue, newOk := newObj.(bool); newOk {
			return true, oldValue == newValue
		}
	}

	if oldValue, oldOk := oldObj.(int64); oldOk {
		if newValue, newOk := newObj.(int64); newOk {
			return true, oldValue == newValue
		}
	}

	if oldValue, oldOk := oldObj.(float64); oldOk {
		if newValue, newOk := newObj.(float64); newOk {
			return true, oldValue == newValue
		}
	}

	return false, false
}

func compareMaps(
	diffs *[]Diff,
	jsonPath []string,
	oldObj, newObj map[string]any,
) {
	keysMap := map[string]struct{}{}
	collectKeys(keysMap, oldObj)
	collectKeys(keysMap, newObj)
	keys := getMapKeys(keysMap)
	sort.Strings(keys)

	for _, key := range keys {
		keyPath := append(jsonPath, key)

		oldValue, oldExist := oldObj[key]
		newValue, newExist := newObj[key]

		if oldExist && !newExist {
			pushDiff(diffs, keyPath, oldValue, nil)
		} else if newExist && !oldExist {
			pushDiff(diffs, keyPath, nil, newValue)
			*diffs = append(*diffs, Diff{
				JsonPath: strings.Join(keyPath, "."),
				Old:      nil,
				New:      newValue,
			})
		} else {
			// both exist
			compare(diffs, keyPath, oldValue, newValue)
		}
	}
}

func collectKeys(dest map[string]struct{}, src map[string]any) {
	for key := range src {
		dest[key] = struct{}{}
	}
}

func getMapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func compareSlices(
	diffs *[]Diff,
	jsonPath []string,
	oldSlice, newSlice []any,
) {
	for i := 0; i < len(oldSlice) || i < len(newSlice); i++ {
		keyPath := append(jsonPath, fmt.Sprintf("[%d]", i))

		oldValue := any(nil)
		if i < len(oldSlice) {
			oldValue = oldSlice[i]
		}

		newValue := any(nil)
		if i < len(newSlice) {
			newValue = newSlice[i]
		}

		compare(diffs, keyPath, oldValue, newValue)
	}
}
