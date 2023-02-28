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

//go:build integration

package etcd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/rand"

	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache"
	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache/etcd"
)

func TestFetchOrReserveReserved(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	_, err := client.FetchOrReserve(ctx, key, time.Second*10)
	assert.Nil(err)
}

func TestFetchOrReservePending(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	_, err := client.Client().Put(ctx, key, string(etcd.ReservedVarintTime()))
	assert.Nil(err)

	_, err = client.FetchOrReserve(ctx, key, time.Second*10)
	assert.ErrorIs(err, spancache.ErrAlreadyReserved, "%#v, %#v", err, spancache.ErrAlreadyReserved)
}

func TestFetchOrReserveFetched(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	putResp, err := client.Client().Put(ctx, key, string([]byte{1, 'o', 'k'}))
	assert.Nil(err)
	putRev := etcd.EncodeInt64(putResp.Header.Revision)

	entry, err := client.FetchOrReserve(ctx, key, time.Second*10)
	assert.Nil(err)
	assert.NotNil(entry)
	assert.Equal("ok", string(entry.Value))
	assert.Equal(spancache.Uid(putRev[:]), entry.LastUid[:])
}

func TestFetchSucceed(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	putResp, err := client.Client().Put(ctx, key, string([]byte{1, 'o', 'k'}))
	assert.Nil(err)
	putRev := etcd.EncodeInt64(putResp.Header.Revision)

	entry, err := client.Fetch(ctx, key)
	assert.Nil(err)
	assert.NotNil(entry)
	assert.Equal("ok", string(entry.Value))
	assert.Equal(spancache.Uid(putRev[:]), entry.LastUid)
}

func TestFetchNotFound(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	entry, err := client.Fetch(ctx, key)
	assert.Nil(err)
	assert.Nil(entry)
}

func TestSetReservedSucceed(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	entry, err := client.FetchOrReserve(ctx, key, time.Second*10)
	assert.Nil(err)
	assert.NotNil(entry)
	assert.Nil(entry.Value)

	value := []byte(randomKey())
	err = client.SetReserved(ctx, key, value, entry.LastUid, time.Second*10)
	assert.Nil(err)

	entry, err = client.Fetch(ctx, key)
	assert.Nil(err)
	assert.NotNil(entry)
	assert.Equal(value, entry.Value)
}

func TestSetReservedNotFound(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	entry, err := client.Fetch(ctx, key)
	assert.Nil(err)
	assert.Nil(entry)

	value := []byte(randomKey())
	err = client.SetReserved(ctx, key, value, []byte("abcdefgh"), time.Second*10)
	assert.ErrorIs(err, spancache.ErrInvalidKey)
}

func TestSetReservedCasFailed(t *testing.T) {
	assert := assert.New(t)
	ctx, client := testClient(t)
	key := randomKey()

	entry, err := client.FetchOrReserve(ctx, key, time.Second*10)
	assert.Nil(err)
	assert.Nil(entry.Value)

	value := []byte(randomKey())
	err = client.SetReserved(ctx, key, value, []byte("abcdefgh"), time.Second*10)
	assert.ErrorIs(err, spancache.ErrUidMismatch)
}

func testClient(t *testing.T) (context.Context, *etcd.Etcd) {
	comp := etcd.NewEtcd(logrus.New())

	fs := pflag.NewFlagSet("test", pflag.PanicOnError)
	comp.Options().Setup(fs)
	fs.Parse([]string{
		"--span-cache-etcd-endpoints=http://127.0.0.1:2379",
		"--span-cache-etcd-prefix=", // to allow reusing the same key in direct ops
	})

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Second*10)
	ch := make(chan struct{})

	if err := comp.Init(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { cancelFunc(); close(ch) })

	if err := comp.Start(ch); err != nil {
		t.Fatal(err)
	}

	return ctx, comp
}

func randomKey() string {
	return rand.String(16)
}
