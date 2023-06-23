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

package globalinformer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/manager"
	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
	syncutil "github.com/kubewharf/kelemetry/pkg/util/sync"
)

type Object interface {
	metav1.Object
	runtime.Object
}

type options struct {
	name string

	timeout time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.DurationVar(
		&options.timeout,
		fmt.Sprintf("global-informer-%s-timeout", options.name),
		time.Hour,
		"idle timeout for each cluster informer",
	)
}
func (options *options) EnableFlag() *bool { return nil }

type Comp[T Object, A Adapter[T, S], S StoreWrapper] struct {
	options options
	Clock   clock.Clock
	Logger  logrus.FieldLogger
	Clients k8s.Clients

	Adapter A `managerSkipFill:""`

	informerMap *InformerMap[T, A, S]
}

func (comp *Comp[T, A, S]) Options() manager.Options {
	comp.options.name = comp.Adapter.AdapterName()
	return &comp.options
}
func (comp *Comp[T, A, S]) Init() error                     { return nil }
func (comp *Comp[T, A, S]) Close(ctx context.Context) error { return nil }

func (comp *Comp[T, A, S]) Start(ctx context.Context) error {
	comp.informerMap = New[T, A, S](
		ctx, comp.Clock, comp.Logger,
		func(clusterName string) (*rest.Config, error) {
			client, err := comp.Clients.Cluster(clusterName)
			if err != nil {
				return nil, err
			}

			config := client.RestConfig()
			if config == nil {
				return nil, fmt.Errorf("cluster provider does not support new clients")
			}

			return config, nil
		},
		comp.options.timeout, comp.Adapter,
	)
	return nil
}

func (comp *Comp[T, A, S]) Cluster(clusterName string) (_s S, _ error) {
	entry, err := comp.informerMap.Cluster(clusterName)
	if err != nil {
		return _s, err
	}

	return entry.storeWrapper, nil
}

type ConfigProvider = func(clusterName string) (*rest.Config, error)

type InformerMap[T Object, A Adapter[T, S], S StoreWrapper] struct {
	ctx            context.Context //nolint:containedctx // this is a background base context inherited by lazily created async reflectors
	clock          clock.Clock
	logger         logrus.FieldLogger
	configProvider ConfigProvider

	timeout time.Duration
	adapter A

	clustersMu sync.Mutex
	clusters   map[string]*syncutil.Promise[*ClusterEntry[S]]
}

// Creates a new global informer map that lives until the channel is closed.
func New[T Object, A Adapter[T, S], S StoreWrapper](
	ctx context.Context,
	clock clock.Clock,
	logger logrus.FieldLogger,
	configProvider ConfigProvider,
	timeout time.Duration,
	adapter A,
) *InformerMap[T, A, S] {
	return &InformerMap[T, A, S]{
		ctx:            ctx,
		clock:          clock,
		logger:         logger,
		configProvider: configProvider,
		timeout:        timeout,
		adapter:        adapter,
		clusters:       map[string]*syncutil.Promise[*ClusterEntry[S]]{},
	}
}

func (informerMap *InformerMap[T, A, S]) cluster(
	name string,
	newPromise *syncutil.Promise[*ClusterEntry[S]],
) (_ *syncutil.Promise[*ClusterEntry[S]], isNew bool) {
	informerMap.clustersMu.Lock()
	defer informerMap.clustersMu.Unlock()

	if _, exists := informerMap.clusters[name]; !exists {
		informerMap.clusters[name] = newPromise
		isNew = true
	}

	return informerMap.clusters[name], isNew
}

func (informerMap *InformerMap[T, A, S]) Cluster(clusterName string) (*ClusterEntry[S], error) {
	maybeUnusedPromise, initer := syncutil.NewPromise(func() (*ClusterEntry[S], error) {
		config, err := informerMap.configProvider(clusterName)
		if err != nil {
			return nil, fmt.Errorf("cannot create cluster config: %w", err)
		}

		lw, err := informerMap.adapter.ListWatchForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("create ListerWatcher from config: %w", err)
		}

		ctx, cancelFunc := context.WithCancel(informerMap.ctx)

		store, informer := cache.NewInformer(lw, reflectutil.ZeroOf[T](), 0, cache.ResourceEventHandlerFuncs{})
		go func() {
			informer.Run(ctx.Done())
		}()

		storeWrapper := informerMap.adapter.NewStoreWrapper(clusterName, store)

		entry := &ClusterEntry[S]{
			storeWrapper: storeWrapper,
		}
		entry.touch(informerMap.clock)

		go informerMap.waitForClusterExpiry(clusterName, entry, cancelFunc)

		cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)
		return entry, nil
	})

	promise, isNew := informerMap.cluster(clusterName, maybeUnusedPromise)

	if isNew {
		_, err := initer()
		if err != nil {
			return nil, err
		}
	}

	entry, err := promise.Get()
	if err != nil {
		return nil, err
	}

	return entry, nil
}

func (informerMap *InformerMap[T, A, S]) waitForClusterExpiry(clusterName string, entry *ClusterEntry[S], cancelFunc context.CancelFunc) {
	defer shutdown.RecoverPanic(informerMap.logger)
	defer cancelFunc()
	defer func() {
		informerMap.clustersMu.Lock()
		defer informerMap.clustersMu.Unlock()
		delete(informerMap.clusters, clusterName)
	}()

	for {
		timeout := entry.lastTouch.Load().Add(informerMap.timeout)
		duration := time.Until(timeout)
		if duration < 0 {
			break
		}

		time.Sleep(duration)
	}
}

type StoreWrapper interface {
	IsStoreWrapper()
}

type ClusterEntry[S StoreWrapper] struct {
	lastTouch    atomic.Pointer[time.Time]
	storeWrapper S
}

func (entry *ClusterEntry[S]) touch(clock clock.Clock) {
	now := clock.Now()
	entry.lastTouch.Store(&now)
}

type Adapter[T Object, S StoreWrapper] interface {
	AdapterName() string

	ListWatchForConfig(config *rest.Config) (cache.ListerWatcher, error)

	NewStoreWrapper(cluster string, store cache.Store) S
}

type CastStoreWrapper[T Object] struct {
	store cache.Store
}

func NewCastStoreWrapper[T Object](store cache.Store) *CastStoreWrapper[T] {
	return &CastStoreWrapper[T]{store: store}
}

func (w *CastStoreWrapper[T]) IsStoreWrapper() {}

func (w *CastStoreWrapper[T]) Get(namespace, name string) (_t T, _ bool) {
	key := name
	if len(namespace) > 0 {
		key = namespace + "/" + name
	}

	// (&cache.cache{}).GetByKey never returns err
	item, exists, _ := w.store.GetByKey(key)
	if !exists {
		return _t, false
	}

	return item.(T), true
}

func (w *CastStoreWrapper[T]) List() []T {
	anyList := w.store.List()
	list := make([]T, len(anyList))
	for i, item := range anyList {
		list[i] = item.(T)
	}
	return list
}
