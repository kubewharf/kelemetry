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
	"encoding/json"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	diffcmp "github.com/kubewharf/kelemetry/pkg/diff/cmp"
	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

func init() {
	manager.Global.Provide("diff-cache", manager.Ptr(newCache()))
}

type Patch struct {
	InformerTime       time.Time
	OldResourceVersion string
	NewResourceVersion string
	Redacted           bool `json:"Redacted,omitempty"`
	DiffList           diffcmp.DiffList
}

type Snapshot struct {
	ResourceVersion string
	Redacted        bool `json:"Redacted,omitempty"`
	Value           json.RawMessage
}

type CommonOptions struct {
	PatchTtl           time.Duration
	SnapshotTtl        time.Duration
	EnableCacheWrapper bool
}

func (options *CommonOptions) Setup(fs *pflag.FlagSet) {
	fs.DurationVar(&options.PatchTtl, "diff-cache-patch-ttl", time.Minute*10, "duration for which patch cache remains (0 to disable TTL)")
	fs.DurationVar(
		&options.SnapshotTtl,
		"diff-cache-snapshot-ttl",
		time.Minute*10,
		"duration for which snapshot cache remains (0 to disable TTL)",
	)
	fs.BoolVar(&options.EnableCacheWrapper, "diff-cache-wrapper-enable", false, "enable an intermediate layer of cache in memory")
}

type Cache interface {
	GetCommonOptions() *CommonOptions

	Store(ctx context.Context, object utilobject.Key, patch *Patch)
	Fetch(ctx context.Context, object utilobject.Key, oldResourceVersion string, newResourceVersion *string) (*Patch, error)

	StoreSnapshot(ctx context.Context, object utilobject.Key, snapshotName string, snapshot *Snapshot)
	FetchSnapshot(ctx context.Context, object utilobject.Key, snapshotName string) (*Snapshot, error)

	List(ctx context.Context, object utilobject.Key, limit int) ([]string, error)
}

type mux struct {
	options *CommonOptions
	*manager.Mux
	Metrics        metrics.Client
	Logger         logrus.FieldLogger
	Clock          clock.Clock
	ClusterConfigs k8sconfig.Config

	impl Cache

	StoreDiffMetric     *metrics.Metric[*storeDiffMetric]
	FetchDiffMetric     *metrics.Metric[*fetchDiffMetric]
	StoreSnapshotMetric *metrics.Metric[*storeSnapshotMetric]
	FetchSnapshotMetric *metrics.Metric[*fetchSnapshotMetric]
	ListMetric          *metrics.Metric[*listMetric]
	PenetrateMetric     *metrics.Metric[*penetrateMetric]
}

func newCache() Cache {
	options := &CommonOptions{}
	return &mux{
		options: options,
		Mux:     manager.NewMux("diff-cache", false).WithAdditionalOptions(options),
	}
}

type storeDiffMetric struct {
	Redacted bool
}

func (*storeDiffMetric) MetricName() string { return "diff_cache_store" }

type fetchDiffMetric struct {
	Found bool
	Error metrics.LabeledError
}

func (*fetchDiffMetric) MetricName() string { return "diff_cache_fetch" }

type storeSnapshotMetric struct {
	Redacted bool
}

func (*storeSnapshotMetric) MetricName() string { return "diff_cache_store_snapshot" }

type fetchSnapshotMetric struct {
	Found bool
	Error metrics.LabeledError
}

func (*fetchSnapshotMetric) MetricName() string { return "diff_cache_fetch_snapshot" }

type listMetric struct{}

func (*listMetric) MetricName() string { return "diff_cache_list" }

func (mux *mux) Init() error {
	if err := mux.Mux.Init(); err != nil {
		return err
	}

	mux.impl = mux.Impl().(Cache)
	if mux.options.EnableCacheWrapper {
		wrapper := newCacheWrapper(mux.options, mux.impl, mux.Clock, mux.ClusterConfigs, mux.PenetrateMetric)
		wrapper.initMetricsLoop(mux.Metrics)
		mux.impl = wrapper
	}

	return nil
}

func (mux *mux) Start(ctx context.Context) error {
	if wrapped, ok := mux.impl.(*CacheWrapper); ok {
		go wrapped.patchCache.RunCleanupLoop(ctx, mux.Logger)
	}

	return nil
}

func (mux *mux) GetCommonOptions() *CommonOptions {
	return mux.options
}

func (mux *mux) Store(ctx context.Context, object utilobject.Key, patch *Patch) {
	defer mux.StoreDiffMetric.DeferCount(mux.Clock.Now(), &storeDiffMetric{Redacted: patch.Redacted})
	mux.Impl().(Cache).Store(ctx, object, patch)
}

func (mux *mux) Fetch(ctx context.Context, object utilobject.Key, oldResourceVersion string, newResourceVersion *string) (*Patch, error) {
	metric := &fetchDiffMetric{}
	defer mux.FetchDiffMetric.DeferCount(mux.Clock.Now(), metric)

	patch, err := mux.Impl().(Cache).Fetch(ctx, object, oldResourceVersion, newResourceVersion)
	if err != nil {
		metric.Error = err
		return nil, err
	}

	metric.Found = patch != nil
	return patch, nil
}

func (mux *mux) StoreSnapshot(ctx context.Context, object utilobject.Key, snapshotName string, snapshot *Snapshot) {
	defer mux.StoreSnapshotMetric.DeferCount(mux.Clock.Now(), &storeSnapshotMetric{Redacted: snapshot.Redacted})
	mux.Impl().(Cache).StoreSnapshot(ctx, object, snapshotName, snapshot)
}

func (mux *mux) FetchSnapshot(ctx context.Context, object utilobject.Key, snapshotName string) (*Snapshot, error) {
	metric := &fetchSnapshotMetric{}
	defer mux.FetchSnapshotMetric.DeferCount(mux.Clock.Now(), metric)

	snapshot, err := mux.Impl().(Cache).FetchSnapshot(ctx, object, snapshotName)
	if err != nil {
		metric.Error = err
		return nil, err
	}

	metric.Found = snapshot != nil
	return snapshot, nil
}

func (mux *mux) List(ctx context.Context, object utilobject.Key, limit int) ([]string, error) {
	defer mux.ListMetric.DeferCount(mux.Clock.Now(), &listMetric{})
	return mux.Impl().(Cache).List(ctx, object, limit)
}
