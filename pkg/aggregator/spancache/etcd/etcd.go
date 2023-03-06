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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	etcdv3 "go.etcd.io/etcd/client/v3"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.ProvideMuxImpl("spancache/etcd", NewEtcd, spancache.Cache.Fetch)
}

type etcdOptions struct {
	endpoints   []string
	prefix      string
	dialTimeout time.Duration
}

func (options *etcdOptions) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(&options.endpoints, "span-cache-etcd-endpoints", []string{}, "etcd endpoints")
	fs.StringVar(&options.prefix, "span-cache-etcd-prefix", "/span/", "etcd prefix")
	fs.DurationVar(&options.dialTimeout, "span-cache-etcd-dial-timeout", time.Second*10, "dial timeout for span cache etcd connection")
}

func (options *etcdOptions) EnableFlag() *bool { return nil }

type Etcd struct {
	manager.MuxImplBase

	options   etcdOptions
	logger    logrus.FieldLogger
	clock     clock.Clock
	client    *etcdv3.Client
	deferList *shutdown.DeferList
}

var _ spancache.Cache = &Etcd{}

func NewEtcd(
	logger logrus.FieldLogger,
	clock clock.Clock,
) *Etcd {
	return &Etcd{
		logger:    logger,
		clock:     clock,
		deferList: shutdown.NewDeferList(),
	}
}

func (_ *Etcd) MuxImplName() (name string, isDefault bool) { return "etcd", false }

func (cache *Etcd) Options() manager.Options { return &cache.options }

func (cache *Etcd) Init(ctx context.Context) error {
	if len(cache.options.endpoints) == 0 {
		return fmt.Errorf("No etcd endpoints provided")
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
	if name, err := cache.deferList.Run(cache.logger); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

func (cache *Etcd) FetchOrReserve(ctx context.Context, key string, ttl time.Duration) (*spancache.Entry, error) {
	key = cache.options.prefix + key
	buf := cache.ReservedVarintTime()

	reserveLease, err := cache.client.Lease.Grant(ctx, int64(ttl.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("cannot create lease to reserve key: %w", err)
	}

	txn, err := cache.client.KV.Txn(ctx).
		If(etcdv3.Compare(etcdv3.CreateRevision(key), "=", 0)).
		Then(etcdv3.OpPut(key, string(buf), etcdv3.WithLease(reserveLease.ID))).
		Else(etcdv3.OpGet(key)).
		Commit()
	if err != nil {
		return nil, fmt.Errorf("cannot create-or-get key: %w", err)
	}

	if !txn.Succeeded {
		resp := txn.Responses[0].GetResponseRange().Kvs[0]
		reader := bytes.NewReader(resp.Value)

		isInit, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("etcd returned unexpected empty value")
		}

		switch isInit {
		case 0:
			// reserved by another request

			reserveTimeUnix, err := binary.ReadVarint(reader)
			if err != nil {
				return nil, fmt.Errorf("etcd returned value with invalid reserve timestamp: %w", err)
			}
			reserveTime := time.UnixMilli(reserveTimeUnix)

			return nil, fmt.Errorf("%w for %s", spancache.ErrAlreadyReserved, cache.clock.Since(reserveTime))
		case 1:
			// already initialized

			value, _ := io.ReadAll(reader) // ReadAll on bytes.Reader is infallible

			uid := EncodeInt64(resp.CreateRevision)

			return &spancache.Entry{
				Value:   value,
				LastUid: uid[:],
			}, nil
		default:
			return nil, fmt.Errorf("etcd returned value with invalid header")
		}
	}

	resp := txn.Responses[0].GetResponsePut()
	rev := EncodeInt64(resp.Header.Revision)

	return &spancache.Entry{
		Value:   nil,
		LastUid: rev[:],
	}, nil
}

func (cache *Etcd) Fetch(ctx context.Context, key string) (*spancache.Entry, error) {
	key = cache.options.prefix + key

	resp, err := cache.client.KV.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("etcd request error: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, nil
	}

	kv := resp.Kvs[0]

	uid := EncodeInt64(kv.ModRevision)

	reader := bytes.NewReader(kv.Value)
	isInit, err := reader.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("etcd returned result with empty value")
	}

	var value []byte

	switch isInit {
	case 0:
		value = nil
	case 1:
		value, _ = io.ReadAll(reader) // ReadAll on bytes.Reader is infallible
	default:
		return nil, fmt.Errorf("etcd returned value with invalid header")
	}

	return &spancache.Entry{
		Value:   value,
		LastUid: uid[:],
	}, nil
}

func (cache *Etcd) SetReserved(ctx context.Context, key string, value []byte, lastUid spancache.Uid, ttl time.Duration) error {
	key = cache.options.prefix + key

	var uid [8]byte
	copy(uid[:], lastUid)
	revision := DecodeInt64(uid)

	buf := append([]byte{1}, value...)

	persistLease, err := cache.client.Lease.Grant(ctx, int64(ttl.Seconds()))
	if err != nil {
		return fmt.Errorf("cannot create persistence lease: %w", err)
	}

	resp, err := cache.client.KV.Txn(ctx).
		If(etcdv3.Compare(etcdv3.ModRevision(key), "=", revision)).
		Then(etcdv3.OpPut(key, string(buf), etcdv3.WithLease(persistLease.ID))).
		Else(etcdv3.OpGet(key)).
		Commit()
	if err != nil {
		return fmt.Errorf("cannot compare-and-swap key: %w", err)
	}

	if resp.Succeeded {
		return nil
	}

	getResp := resp.Responses[0].GetResponseRange()

	if len(getResp.Kvs) == 0 {
		return spancache.ErrInvalidKey
	}

	realRev := getResp.Kvs[0].ModRevision

	if realRev != revision {
		return fmt.Errorf("%w (expect %x, persisted %x)", spancache.ErrUidMismatch, revision, realRev)
	}

	return fmt.Errorf("etcd misbehavior, %x is not reflexively equal", revision)
}

func (cache *Etcd) Client() *etcdv3.Client { return cache.client }

func EncodeInt64(i int64) [8]byte {
	ret := [8]byte{}
	binary.LittleEndian.PutUint64(ret[:], uint64(i))
	return ret
}

func DecodeInt64(b [8]byte) int64 {
	return int64(binary.LittleEndian.Uint64(b[:]))
}

func (cache *Etcd) ReservedVarintTime() []byte {
	now := EncodeInt64(cache.clock.Now().UnixMilli())
	return append([]byte{0}, now[:]...)
}
