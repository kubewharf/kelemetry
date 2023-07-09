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

package syncutil

import "errors"

var ErrPanic = errors.New("loader panicked")

// Promise is a shared value that can be accessed after the initializer is ready.
type Promise[T any] struct {
	ch    <-chan struct{}
	value T
	err   error
}

// NewPromise creates a pending promise.
// The loader for the promise value only gets run when `initer` is called.
//
// The typical usage would be like this:
//
// ```
// promise, initer := NewPromise(...)
// sharedMap[key] = promise
// initer() // or `go initer()`
// ```
func NewPromise[T any](loader func() (T, error)) (promise *Promise[T], initer func() (T, error)) {
	ch := make(chan struct{})
	promise = &Promise[T]{
		ch:  ch,
		err: ErrPanic, // to be overridden before normal channel close
	}

	initer = func() (T, error) {
		defer close(ch)

		promise.value, promise.err = loader()
		return promise.value, promise.err
	}

	return promise, initer
}

// TryGet returns the promise value/error and `true`, or return `false` if the promise is not ready yet.
func (promise *Promise[T]) TryGet() (_t T, _err error, _ready bool) {
	select {
	case <-promise.ch:
		return promise.value, promise.err, true
	default:
		return _t, _err, false
	}
}

// Get waits for the promise to become ready and returns the value/error.
func (promise *Promise[T]) Get() (T, error) {
	<-promise.ch
	return promise.value, promise.err
}
