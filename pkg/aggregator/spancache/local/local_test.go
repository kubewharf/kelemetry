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

package local_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache"
	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache/local"
)

func TestLocalReserve(t *testing.T) {
	assert := assert.New(t)

	clock := clocktesting.NewFakeClock(time.Time{})
	cache := local.NewMockLocal(clock)

	entry1, err := cache.FetchOrReserve(context.Background(), "foo", 10)
	assert.Nil(err)
	assert.NotNil(entry1)
	assert.Nil(entry1.Value)

	clock.Step(5)

	_, err = cache.FetchOrReserve(context.Background(), "foo", 10)
	assert.NotNil(err)
	assert.ErrorIs(err, spancache.ErrAlreadyReserved)

	err = cache.SetReserved(context.Background(), "foo", []byte("bar"), spancache.Uid("no such uid"), 10)
	assert.NotNil(err)
	assert.ErrorIs(err, spancache.ErrUidMismatch)

	_, err = cache.FetchOrReserve(context.Background(), "foo", 10)
	assert.NotNil(err)
	assert.ErrorIs(err, spancache.ErrAlreadyReserved)

	err = cache.SetReserved(context.Background(), "foo", []byte("bar"), entry1.LastUid, 10)
	assert.Nil(err)
}

func TestLocalReserveExpired(t *testing.T) {
	assert := assert.New(t)

	clock := clocktesting.NewFakeClock(time.Time{})
	cache := local.NewMockLocal(clock)

	entry1, err := cache.FetchOrReserve(context.Background(), "foo", 10)
	assert.Nil(err)
	assert.NotNil(entry1)
	assert.Nil(entry1.Value)

	clock.Step(15)

	err = cache.SetReserved(context.Background(), "foo", []byte("bar"), entry1.LastUid, 10)
	assert.NotNil(err)
	assert.ErrorIs(err, spancache.ErrInvalidKey)
}
