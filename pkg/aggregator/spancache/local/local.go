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

package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.ProvideMuxImpl("spancache/local", manager.Ptr(&Local{
		entries: map[string]*localEntry{},
	}), spancache.Cache.Fetch)
}

type options struct {
	trimFrequency time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.DurationVar(
		&options.trimFrequency,
		"span-cache-local-cleanup-frequency",
		time.Minute*30,
		"frequency to collect garbage from span cache",
	)
}

func (options *options) EnableFlag() *bool { return nil }

// A basic implementation of Cache in memory that satisfies its requirements.
// Used for unit testing and installations who don't want to setup an external database.
type Local struct {
	manager.MuxImplBase
	options options

	Logger      logrus.FieldLogger
	Clock       clock.Clock
	entriesLock sync.Mutex
	entries     map[string]*localEntry
}

func NewMockLocal(clock clock.Clock) spancache.Cache {
	return &Local{
		Logger:  logrus.New(),
		Clock:   clock,
		entries: map[string]*localEntry{},
	}
}

func (_ *Local) MuxImplName() (name string, isDefault bool) { return "local", true }

func (cache *Local) Options() manager.Options { return &cache.options }

func (cache *Local) Init() error { return nil }

func (cache *Local) Start(ctx context.Context) error {
	go func() {
		defer shutdown.RecoverPanic(cache.Logger)
		for {
			select {
			case <-cache.Clock.After(cache.options.trimFrequency):
				cache.Trim()
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (cache *Local) Close(ctx context.Context) error { return nil }

func (cache *Local) Trim() {
	cache.entriesLock.Lock()
	defer cache.entriesLock.Unlock()

	for key, ent := range cache.entries {
		if ent.expired(cache.Clock) {
			delete(cache.entries, key)
		}
	}
}

type localEntry struct {
	lock     sync.RWMutex
	value    []byte    // note: do not write to the array behind the slice; always clone if write is needed
	creation time.Time // for debug only
	expiry   time.Time
	uid      spancache.Uid
}

func (entry *localEntry) expired(clock clock.Clock) bool {
	entry.lock.RLock()
	defer entry.lock.RUnlock()
	return entry.expiry.Before(clock.Now())
}

func (cache *Local) getEntry(key string) *localEntry {
	cache.entriesLock.Lock()
	defer cache.entriesLock.Unlock()

	return cache.entries[key]
}

func (cache *Local) getOrInsertEntry(key string, expiry time.Time) (*localEntry, bool, error) {
	cache.entriesLock.Lock()
	defer cache.entriesLock.Unlock()

	isNew := false
	if ent := cache.entries[key]; ent == nil {
		uid, err := randUid()
		if err != nil {
			return nil, false, err
		}

		cache.entries[key] = &localEntry{creation: cache.Clock.Now(), expiry: expiry, uid: uid}
		isNew = true
	}

	return cache.entries[key], isNew, nil
}

func (cache *Local) FetchOrReserve(ctx context.Context, key string, ttl time.Duration) (*spancache.Entry, error) {
	expiry := cache.Clock.Now().Add(ttl)
	ent, isInsert, err := cache.getOrInsertEntry(key, expiry)
	if err != nil {
		return nil, fmt.Errorf("create cache entry: %w", err)
	}

	ent.lock.RLock()
	defer ent.lock.RUnlock()

	// already initialized
	if ent.value != nil {
		return &spancache.Entry{
			Value:   ent.value,
			LastUid: ent.uid,
		}, nil
	}

	if !isInsert {
		return nil, fmt.Errorf("%w for %s", spancache.ErrAlreadyReserved, cache.Clock.Now().Sub(ent.creation))
	}

	return &spancache.Entry{
		Value:   nil,
		LastUid: ent.uid,
	}, nil
}

func (cache *Local) Fetch(ctx context.Context, key string) (*spancache.Entry, error) {
	ent := cache.getEntry(key)

	if ent == nil {
		return nil, nil
	}

	ent.lock.RLock()
	defer ent.lock.RUnlock()

	return &spancache.Entry{
		Value:   ent.value,
		LastUid: ent.uid,
	}, nil
}

func (cache *Local) SetReserved(ctx context.Context, key string, value []byte, lastUid spancache.Uid, ttl time.Duration) error {
	ent := cache.getEntry(key)

	if ent == nil {
		return spancache.ErrInvalidKey
	}

	ent.lock.Lock()
	defer ent.lock.Unlock()

	if ent.value != nil || ent.expiry.Before(cache.Clock.Now()) {
		return spancache.ErrInvalidKey
	}

	if !bytes.Equal(ent.uid, lastUid) {
		return spancache.ErrUidMismatch
	}

	newUid, err := randUid()
	if err != nil {
		return err
	}

	ent.expiry = cache.Clock.Now().Add(ttl)
	ent.value = value
	ent.uid = newUid // new version

	return nil
}

func randUid() (spancache.Uid, error) {
	entropy := make([]byte, 16)
	_, err := rand.Read(entropy)
	if err != nil {
		return nil, fmt.Errorf("error getting random value: %w", err)
	}

	return spancache.Uid(entropy), nil
}
