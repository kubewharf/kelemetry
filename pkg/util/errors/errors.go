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

package errors

import (
	goerrors "errors"
)

var (
	New    = goerrors.New
	Unwrap = goerrors.Unwrap
	Is     = goerrors.Is
	As     = goerrors.As
)

type labeled struct {
	unwrap error
	key    string
	value  any
}

func Label(err error, key string, value any) error {
	return labeled{unwrap: err, key: key, value: value}
}

func (err labeled) Error() string {
	return err.unwrap.Error()
}

func (err labeled) Unwrap() error {
	return err.unwrap
}

func GetNearestLabel(err error, key string) (any, bool) {
	if err, ok := err.(labeled); ok && err.key == key {
		return err.value, true
	}

	if inner := goerrors.Unwrap(err); inner != nil {
		return GetNearestLabel(inner, key)
	}

	return "", false
}

func GetDeepestLabel(err error, key string) (any, bool) {
	if inner := goerrors.Unwrap(err); inner != nil {
		if value, ok := GetDeepestLabel(inner, key); ok {
			return value, true
		}
	}

	if err, ok := err.(labeled); ok && err.key == key {
		return err.value, true
	}

	return "", false
}

// GetLabels returns labels from the nearest to the deepest.
func GetLabels(err error, key string) []any {
	var out []any

	for err != nil {
		if err, ok := err.(labeled); ok {
			out = append(out, err.value)
		}

		err = Unwrap(err)
	}

	return out
}
