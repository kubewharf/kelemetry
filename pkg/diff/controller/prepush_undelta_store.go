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

package controller

import (
	"sync"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type prepushUndeltaStore struct {
	logger logrus.FieldLogger

	storeMutex sync.Mutex
	store      map[namespacedName]storageType

	objectFilter func(obj *unstructured.Unstructured) bool
	onAdd        func(newObj *unstructured.Unstructured)
	onUpdate     func(oldObj, newObj *unstructured.Unstructured)
	onDelete     func(oldObj *unstructured.Unstructured)
}

// type storageType = []byte
type storageType = *unstructured.Unstructured

func (store *prepushUndeltaStore) swap(key namespacedName, newValue storageType) (oldValue storageType) {
	store.storeMutex.Lock()
	defer store.storeMutex.Unlock()

	oldValue = store.store[key]
	if newValue != nil {
		store.store[key] = newValue
	} else {
		delete(store.store, key)
	}

	return oldValue
}

func (store *prepushUndeltaStore) set(obj interface{}, isDelete bool) {
	uns := obj.(*unstructured.Unstructured)
	if !store.objectFilter(uns) {
		return
	}

	key := getNamespacedKey(uns)

	var newValue *unstructured.Unstructured
	if !isDelete {
		newValue = uns
	}

	oldValue := store.swap(key, newValue)

	if oldValue != nil && newValue != nil {
		if store.onUpdate != nil {
			store.onUpdate(oldValue, newValue)
		}
	} else if oldValue == nil && newValue != nil {
		if store.onAdd != nil {
			store.onAdd(newValue)
		}
	} else if oldValue != nil && newValue == nil {
		if store.onDelete != nil {
			store.onDelete(oldValue)
		}
	} else {
		store.logger.WithField("key", key).Warn("unexpected set(nonexistent, true)")
	}
}

func (store *prepushUndeltaStore) Add(obj interface{}) error {
	store.set(obj, false)
	return nil
}

func (store *prepushUndeltaStore) Update(obj interface{}) error {
	store.set(obj, false)
	return nil
}

func (store *prepushUndeltaStore) Delete(obj interface{}) error {
	store.set(obj, true)
	return nil
}

func (store *prepushUndeltaStore) Replace(list []interface{}, resourceVersion string) error {
	keys := map[namespacedName]struct{}{}

	for _, item := range list {
		store.set(item, false)
		keys[getNamespacedKey(item.(*unstructured.Unstructured))] = struct{}{}
	}

	deletions := store.drainNotIn(keys)
	if store.onDelete != nil {
		for _, deletion := range deletions {
			store.onDelete(deletion)
		}
	}

	return nil
}

func (store *prepushUndeltaStore) drainNotIn(dontDelete map[namespacedName]struct{}) []storageType {
	store.storeMutex.Lock()
	defer store.storeMutex.Unlock()

	output := []storageType{}
	oldLength := len(store.store)

	for key := range store.store {
		if _, exists := dontDelete[key]; !exists {
			output = append(output, store.store[key])
			delete(store.store, key)
		}
	}

	if len(output)+len(dontDelete) != oldLength {
		store.logger.Warnf("some entries in dontDelete are not in store.store (%d + %d != %d)", len(output), len(dontDelete), oldLength)
	}

	return output
}

func (store *prepushUndeltaStore) Get(obj interface{}) (interface{}, bool, error) { panic("unused") }
func (store *prepushUndeltaStore) GetByKey(key string) (interface{}, bool, error) { panic("unused") }
func (store *prepushUndeltaStore) List() []interface{}                            { panic("unused") }
func (store *prepushUndeltaStore) ListKeys() []string                             { panic("unused") }
func (store *prepushUndeltaStore) Resync() error                                  { panic("unused") }
