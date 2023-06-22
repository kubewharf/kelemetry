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

package semaphore_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/kelemetry/pkg/util/semaphore"
)

var ErrTest = errors.New("test error")

func TestLimit(t *testing.T) {
	assert := assert.New(t)
	sem := semaphore.New(2)

	run := make(chan struct{}, 16)
	var counter atomic.Int32
	stopCh := make(chan struct{})

	var completions int

	for i := 0; i < 3; i++ {
		sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
			run <- struct{}{}
			currentCount := counter.Add(1)
			assert.LessOrEqual(currentCount, int32(2))

			<-stopCh

			return func() error {
				completions += 1
				return nil
			}, nil
		})
	}

	runOkCh := make(chan struct{})

	go func() {
		err := sem.Run(context.Background())
		assert.NoError(err)
		close(runOkCh)
	}()

	received := 0
	for i := 0; i < 2; i++ {
		<-run
		received += 1
	}

	time.Sleep(time.Millisecond)
	assert.Empty(run)

	counter.Store(0) // reset it to 0 so that third goroutine does not crash
	close(stopCh)

	<-runOkCh
	assert.Equal(3, completions)
}

func TestEmpty(t *testing.T) {
	assert := assert.New(t)
	sem := semaphore.New(2)
	err := sem.Run(context.Background())
	assert.NoError(err)
}

func TestErrorPreempt(t *testing.T) {
	assert := assert.New(t)
	sem := semaphore.New(4)

	othersStarted := make(chan struct{}, 16)

	scheduleOther := func() {
		sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
			othersStarted <- struct{}{}
			_, open := <-ctx.Done()
			assert.False(open, "context should be canceled")
			return nil, errors.New("other errors")
		})
	}

	sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
		for i := 0; i < 3; i++ {
			<-othersStarted
		}
		for i := 0; i < 2; i++ {
			// schedule new tasks after error source starts
			// to avoid all waiting tasks getting created first
			scheduleOther()
		}
		return nil, ErrTest
	})

	for i := 0; i < 3; i++ {
		scheduleOther()
	}

	err := sem.Run(context.Background())
	assert.ErrorIs(err, ErrTest)
}

func TestParentContextCancel(t *testing.T) {
	assert := assert.New(t)
	sem := semaphore.New(2)

	started := make(chan struct{}, 16)
	var completed atomic.Int32

	for i := 0; i < 3; i++ {
		sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
			started <- struct{}{}
			_, open := <-ctx.Done()
			time.Sleep(time.Millisecond)
			assert.False(open, "context should be canceled")
			completed.Add(1)
			return nil, ErrTest
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 2; i++ {
			<-started
		}

		cancel()
	}()

	err := sem.Run(ctx)
	assert.LessOrEqual(completed.Load(), int32(2), "should not execute new tasks after cancelation")
	assert.True(errors.Is(err, ErrTest) || errors.Is(err, ctx.Err()), "error is either ContextCanceled or TestErr")
}

func TestRecursiveSchedule(t *testing.T) {
	assert := assert.New(t)
	sem := semaphore.New(3)

	completions := 0

	var f func(depth int) semaphore.Publish
	f = func(depth int) semaphore.Publish {
		if depth < 4 {
			for i := 0; i < 2; i++ {
				sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
					return f(depth + 1), nil
				})
			}
		}
		return func() error {
			completions += 1
			return nil
		}
	}

	sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
		return f(0), nil
	})

	err := sem.Run(context.Background())
	assert.NoError(err)
	assert.Equal(31, completions)
}
