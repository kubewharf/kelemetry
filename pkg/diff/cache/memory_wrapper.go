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

package diffcache

import (
	"context"
	"fmt"

	"k8s.io/utils/clock"

	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/cache"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

type CacheWrapper struct {
	delegate        Cache
	patchCache      *cache.TtlOnce
	penetrateMetric *metrics.Metric[*penetrateMetric]
	clusterConfigs  k8sconfig.Config
	snapshotCache   *cache.TtlOnce
	options         *CommonOptions
	clock           clock.Clock
}

func newCacheWrapper(
	options *CommonOptions,
	delegate Cache,
	clock clock.Clock,
	clusterConfigs k8sconfig.Config,
	penetrateMetric *metrics.Metric[*penetrateMetric],
) *CacheWrapper {
	cacheWrapper := &CacheWrapper{
		delegate:        delegate,
		penetrateMetric: penetrateMetric,
		clusterConfigs:  clusterConfigs,
		options:         options,
		clock:           clock,
	}
	if options.PatchTtl > 0 {
		cacheWrapper.patchCache = cache.NewTtlOnce(options.PatchTtl, clock)
	}
	if options.SnapshotTtl > 0 {
		cacheWrapper.snapshotCache = cache.NewTtlOnce(options.SnapshotTtl, clock)
	}

	return cacheWrapper
}

type penetrateMetric struct {
	Penetrate bool
	Type      string
}

func (*penetrateMetric) MetricName() string { return "diff_cache_memory_wrapper_penetrate" }

type wrapperSizeMetric struct {
	Type string
}

func (*wrapperSizeMetric) MetricName() string { return "diff_cache_memory_wrapper_cardinality" }

func (wrapper *CacheWrapper) initMetricsLoop(metricsClient metrics.Client) {
	metrics.NewMonitor(
		metricsClient,
		&wrapperSizeMetric{Type: "diff"},
		func() float64 {
			if wrapper.patchCache == nil {
				return 0
			}
			return float64(wrapper.patchCache.Size())
		},
	)

	metrics.NewMonitor(
		metricsClient,
		&wrapperSizeMetric{Type: "snapshot"},
		func() float64 {
			if wrapper.snapshotCache == nil {
				return 0
			}
			return float64(wrapper.snapshotCache.Size())
		},
	)
}

func (wrapper *CacheWrapper) GetCommonOptions() *CommonOptions {
	return wrapper.options
}

func (wrapper *CacheWrapper) Store(ctx context.Context, object utilobject.Key, patch *Patch) {
	wrapper.delegate.Store(ctx, object, patch)

	if wrapper.patchCache != nil {
		wrapper.patchCache.Add(cacheWrapperKey(object, patch.NewResourceVersion), patch)
	}
}

func (wrapper *CacheWrapper) Fetch(
	ctx context.Context,
	object utilobject.Key,
	oldResourceVersion string,
	newResourceVersion *string,
) (*Patch, error) {
	penetrateMetric := &penetrateMetric{Type: "diff"}
	defer wrapper.penetrateMetric.DeferCount(wrapper.clock.Now(), penetrateMetric)

	keyRv, err := wrapper.clusterConfigs.Provide(object.Cluster).ChooseResourceVersion(oldResourceVersion, newResourceVersion)
	if err != nil {
		return nil, err
	}

	if wrapper.patchCache != nil {
		if patch, ok := wrapper.patchCache.Get(cacheWrapperKey(object, keyRv)); ok {
			return patch.(*Patch), nil
		}
	}
	penetrateMetric.Penetrate = true

	patch, err := wrapper.delegate.Fetch(ctx, object, oldResourceVersion, newResourceVersion)
	if wrapper.patchCache != nil && patch != nil && err == nil {
		wrapper.patchCache.Add(cacheWrapperKey(object, keyRv), patch)
	}

	return patch, err
}

func (wrapper *CacheWrapper) StoreSnapshot(
	ctx context.Context,
	object utilobject.Key,
	snapshotName string,
	snapshot *Snapshot,
) {
	wrapper.delegate.StoreSnapshot(ctx, object, snapshotName, snapshot)
	if wrapper.snapshotCache != nil {
		wrapper.snapshotCache.Add(cacheWrapperKey(object, snapshotName), snapshot)
	}
}

func (wrapper *CacheWrapper) FetchSnapshot(
	ctx context.Context,
	object utilobject.Key,
	snapshotName string,
) (*Snapshot, error) {
	penetrateMetric := &penetrateMetric{Type: fmt.Sprintf("snapshot/%s", snapshotName)}
	defer wrapper.penetrateMetric.DeferCount(wrapper.clock.Now(), penetrateMetric)

	if wrapper.snapshotCache != nil {
		if value, ok := wrapper.snapshotCache.Get(cacheWrapperKey(object, snapshotName)); ok {
			return value.(*Snapshot), nil
		}
	}

	penetrateMetric.Penetrate = true

	patch, err := wrapper.delegate.FetchSnapshot(ctx, object, snapshotName)
	if wrapper.snapshotCache != nil && patch != nil && err == nil {
		wrapper.snapshotCache.Add(cacheWrapperKey(object, snapshotName), patch)
	}
	return patch, err
}

// List always penetrates the cache because we cannot get notified of new keys
func (wrapper *CacheWrapper) List(ctx context.Context, object utilobject.Key, limit int) ([]string, error) {
	return wrapper.delegate.List(ctx, object, limit)
}

func cacheWrapperKey(object utilobject.Key, subkey string) string {
	return fmt.Sprintf("%s/%s", object.String(), subkey)
}
