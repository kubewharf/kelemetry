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

package informerutil

import (
	"sync"
)

// SwapMap is a thread-safe map that provides atomic swapping results,
// optimized to be used as an informer backend with no read operations.
type SwapMap[K comparable, V any] struct {
	mutex sync.Mutex // not RWMutex, because there is no read-only access.
	data  map[K]V
}

func NewSwapMap[K comparable, V any](capacity int) *SwapMap[K, V] {
	return &SwapMap[K, V]{
		data: make(map[K]V, capacity),
	}
}

func (m *SwapMap[K, V]) Swap(key K, newValue V, hasNewValue bool) SwapResult[V] {
	return m.SwapIf(key, newValue, hasNewValue, func(_, _ V) bool { return true })
}

// SwapIf swaps newValue with m[key] unless both values exist and condition(m[key], newValue) is false.
func (m *SwapMap[K, V]) SwapIf(
	key K,
	newValue V,
	hasNewValue bool,
	shouldSwap func(oldValue, newValue V) bool,
) SwapResult[V] {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return m.lockedSwapIf(key, newValue, hasNewValue, shouldSwap)
}

func (m *SwapMap[K, V]) lockedSwapIf(
	key K,
	newValue V,
	hasNewValue bool,
	condition func(oldValue, newValue V) bool,
) SwapResult[V] {
	oldValue, hasOldValue := m.data[key]
	if hasOldValue && hasNewValue {
		if condition(oldValue, newValue) {
			m.data[key] = newValue
			return SwapResult[V]{
				Kind:     SwapResultKindReplace,
				OldValue: oldValue,
				NewValue: newValue,
			}
		} else {
			return SwapResult[V]{
				Kind: SwapResultKindNoop,
			}
		}
	} else if hasOldValue {
		delete(m.data, key)
		return SwapResult[V]{
			Kind:     SwapResultKindRemove,
			OldValue: oldValue,
		}
	} else if hasNewValue {
		m.data[key] = newValue
		return SwapResult[V]{
			Kind:     SwapResultKindAdd,
			NewValue: newValue,
		}
	} else {
		return SwapResult[V]{
			Kind: SwapResultKindNoop,
		}
	}
}

func SwapMapReplace[K comparable, V any, U any](m *SwapMap[K, V], values map[K]U, transformValue func(U) V) map[K]SwapResult[V] {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	output := map[K]SwapResult[V]{}

	for key, value := range m.data {
		if _, retain := values[key]; !retain {
			output[key] = SwapResult[V]{
				Kind:     SwapResultKindRemove,
				OldValue: value,
			}
			delete(m.data, key)
		}
	}

	for key, value := range values {
		output[key] = m.lockedSwapIf(key, transformValue(value), true, func(_, _ V) bool { return true })
	}

	return output
}

type SwapResult[V any] struct {
	Kind SwapResultKind
	// OldValue is the original value if Kind is Remove or Replace
	OldValue V
	// NewValue is the new value if Kind is Add or Replace
	NewValue V
}

type SwapResultKind uint8

const (
	SwapResultKindNoop SwapResultKind = iota
	SwapResultKindAdd
	SwapResultKindRemove
	SwapResultKindReplace
)
