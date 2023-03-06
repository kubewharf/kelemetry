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

package spancache

import (
	"context"
	"encoding/hex"
	"time"

	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/errors"
)

func init() {
	manager.Global.Provide("spancache", newMux)
}

// Uid identifies an entry reservation.
type Uid []byte

func (uid Uid) String() string {
	return hex.EncodeToString(uid)
}

type Entry struct {
	// The value of the entry, if it has been initialized.
	// This slice is immutable.
	Value []byte
	// The UID of the entry when it was fetched.
	// UID is regenerated every time it is reserved
	LastUid Uid
}

var (
	ErrAlreadyReserved = metrics.LabelError(errors.New("entry is already reserved"), "spancache.AlreadyReserved")
	ErrInvalidKey      = metrics.LabelError(errors.New("key refers to invalid or expired entry"), "spancache.InvalidKey")
	ErrUidMismatch     = metrics.LabelError(errors.New("UID mismatch"), "spancache.UidMismatch")
)

func ShouldRetry(err error) bool {
	ret := errors.Is(err, ErrAlreadyReserved) || errors.Is(err, ErrInvalidKey) || errors.Is(err, ErrUidMismatch)
	return ret
}

// Cache is the abstraction of a linearized, probably-shared key-value storage.
// A string key results in a reserved, initialized or invalid entry.
// where each entry is either in "reserved" or "initialized" state.
type Cache interface {
	// FetchOrReserve reserves an entry if it is invalid.
	// Returns ErrAlreadyReserved if entry is currently in reserved state.
	// Returns the entry if a new reservation is made with nil entry.Value,
	// and returns the entry with non-nil entry.Value if it is valid and initialized.
	FetchOrReserve(ctx context.Context, key string, ttl time.Duration) (*Entry, error)

	// Fetch fetches the data of an entry if it is initialized,
	// and returns an entry with nil Value if it is in reserved state.
	// Returns nil if the entry does not exist or has expired.
	Fetch(ctx context.Context, key string) (*Entry, error)

	// SetReserved sets the value of a reserved entry.
	// Returns non-nil error if the entry UID mismatches or if the entry is not reserved.
	SetReserved(ctx context.Context, key string, value []byte, lastUid Uid, ttl time.Duration) error
}

type mux struct {
	*manager.Mux
	clock   clock.Clock
	metrics metrics.Client

	fetchOrReserveMetric, fetchMetric, unsetAndReserveMetric, setReservedMetric metrics.Metric
}

func newMux(
	clock clock.Clock,
	metrics metrics.Client,
) Cache {
	return &mux{
		Mux:     manager.NewMux("span-cache", false),
		clock:   clock,
		metrics: metrics,
	}
}

type (
	fetchOrReserveMetric  struct{}
	fetchMetric           struct{}
	unsetAndReserveMetric struct{}
	setReservedMetric     struct{}
)

func (mux *mux) Init(ctx context.Context) error {
	mux.fetchOrReserveMetric = mux.metrics.New("spancache_fetch_or_reserve", &fetchOrReserveMetric{})
	mux.fetchMetric = mux.metrics.New("spancache_fetch", &fetchMetric{})
	mux.unsetAndReserveMetric = mux.metrics.New("spancache_unset_and_reserve", &unsetAndReserveMetric{})
	mux.setReservedMetric = mux.metrics.New("spancache_set_reserved", &setReservedMetric{})

	return mux.Mux.Init(ctx)
}

func (mux *mux) FetchOrReserve(ctx context.Context, key string, ttl time.Duration) (*Entry, error) {
	defer mux.fetchOrReserveMetric.DeferCount(mux.clock.Now(), &fetchOrReserveMetric{})
	return mux.Impl().(Cache).FetchOrReserve(ctx, key, ttl)
}

func (mux *mux) Fetch(ctx context.Context, key string) (*Entry, error) {
	defer mux.fetchMetric.DeferCount(mux.clock.Now(), &fetchMetric{})
	return mux.Impl().(Cache).Fetch(ctx, key)
}

func (mux *mux) SetReserved(ctx context.Context, key string, value []byte, lastUid Uid, ttl time.Duration) error {
	defer mux.setReservedMetric.DeferCount(mux.clock.Now(), &setReservedMetric{})
	return mux.Impl().(Cache).SetReserved(ctx, key, value, lastUid, ttl)
}
