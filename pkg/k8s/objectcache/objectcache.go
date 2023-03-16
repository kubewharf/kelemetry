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

package objectcache

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/coocood/freecache"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/clock"

	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("kube-object-cache", manager.Ptr[ObjectCache](&objectCache{}))
}

type objectCacheOptions struct {
	cacheSize    int
	fetchTimeout time.Duration
	storeTtl     time.Duration
}

func (options *objectCacheOptions) Setup(fs *pflag.FlagSet) {
	fs.IntVar(&options.cacheSize, "object-cache-size", 1<<30, "maximum number of bytes to cache API objects")
	fs.DurationVar(&options.fetchTimeout, "object-cache-fetch-timeout", time.Second*5, "duration that an object is locked for fetching")
	fs.DurationVar(&options.storeTtl, "object-cache-store-ttl", time.Second*5, "duration that an object stays in the cache after fetch")
}

func (options *objectCacheOptions) EnableFlag() *bool { return nil }

type ObjectCache interface {
	manager.Component

	// Get retrieves an object from the cache, or requests it from the apiserver if it is not in the active cache.
	Get(ctx context.Context, object util.ObjectRef) (*unstructured.Unstructured, error)
}

type objectCache struct {
	options   objectCacheOptions
	Logger    logrus.FieldLogger
	Clock     clock.Clock
	Clients   k8s.Clients
	Metrics   metrics.Client
	DiffCache diffcache.Cache

	CacheRequestMetric *metrics.Metric[*cacheRequestMetric]

	cache *freecache.Cache
}

type cacheSizeMetric struct{}

func (*cacheSizeMetric) MetricName() string { return "object_cache_size" }

type cacheEvictionMetric struct{}

func (*cacheEvictionMetric) MetricName() string { return "object_cache_eviction" }

type cacheRequestMetric struct {
	Hit   bool
	Error string
}

func (*cacheRequestMetric) MetricName() string { return "object_cache_request" }

func (oc *objectCache) Options() manager.Options { return &oc.options }

func (oc *objectCache) Init(ctx context.Context) error {
	oc.cache = freecache.NewCache(oc.options.cacheSize)
	metrics.NewMonitor(oc.Metrics, &cacheSizeMetric{}, func() int64 { return oc.cache.EntryCount() })
	metrics.NewMonitor(oc.Metrics, &cacheEvictionMetric{}, func() int64 { return oc.cache.EvacuateCount() })
	return nil
}

func (oc *objectCache) Start(stopCh <-chan struct{}) error { return nil }

func (oc *objectCache) Close() error { return nil }

func (oc *objectCache) Get(ctx context.Context, object util.ObjectRef) (*unstructured.Unstructured, error) {
	metric := &cacheRequestMetric{Error: "Unknown"}
	defer oc.CacheRequestMetric.DeferCount(oc.Clock.Now(), metric)

	key := objectKey(object)
	randomId := [5]byte{0, 0, 0, 0, 0}
	_, _ = rand.Read(randomId[1:])

	for {
		cached, _ := oc.cache.GetOrSet(key, randomId[:], int(oc.options.fetchTimeout.Seconds()))
		if cached != nil {
			// cache hit
			metric.Hit = true

			if cached[0] == 0 {
				// pending; JSON never starts with a NUL byte
				oc.Clock.Sleep(time.Millisecond * 100) // constant backoff, up to 50 times by default
				continue
			}

			uns := &unstructured.Unstructured{}
			err := uns.UnmarshalJSON(cached)
			if err != nil {
				metric.Error = "Unmarshal"
				return nil, fmt.Errorf("cached invalid data: %w", err)
			}

			metric.Error = "nil"
			return uns, nil
		}

		// cache miss and reserved
		break
	}

	metric.Hit = false

	clusterClient, err := oc.Clients.Cluster(object.Cluster)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize clients for cluster %q: %w", object.Cluster, err)
	}
	nsClient := clusterClient.DynamicClient().Resource(object.GroupVersionResource)
	var client dynamic.ResourceInterface = nsClient
	if object.Namespace != "" {
		client = nsClient.Namespace(object.Namespace)
	}

	raw, err := client.Get(ctx, object.Name, metav1.GetOptions{
		ResourceVersion: "0",
	})
	if err != nil && !k8serrors.IsNotFound(err) {
		metric.Error = string(k8serrors.ReasonForError(err))
		return nil, err
	}

	if err == nil {
		json, err := raw.MarshalJSON()
		if err != nil {
			metric.Error = "Marshal"
			return nil, fmt.Errorf("server responds with non-marshalable data")
		}

		err = oc.cache.Set(key, json, int(oc.options.storeTtl.Seconds()))
		if err != nil {
			// the object is too large, so just don't cache it
			metric.Error = "ValueTooLarge"
		} else {
			metric.Error = "Penetrated"
		}

		return raw, nil
	}
	// else, not found

	metric.Error = "DeletionSnapshot"

	snapshot, err := oc.DiffCache.FetchSnapshot(ctx, object, diffcache.SnapshotNameDeletion)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot fallback to snapshot: %w", err), "SnapshotFetch")
	}

	if snapshot != nil {
		uns := &unstructured.Unstructured{}
		if err := uns.UnmarshalJSON(snapshot.Value); err != nil {
			return nil, metrics.LabelError(fmt.Errorf("decode snapshot err: %w", err), "SnapshotDecode")
		}

		return uns, nil
	}

	// all methods failed

	return nil, nil
}

func objectKey(object util.ObjectRef) []byte {
	return []byte(fmt.Sprintf("%s/%s/%s/%s", object.Group, object.Resource, object.Namespace, object.Name))
}
