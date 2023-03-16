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
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ObjectKey struct {
	Namespace string
	Name      string
}

func ObjectKeyOf[V metav1.Object](object V) ObjectKey {
	return ObjectKey{
		Namespace: object.GetNamespace(),
		Name:      object.GetName(),
	}
}

type PrepushUndeltaStore[V metav1.Object] struct {
	logger       logrus.FieldLogger
	objectFilter func(obj V) bool

	OnAdd    func(newObj V)
	OnUpdate func(oldObj, newObj V)
	OnDelete func(oldObj V)

	data *SwapMap[ObjectKey, V]
}

func NewPrepushUndeltaStore[V metav1.Object](
	logger logrus.FieldLogger,
	objectFilter func(obj V) bool,
) *PrepushUndeltaStore[V] {
	return &PrepushUndeltaStore[V]{
		logger:       logger,
		objectFilter: objectFilter,
		data:         NewSwapMap[ObjectKey, V](0),
	}
}

func (store *PrepushUndeltaStore[V]) processSwapResult(key ObjectKey, swap SwapResult[V]) {
	switch swap.Kind {
	case SwapResultKindAdd:
		if store.OnAdd != nil {
			store.OnAdd(swap.NewValue)
		}
	case SwapResultKindReplace:
		if store.OnUpdate != nil {
			store.OnUpdate(swap.OldValue, swap.NewValue)
		}
	case SwapResultKindRemove:
		if store.OnDelete != nil {
			store.OnDelete(swap.OldValue)
		}
	case SwapResultKindNoop:
		store.logger.WithField("key", key).Warn("unexpected set(nonexistent, isDelete=true)")
	}
}

func (store *PrepushUndeltaStore[V]) set(objAny any, isDelete bool) {
	obj := objAny.(V)
	if !store.objectFilter(obj) {
		return
	}

	key := ObjectKeyOf(obj)

	var newValue V
	if !isDelete {
		newValue = obj
	}

	swap := store.data.Swap(key, newValue, !isDelete)
	store.processSwapResult(key, swap)
}

func (store *PrepushUndeltaStore[V]) Add(obj any) error {
	store.set(obj, false)
	return nil
}

func (store *PrepushUndeltaStore[V]) Update(obj any) error {
	store.set(obj, false)
	return nil
}

func (store *PrepushUndeltaStore[V]) Delete(obj any) error {
	store.set(obj, true)
	return nil
}

func (store *PrepushUndeltaStore[V]) Replace(list []any, resourceVersion string) error {
	kv := make(map[ObjectKey]V, len(list))
	for _, obj := range list {
		value := obj.(V)
		kv[ObjectKeyOf(value)] = value
	}

	for key, swap := range SwapMapReplace(store.data, kv, identity[V]) {
		store.processSwapResult(key, swap)
	}

	return nil
}

func (store *PrepushUndeltaStore[V]) Get(obj any) (any, bool, error)         { panic("unused") }
func (store *PrepushUndeltaStore[V]) GetByKey(key string) (any, bool, error) { panic("unused") }
func (store *PrepushUndeltaStore[V]) List() []any                            { panic("unused") }
func (store *PrepushUndeltaStore[V]) ListKeys() []string                     { panic("unused") }
func (store *PrepushUndeltaStore[V]) Resync() error                          { panic("unused") }

func identity[T any](value T) T { return value }
