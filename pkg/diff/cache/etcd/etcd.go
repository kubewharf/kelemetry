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

package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	etcdv3 "go.etcd.io/etcd/client/v3"

	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.ProvideMuxImpl("diff-cache/etcd", manager.Ptr(&Etcd{
		deferList: shutdown.NewDeferList(),
	}), diffcache.Cache.Store)
}

type etcdOptions struct {
	endpoints   []string
	prefix      string
	dialTimeout time.Duration
}

func (options *etcdOptions) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(&options.endpoints, "diff-cache-etcd-endpoints", []string{}, "etcd endpoints")
	fs.StringVar(&options.prefix, "diff-cache-etcd-prefix", "/diff/", "etcd prefix")
	fs.DurationVar(
		&options.dialTimeout,
		"diff-cache-etcd-dial-timeout",
		time.Second*10,
		"dial timeout for diff cache etcd connection",
	)
}

func (options *etcdOptions) EnableFlag() *bool { return nil }

type Etcd struct {
	manager.MuxImplBase

	options etcdOptions
	Logger  logrus.FieldLogger

	client    *etcdv3.Client
	deferList *shutdown.DeferList
}

var _ diffcache.Cache = &Etcd{}

func (_ *Etcd) MuxImplName() (name string, isDefault bool) { return "etcd", false }

func (cache *Etcd) Options() manager.Options { return &cache.options }

func (cache *Etcd) Init(ctx context.Context) error {
	if len(cache.options.endpoints) == 0 {
		return fmt.Errorf("no etcd endpoints provided")
	}

	client, err := etcdv3.New(etcdv3.Config{
		Endpoints:   cache.options.endpoints,
		DialTimeout: cache.options.dialTimeout,
	})
	if err != nil {
		return fmt.Errorf("cannot connect to etcd: %w", err)
	}

	cache.deferList.Defer("closing etcd client", client.Close)
	cache.client = client

	return nil
}

func (cache *Etcd) Start(stopCh <-chan struct{}) error {
	return nil
}

func (cache *Etcd) Close() error {
	if name, err := cache.deferList.Run(cache.Logger); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

func (cache *Etcd) GetCommonOptions() *diffcache.CommonOptions {
	return cache.GetAdditionalOptions().(*diffcache.CommonOptions)
}

func (cache *Etcd) Store(ctx context.Context, object util.ObjectRef, patch *diffcache.Patch) {
	patchJson, err := json.Marshal(patch)
	if err != nil {
		cache.Logger.WithError(err).Error("cannot marshal patch")
		return
	}

	lease, err := cache.client.Lease.Grant(ctx, int64(cache.GetCommonOptions().PatchTtl.Seconds()))
	if err != nil {
		cache.Logger.WithError(err).Error("cannot grant lease for diff cache")
		return
	}

	keyRv, _ := cache.GetCommonOptions().ChooseResourceVersion(patch.OldResourceVersion, &patch.NewResourceVersion)
	_, err = cache.client.KV.Put(ctx, cache.cacheKey(object, keyRv), string(patchJson), etcdv3.WithLease(lease.ID))
	if err != nil {
		cache.Logger.WithError(err).Error("cannot write cache")
		return
	}
}

func (cache *Etcd) Fetch(
	ctx context.Context,
	object util.ObjectRef,
	oldResourceVersion string,
	newResourceVersion *string,
) (*diffcache.Patch, error) {
	keyRv, err := cache.GetCommonOptions().ChooseResourceVersion(oldResourceVersion, newResourceVersion)
	if err != nil {
		return nil, err
	}

	key := cache.cacheKey(object, keyRv)

	resp, err := cache.client.KV.Get(ctx, key)
	if err != nil {
		cache.Logger.WithError(err).Error("cannot fetch cache")
		return nil, metrics.LabelError(err, "UnknownEtcd")
	}

	if len(resp.Kvs) == 0 {
		return nil, nil
	}

	jsonBuf := resp.Kvs[0].Value
	patch := &diffcache.Patch{}
	if err := json.Unmarshal(jsonBuf, patch); err != nil {
		cache.Logger.WithError(err).Error("cannot decode etcd result")
		return nil, metrics.LabelError(err, "EtcdValueError")
	}

	return patch, nil
}

func (cache *Etcd) StoreSnapshot(ctx context.Context, object util.ObjectRef, snapshotName string, snapshot *diffcache.Snapshot) {
	snapshotJson, err := json.Marshal(snapshot)
	if err != nil {
		cache.Logger.WithError(err).Error("cannot marshal snapshot")
		return
	}

	lease, err := cache.client.Lease.Grant(ctx, int64(cache.GetCommonOptions().SnapshotTtl.Seconds()))
	if err != nil {
		cache.Logger.WithError(err).Error("cannot grant lease for diff cache")
		return
	}

	key := cache.snapshotKey(object, snapshotName)
	_, err = cache.client.KV.Put(ctx, key, string(snapshotJson), etcdv3.WithLease(lease.ID))
	if err != nil {
		cache.Logger.WithError(err).Error("cannot write cache")
		return
	}
}

func (cache *Etcd) FetchSnapshot(ctx context.Context, object util.ObjectRef, snapshotName string) (*diffcache.Snapshot, error) {
	key := cache.snapshotKey(object, snapshotName)
	resp, err := cache.client.KV.Get(ctx, key)
	if err != nil {
		cache.Logger.WithError(err).Error("cannot fetch cache")
		return nil, metrics.LabelError(err, "UnknownEtcd")
	}

	if len(resp.Kvs) == 0 {
		return nil, nil
	}

	jsonBuf := resp.Kvs[0].Value
	snapshot := &diffcache.Snapshot{}
	if err := json.Unmarshal(jsonBuf, snapshot); err != nil {
		cache.Logger.WithError(err).Error("cannot decode etcd result")
		return nil, metrics.LabelError(err, "EtcdValueError")
	}

	return snapshot, nil
}

func (cache *Etcd) List(ctx context.Context, object util.ObjectRef, limit int) ([]string, error) {
	resp, err := cache.client.KV.Get(
		ctx,
		cache.cacheKeyPrefix(object),
		etcdv3.WithLimit(int64(limit)),
		etcdv3.WithPrefix(),
		etcdv3.WithKeysOnly(),
		// resource versions are not collatable,
		// but we try to sort them naively (without even natural sort)
		// to get more reasonable result for debugging.
		etcdv3.WithSort(etcdv3.SortByKey, etcdv3.SortDescend),
	)
	if err != nil {
		return nil, fmt.Errorf("etcd scan error: %w", err)
	}

	keys := make([]string, 0, resp.Count)
	for _, kv := range resp.Kvs {
		key := string(kv.Key)
		rvIndex := strings.LastIndexByte(key, '/') + 1
		keys = append(keys, key[rvIndex:])
	}
	return keys, nil
}

func (cache *Etcd) cacheKeyPrefix(object util.ObjectRef) string {
	return fmt.Sprintf("%s%s/", cache.options.prefix, object.String())
}

func (cache *Etcd) cacheKey(object util.ObjectRef, keyRv string) string {
	whichRv := "newRv"
	if cache.GetCommonOptions().UseOldResourceVersion {
		whichRv = "oldRv"
	}

	return cache.cacheKeyPrefix(object) + fmt.Sprintf("%s/%s", whichRv, keyRv)
}

func (cache *Etcd) snapshotKey(object util.ObjectRef, snapshotName string) string {
	return cache.cacheKeyPrefix(object) + snapshotName
}
