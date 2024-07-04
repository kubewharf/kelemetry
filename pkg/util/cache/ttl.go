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

package cache

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/util/channel"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

// TtlOnce is a cache where new insertions do not overwrite the old insertion.
type TtlOnce struct {
	ttl      time.Duration
	clock    clock.Clock
	wakeupCh chan struct{}

	lock         sync.RWMutex
	cleanupQueue *channel.Deque[cleanupEntry]
	data         map[string]any
}

type cleanupEntry struct {
	key    string
	expiry time.Time
}

func NewTtlOnce(ttl time.Duration, clock clock.Clock) *TtlOnce {
	return &TtlOnce{
		ttl:          ttl,
		clock:        clock,
		wakeupCh:     make(chan struct{}),
		cleanupQueue: channel.NewDeque[cleanupEntry](16),
		data:         map[string]any{},
	}
}

func (cache *TtlOnce) Add(key string, value any) {
	cache.lock.Lock()
	defer cache.lock.Unlock()

	if _, exists := cache.data[key]; !exists {
		cache.data[key] = value
		expiry := cache.clock.Now().Add(cache.ttl)
		cache.cleanupQueue.LockedPushBack(cleanupEntry{key: key, expiry: expiry})
		select {
		case cache.wakeupCh <- struct{}{}:
		default:
		}
	}
}

func (cache *TtlOnce) Get(key string) (any, bool) {
	cache.lock.RLock()
	defer cache.lock.RUnlock()

	value, ok := cache.data[key]
	return value, ok
}

func (cache *TtlOnce) Size() int {
	cache.lock.RLock()
	defer cache.lock.RUnlock()

	return len(cache.data)
}

func (cache *TtlOnce) RunCleanupLoop(ctx context.Context, logger logrus.FieldLogger) {
	defer shutdown.RecoverPanic(logger)

	for {
		wakeup := cache.wakeupCh
		var nextExpiryCh <-chan time.Time
		if expiry, hasNext := cache.peekExpiry(); hasNext {
			nextExpiryCh = cache.clock.After(expiry.Sub(cache.clock.Now()) + time.Second) // +1s to mitigate race conditions
			wakeup = nil
		}

		select {
		case <-ctx.Done():
			return
		case <-wakeup:
			continue
		case <-nextExpiryCh:
			cache.doCleanup()
		}
	}
}

func (cache *TtlOnce) doCleanup() {
	cache.lock.Lock()
	defer cache.lock.Unlock()

	for {
		if entry, hasEntry := cache.cleanupQueue.LockedPeekFront(); hasEntry {
			if entry.expiry.Before(cache.clock.Now()) {
				cache.cleanupQueue.LockedPopFront()
				delete(cache.data, entry.key)
				continue
			}
		}

		break
	}
}

func (cache *TtlOnce) peekExpiry() (expiry time.Time, found bool) {
	cache.lock.RLock()
	defer cache.lock.RUnlock()

	if entry, hasEntry := cache.cleanupQueue.LockedPeekFront(); hasEntry {
		expiry = entry.expiry
		found = true
	}

	return expiry, found
}
