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
	manager.Global.Provide("kube-object-cache", manager.Ptr(&ObjectCache{}))
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

type ObjectCache struct {
	options   objectCacheOptions
	Logger    logrus.FieldLogger
	Clock     clock.Clock
	Clients   k8s.Clients
	Metrics   metrics.Client
	DiffCache diffcache.Cache

	CacheRequestMetric *metrics.Metric[*CacheRequestMetric]

	cache *freecache.Cache
}

type cacheSizeMetric struct{}

func (*cacheSizeMetric) MetricName() string { return "object_cache_size" }

type cacheEvictionMetric struct{}

func (*cacheEvictionMetric) MetricName() string { return "object_cache_eviction" }

type CacheRequestMetric struct {
	Cluster string
	Hit     bool
	Error   string
}

func (*CacheRequestMetric) MetricName() string { return "object_cache_request" }

func (oc *ObjectCache) Options() manager.Options { return &oc.options }

func (oc *ObjectCache) Init() error {
	oc.cache = freecache.NewCache(oc.options.cacheSize)
	metrics.NewMonitor(oc.Metrics, &cacheSizeMetric{}, func() int64 { return oc.cache.EntryCount() })
	metrics.NewMonitor(oc.Metrics, &cacheEvictionMetric{}, func() int64 { return oc.cache.EvacuateCount() })
	return nil
}

func (oc *ObjectCache) Start(ctx context.Context) error { return nil }

func (oc *ObjectCache) Close(ctx context.Context) error { return nil }

func (oc *ObjectCache) Get(ctx context.Context, object util.ObjectRef) (*unstructured.Unstructured, error) {
	metric := &CacheRequestMetric{Cluster: object.Cluster, Error: "Unknown"}
	defer oc.CacheRequestMetric.DeferCount(oc.Clock.Now(), metric)

	key := objectKey(object)

	for {
		fetchCtx, cancelFunc := context.WithTimeout(ctx, oc.options.fetchTimeout)
		defer cancelFunc()

		cached, _ := oc.cache.GetOrSet(key, []byte{0}, int(oc.options.fetchTimeout.Seconds()))
		if cached != nil {
			if cached[0] == 0 {
				// pending; JSON never starts with a NUL byte
				oc.Clock.Sleep(time.Millisecond * 10) // constant backoff until the previous fetchTimeout returns
				// TODO add metrics to monitor this
				continue
			}

			// cache hit
			return decodeCached(cached, metric)
		} else {
			// cache miss, we have reserved the entry

			persisted := false
			defer func() {
				// release the entry on failure
				if !persisted {
					oc.cache.Del(key)
				}
			}()

			value, err, metricCode := oc.penetrate(fetchCtx, object)
			metric.Error = metricCode
			if err != nil {
				return nil, err
			}

			if value == nil {
				return nil, nil
			}

			valueJson, err := value.MarshalJSON()
			if err != nil {
				metric.Error = "FetchedMarshal"
				return nil, fmt.Errorf("server responds with non-marshalable data")
			}

			err = oc.cache.Set(key, valueJson, int(oc.options.storeTtl.Seconds()))
			if err != nil {
				metric.Error += "/ValueTooLarge"
				// the object is too large, so just don't cache it and return normally
				return value, nil //nolint:nilerr
			}

			persisted = true

			return value, nil
		}
	}
}

func decodeCached(cached []byte, metric *CacheRequestMetric) (*unstructured.Unstructured, error) {
	metric.Hit = true

	uns := &unstructured.Unstructured{}
	err := uns.UnmarshalJSON(cached)
	if err != nil {
		metric.Error = "CacheUnmarshal"
		return nil, fmt.Errorf("cached invalid data: %w", err)
	}

	metric.Error = "nil"
	return uns, nil
}

func (oc *ObjectCache) penetrate(
	ctx context.Context,
	object util.ObjectRef,
) (_raw *unstructured.Unstructured, _err error, _metricCode string) {
	// first, try fetching from server
	clusterClient, err := oc.Clients.Cluster(object.Cluster)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize clients for cluster %q: %w", object.Cluster, err), "UnknownCluster"
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
		return nil, err, string(k8serrors.ReasonForError(err))
	}

	if err == nil {
		return raw, nil, "Apiserver"
	}

	// not found from apiserver, try deletion snapshot instead
	snapshot, err := oc.DiffCache.FetchSnapshot(ctx, object, diffcache.SnapshotNameDeletion)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot fallback to snapshot: %w", err), "SnapshotFetch"), "SnapshotFetch"
	}

	if snapshot != nil {
		uns := &unstructured.Unstructured{}
		if err := uns.UnmarshalJSON(snapshot.Value); err != nil {
			return nil, metrics.LabelError(fmt.Errorf("decode snapshot err: %w", err), "SnapshotDecode"), "SnapshotDecode"
		}

		return uns, nil, "DeletionSnapshot"
	}

	// all methods failed

	return nil, nil, "NotFoundAnywhere"
}

func objectKey(object util.ObjectRef) []byte {
	return []byte(fmt.Sprintf("%s/%s/%s/%s", object.Group, object.Resource, object.Namespace, object.Name))
}
