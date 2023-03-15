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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/kelemetry/pkg/util/channel"
)

// DecayingInformer is an informer that only supports delta handling.
// It still handles Replace logic (i.e. relisting) properly,
// but the reference to the object (other than the metadata) is released once the handler is called.
type DecayingInformer[V metav1.Object] struct {
	data *SwapMap[ObjectKey, *Decayed]

	addCh     *channel.UnboundedQueue[V]
	replaceCh *channel.UnboundedQueue[V]
	removeCh  *channel.UnboundedQueue[*Decayed]

	closeFunc func()
}

func NewDecayingInformer[V metav1.Object]() *DecayingInformer[V] {
	return &DecayingInformer[V]{
		data:      NewSwapMap[ObjectKey, *Decayed](0),
		closeFunc: func() {},
	}
}

func (di *DecayingInformer[V]) SetAddCh() <-chan V {
	ch := channel.NewUnboundedQueue[V](16)
	di.addCh = ch

	closeFunc := di.closeFunc
	di.closeFunc = func() {
		closeFunc()
		ch.Close()
	}

	return ch.Receiver()
}

func (di *DecayingInformer[V]) SetReplaceCh() <-chan V {
	ch := channel.NewUnboundedQueue[V](16)
	di.replaceCh = ch

	closeFunc := di.closeFunc
	di.closeFunc = func() {
		closeFunc()
		ch.Close()
	}

	return ch.Receiver()
}

func (di *DecayingInformer[V]) SetRemoveCh() <-chan *Decayed {
	ch := channel.NewUnboundedQueue[*Decayed](16)
	di.removeCh = ch

	closeFunc := di.closeFunc
	di.closeFunc = func() {
		closeFunc()
		ch.Close()
	}

	return ch.Receiver()
}

func (di *DecayingInformer[V]) set(objAny any, isDelete bool) {
	obj := objAny.(V)
	d := DecayedOf(obj)

	key := ObjectKeyOf(obj)
	swap := di.data.SwapIf(key, d, !isDelete, func(oldValue, newValue *Decayed) bool {
		return oldValue.resourceVersion != newValue.resourceVersion
	})
	di.processSwapResult(key, swap, obj)
}

func (di *DecayingInformer[V]) Add(obj any) error {
	di.set(obj, false)
	return nil
}

func (di *DecayingInformer[V]) Update(obj any) error {
	di.set(obj, false)
	return nil
}

func (di *DecayingInformer[V]) Delete(obj any) error {
	di.set(obj, true)
	return nil
}

func (di *DecayingInformer[V]) Replace(list []any, resourceVersion string) error {
	kv := make(map[ObjectKey]V, len(list))
	for _, obj := range list {
		value := obj.(V)
		kv[ObjectKeyOf(value)] = value
	}

	for key, swap := range SwapMapReplace(di.data, kv, DecayedOf[V]) {
		di.processSwapResult(key, swap, kv[key])
	}

	return nil
}

func (di *DecayingInformer[V]) processSwapResult(key ObjectKey, swap SwapResult[*Decayed], raw V) {
	switch swap.Kind {
	case SwapResultKindAdd:
		if di.addCh != nil {
			di.addCh.Send(raw)
		}
	case SwapResultKindReplace:
		if di.replaceCh != nil {
			di.replaceCh.Send(raw)
		}
	case SwapResultKindRemove:
		if di.removeCh != nil {
			di.removeCh.Send(swap.OldValue)
		}
	case SwapResultKindNoop:
	}
}

func (di *DecayingInformer[V]) Get(obj any) (any, bool, error)         { panic("unused") }
func (di *DecayingInformer[V]) GetByKey(key string) (any, bool, error) { panic("unused") }
func (di *DecayingInformer[V]) List() []any                            { panic("unused") }
func (di *DecayingInformer[V]) ListKeys() []string                     { panic("unused") }
func (di *DecayingInformer[V]) Resync() error                          { panic("unused") }

type Decayed struct {
	namespace       string
	name            string
	resourceVersion string
}

func DecayedOf[V metav1.Object](obj V) *Decayed {
	return &Decayed{
		namespace:       strings.Clone(obj.GetNamespace()),
		name:            strings.Clone(obj.GetName()),
		resourceVersion: strings.Clone(obj.GetResourceVersion()),
	}
}
