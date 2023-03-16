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

	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/cache"
)

type CacheWrapper struct {
	delegate        Cache
	patchCache      *cache.TtlOnce
	PenetrateMetric *metrics.Metric[*penetrateMetric]
	snapshotCache   *cache.TtlOnce
	options         *CommonOptions
	clock           clock.Clock
}

func newCacheWrapper(
	options *CommonOptions,
	delegate Cache,
	clock clock.Clock,
	metricsClient metrics.Client,
) *CacheWrapper {
	return &CacheWrapper{
		delegate:      delegate,
		patchCache:    cache.NewTtlOnce(options.PatchTtl, clock),
		snapshotCache: cache.NewTtlOnce(options.SnapshotTtl, clock),
		options:       options,
		clock:         clock,
	}
}

type penetrateMetric struct {
	Penetrate bool
	Type      string
}

func (*penetrateMetric) MetricName() string { return "diff_cache_memory_wrapper_penetrate" }

type wrapperSizeMetric struct{}

func (*wrapperSizeMetric) MetricName() string { return "diff_cache_memory_wrapper_cardinality" }

func (wrapper *CacheWrapper) initMetricsLoop(metricsClient metrics.Client) {
	metrics.NewMonitor(
		metricsClient,
		&wrapperSizeMetric{},
		func() int64 { return int64(wrapper.patchCache.Size()) },
	)
}

func (wrapper *CacheWrapper) GetCommonOptions() *CommonOptions {
	return wrapper.options
}

func (wrapper *CacheWrapper) Store(ctx context.Context, object util.ObjectRef, patch *Patch) {
	wrapper.delegate.Store(ctx, object, patch)

	wrapper.patchCache.Add(cacheWrapperKey(object, patch.NewResourceVersion), patch)
}

func (wrapper *CacheWrapper) Fetch(
	ctx context.Context,
	object util.ObjectRef,
	oldResourceVersion string,
	newResourceVersion *string,
) (*Patch, error) {
	penetrateMetric := &penetrateMetric{Type: "diff"}
	defer wrapper.PenetrateMetric.DeferCount(wrapper.clock.Now(), penetrateMetric)

	keyRv, err := wrapper.options.ChooseResourceVersion(oldResourceVersion, newResourceVersion)
	if err != nil {
		return nil, err
	}

	if patch, ok := wrapper.patchCache.Get(cacheWrapperKey(object, keyRv)); ok {
		return patch.(*Patch), nil
	}

	penetrateMetric.Penetrate = true

	patch, err := wrapper.delegate.Fetch(ctx, object, oldResourceVersion, newResourceVersion)
	if patch != nil && err == nil {
		wrapper.patchCache.Add(cacheWrapperKey(object, keyRv), patch)
	}

	return patch, err
}

func (wrapper *CacheWrapper) StoreSnapshot(
	ctx context.Context,
	object util.ObjectRef,
	snapshotName string,
	snapshot *Snapshot,
) {
	wrapper.delegate.StoreSnapshot(ctx, object, snapshotName, snapshot)
	wrapper.snapshotCache.Add(cacheWrapperKey(object, snapshotName), snapshot)
}

func (wrapper *CacheWrapper) FetchSnapshot(
	ctx context.Context,
	object util.ObjectRef,
	snapshotName string,
) (*Snapshot, error) {
	penetrateMetric := &penetrateMetric{Type: fmt.Sprintf("snapshot/%s", snapshotName)}
	defer wrapper.PenetrateMetric.DeferCount(wrapper.clock.Now(), penetrateMetric)

	if value, ok := wrapper.patchCache.Get(cacheWrapperKey(object, snapshotName)); ok {
		return value.(*Snapshot), nil
	}

	penetrateMetric.Penetrate = true

	patch, err := wrapper.delegate.FetchSnapshot(ctx, object, snapshotName)
	return patch, err
}

// List always penetrates the cache because we cannot get notified of new keys
func (wrapper *CacheWrapper) List(ctx context.Context, object util.ObjectRef, limit int) ([]string, error) {
	return wrapper.delegate.List(ctx, object, limit)
}

func cacheWrapperKey(object util.ObjectRef, subkey string) string {
	return fmt.Sprintf("%s/%s", object.String(), subkey)
}
