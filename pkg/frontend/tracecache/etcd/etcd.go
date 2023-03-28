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

package tracecache_etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	etcdv3 "go.etcd.io/etcd/client/v3"

	tracecache "github.com/kubewharf/kelemetry/pkg/frontend/tracecache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.ProvideMuxImpl("jaeger-trace-cache/etcd", newEtcd, tracecache.Cache.Persist)
}

type options struct {
	endpoints   []string
	prefix      string
	dialTimeout time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(&options.endpoints, "jaeger-trace-cache-etcd-endpoints", []string{}, "etcd endpoints")
	fs.StringVar(&options.prefix, "jaeger-trace-cache-etcd-prefix", "/trace/", "etcd prefix")
	fs.DurationVar(
		&options.dialTimeout,
		"jaeger-trace-cache-etcd-dial-timeout",
		time.Second*10,
		"dial timeout for trace cache etcd connection",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type etcdCache struct {
	manager.MuxImplBase

	options   options
	logger    logrus.FieldLogger
	deferList *shutdown.DeferList

	client *etcdv3.Client
}

func newEtcd(logger logrus.FieldLogger) *etcdCache {
	return &etcdCache{
		logger:    logger,
		deferList: shutdown.NewDeferList(),
	}
}

func (_ *etcdCache) MuxImplName() (name string, isDefault bool) { return "etcd", false }

func (cache *etcdCache) Options() manager.Options { return &cache.options }

func (cache *etcdCache) Init(ctx context.Context) error {
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

func (cache *etcdCache) Start(ctx context.Context) error { return nil }

func (cache *etcdCache) Close(ctx context.Context) error {
	if name, err := cache.deferList.Run(cache.logger); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

func (cache *etcdCache) Persist(ctx context.Context, entries []tracecache.Entry) error {
	for i, entry := range entries {
		key := cache.cacheKey(entry.LowId)

		identifierJson, err := json.Marshal(entry.Identifier)
		if err != nil {
			return fmt.Errorf("cannot marshal entry %d: %w", i, err)
		}

		_, err = cache.client.Put(ctx, key, string(identifierJson))
		if err != nil {
			return fmt.Errorf("etcd write error: %w", err)
		}
	}

	return nil
}

func (cache *etcdCache) Fetch(ctx context.Context, lowId uint64) (json.RawMessage, error) {
	cache.logger.Warn(cache.cacheKey(lowId))
	resp, err := cache.client.Get(ctx, cache.cacheKey(lowId))
	if err != nil {
		return nil, fmt.Errorf("etcd get error: %w", err)
	}

	if len(resp.Kvs) == 0 || resp.Kvs[0] == nil {
		return nil, nil
	}

	return json.RawMessage(resp.Kvs[0].Value), nil
}

func (cache *etcdCache) cacheKey(lowId uint64) string {
	return fmt.Sprintf("%s%d", cache.options.prefix, lowId)
}
