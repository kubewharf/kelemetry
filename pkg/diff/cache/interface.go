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
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("diff-cache", newCache)
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
	PatchTtl              time.Duration
	SnapshotTtl           time.Duration
	EnableCacheWrapper    bool
	UseOldResourceVersion bool
}

func (options *CommonOptions) ChooseResourceVersion(oldRv string, newRv *string) (string, error) {
	if options.UseOldResourceVersion {
		return oldRv, nil
	} else if newRv != nil {
		return *newRv, nil
	} else {
		return "", metrics.MakeLabeledError("NoNewRv")
	}
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
	fs.BoolVar(
		&options.UseOldResourceVersion,
		"diff-cache-use-old-rv",
		false,
		"index diff entries with the resource version before update "+
			"(inaccurate, but works with Metadata-level audit policy)",
	)
}

type Cache interface {
	GetCommonOptions() *CommonOptions

	Store(ctx context.Context, object util.ObjectRef, patch *Patch)
	Fetch(ctx context.Context, object util.ObjectRef, oldResourceVersion string, newResourceVersion *string) (*Patch, error)

	StoreSnapshot(ctx context.Context, object util.ObjectRef, snapshotName string, snapshot *Snapshot)
	FetchSnapshot(ctx context.Context, object util.ObjectRef, snapshotName string) (*Snapshot, error)

	List(ctx context.Context, object util.ObjectRef, limit int) ([]string, error)
}

type mux struct {
	options *CommonOptions
	*manager.Mux
	metrics metrics.Client
	logger  logrus.FieldLogger
	clock   clock.Clock

	impl Cache

	storeDiffMetric     metrics.Metric
	fetchDiffMetric     metrics.Metric
	storeSnapshotMetric metrics.Metric
	fetchSnapshotMetric metrics.Metric
	listMetric          metrics.Metric
}

func newCache(
	logger logrus.FieldLogger,
	clock clock.Clock,
	metrics metrics.Client,
) Cache {
	options := &CommonOptions{}
	return &mux{
		options: options,
		Mux:     manager.NewMux("diff-cache", false).WithAdditionalOptions(options),
		logger:  logger,
		clock:   clock,
		metrics: metrics,
	}
}

type (
	storeMetric struct {
		Redacted bool
	}
	fetchMetric struct {
		Found bool
		Error metrics.LabeledError
	}
	listMetric struct{}
)

func (mux *mux) Init(ctx context.Context) error {
	if err := mux.Mux.Init(ctx); err != nil {
		return err
	}

	mux.impl = mux.Impl().(Cache)
	if mux.options.EnableCacheWrapper {
		wrapper := newCacheWrapper(mux.options, mux.impl, mux.clock, mux.metrics)
		wrapper.initMetricsLoop(mux.metrics)
		mux.impl = wrapper
	}

	mux.storeDiffMetric = mux.metrics.New("diff_cache_store", &storeMetric{})
	mux.fetchDiffMetric = mux.metrics.New("diff_cache_fetch", &fetchMetric{})
	mux.storeSnapshotMetric = mux.metrics.New("diff_cache_store_snapshot", &storeMetric{})
	mux.fetchSnapshotMetric = mux.metrics.New("diff_cache_fetch_snapshot", &fetchMetric{})
	mux.listMetric = mux.metrics.New("diff_cache_list", &listMetric{})

	return nil
}

func (mux *mux) Start(ctx context.Context) error {
	if wrapped, ok := mux.impl.(*CacheWrapper); ok {
		go wrapped.patchCache.RunCleanupLoop(ctx, mux.logger)
	}

	return nil
}

func (mux *mux) GetCommonOptions() *CommonOptions {
	return mux.options
}

func (mux *mux) Store(ctx context.Context, object util.ObjectRef, patch *Patch) {
	defer mux.storeDiffMetric.DeferCount(mux.clock.Now(), &storeMetric{Redacted: patch.Redacted})
	mux.Impl().(Cache).Store(ctx, object, patch)
}

func (mux *mux) Fetch(ctx context.Context, object util.ObjectRef, oldResourceVersion string, newResourceVersion *string) (*Patch, error) {
	metric := &fetchMetric{}
	defer mux.fetchDiffMetric.DeferCount(mux.clock.Now(), metric)

	patch, err := mux.Impl().(Cache).Fetch(ctx, object, oldResourceVersion, newResourceVersion)
	if err != nil {
		metric.Error = err
		return nil, err
	}

	metric.Found = patch != nil
	return patch, nil
}

func (mux *mux) StoreSnapshot(ctx context.Context, object util.ObjectRef, snapshotName string, snapshot *Snapshot) {
	defer mux.storeSnapshotMetric.DeferCount(mux.clock.Now(), &storeMetric{Redacted: snapshot.Redacted})
	mux.Impl().(Cache).StoreSnapshot(ctx, object, snapshotName, snapshot)
}

func (mux *mux) FetchSnapshot(ctx context.Context, object util.ObjectRef, snapshotName string) (*Snapshot, error) {
	metric := &fetchMetric{}
	defer mux.fetchSnapshotMetric.DeferCount(mux.clock.Now(), metric)

	snapshot, err := mux.Impl().(Cache).FetchSnapshot(ctx, object, snapshotName)
	if err != nil {
		metric.Error = err
		return nil, err
	}

	metric.Found = snapshot != nil
	return snapshot, nil
}

func (mux *mux) List(ctx context.Context, object util.ObjectRef, limit int) ([]string, error) {
	defer mux.listMetric.DeferCount(mux.clock.Now(), &listMetric{})
	return mux.Impl().(Cache).List(ctx, object, limit)
}
