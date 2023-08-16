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

// Provides a short-lived task pool that runs fallible tasks and publishes results on the same goroutine.
// Similar to golang.org/x/sync/semaphore, but this supports scheduling tasks recursively (schedule while Wait is being called).
package semaphore

import (
	"context"
	"sync"
	"sync/atomic"
)

// Publish is a function that runs on the goroutine that calls `Run`.
type (
	Publish = func() error
	Task    = func(ctx context.Context) (Publish, error)
)

type Semaphore struct {
	// Channel for limiting the number of concurrent tasks.
	// Tasks can only be run when the channel is not saturated.
	// Send before run, receive after run.
	//
	// limitCh can be nil if the semaphore is unbounded (limit == -1).
	limitCh chan struct{}

	// Done by worker goroutines to indicate that a scheduled task is complete.
	doneWg sync.WaitGroup

	// Sent from worker goroutines, received by main goroutine to publish the result.
	publishCh chan Publish

	// Sent from worker goroutines, received by main goroutine to return the error to caller.
	errCh chan error

	// Oneshot channel closed by the main goroutine to indicate to workers that the semaphore has been invalidated.
	errNotifyCh chan struct{}

	// Oneshot channel closed by the main goroutine to indicate that ctx is ready.
	initCtxCh chan struct{}
	//nolint:containedctx // this context is used for async passing
	ctx context.Context
}

func NewUnbounded() *Semaphore { return New(-1) }

func New(limit int) *Semaphore {
	var limitCh chan struct{}
	if limit >= 0 {
		limitCh = make(chan struct{}, limit)
	}

	sem := &Semaphore{
		limitCh:     limitCh,
		errNotifyCh: make(chan struct{}),
		publishCh:   make(chan Publish),
		errCh:       make(chan error, 1), // only first error needs to get sent, always TrySend here
		initCtxCh:   make(chan struct{}),
	}
	return sem
}

func (sem *Semaphore) Schedule(task Task) {
	sem.doneWg.Add(1)
	go func() {
		defer sem.doneWg.Done()

		<-sem.initCtxCh
		ctx := sem.ctx

		if sem.limitCh != nil {
			// wait for semaphore acquisition

			select {
			case <-sem.errNotifyCh:
				return
			case sem.limitCh <- struct{}{}:
			}

			defer func() { <-sem.limitCh }()
		}

		select {
		case <-sem.errNotifyCh:
			// The previous select with limitCh has random, check again to reduce unnecessary runs
			return
		default:
		}

		publish, err := task(ctx)
		if err != nil {
			select {
			case sem.errCh <- err:
			default:
				// no need to publish if there is another error in the channel
			}
		} else {
			if publish != nil {
				select {
				case sem.publishCh <- publish:
					// publishCh has zero capacity, so this case blocks until the main goroutine selects the publishCh case,
					// so we can ensure that publishCh is received before calling sem.doneWg.Done()
				case <-sem.errNotifyCh:
					// no need to publish if the caller received error
				}
			}
		}
	}()
}

func (sem *Semaphore) Run(ctx context.Context) error {
	ctx, cancelFunc := context.WithCancel(ctx)
	defer cancelFunc()

	sem.ctx = ctx
	close(sem.initCtxCh)

	doneCh := make(chan struct{})

	go func() {
		sem.doneWg.Wait()
		close(doneCh)
	}()

	var stopped atomic.Bool
	defer stopped.Store(true)

	for {
		select {
		case <-doneCh:
			// publishCh must be fully sent before calling sem.doneWg.Done()
			return nil
		case publish := <-sem.publishCh:
			err := publish()
			if err != nil {
				close(sem.errNotifyCh)
				return err
			}
		case err := <-sem.errCh:
			close(sem.errNotifyCh)
			return err
		case <-ctx.Done():
			close(sem.errNotifyCh)
			return ctx.Err()
		}
	}
}
