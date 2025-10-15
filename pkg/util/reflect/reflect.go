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

package reflectutil

import (
	"reflect"
	"unique"
)

func TypeOf[T any]() reflect.Type {
	array := [0]T{}
	arrayTy := reflect.TypeOf(array)
	return arrayTy.Elem()
}

func ZeroOf[T any]() (_ T) { return }

func Identity[T any](t T) T { return t }

func InternString(s string) string {
	return unique.Make(s).Value()
}
