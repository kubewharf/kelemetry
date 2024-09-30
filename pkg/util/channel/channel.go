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

package channel

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

type Queue[T any] interface {
	Send(obj T)
	Receiver() <-chan T

	Length() int
	GetAndResetLength() int

	Close()
}

type BoundedQueue[T any] chan T

func (ch BoundedQueue[T]) Length() int {
	return len(ch)
}

func (ch BoundedQueue[T]) GetAndResetLength() int {
	return len(ch) // for simplicity we just return the channel length
}

func (ch BoundedQueue[T]) Receiver() <-chan T {
	return ch
}

func (ch BoundedQueue[T]) Send(obj T) {
	ch <- obj
}

func (ch BoundedQueue[T]) Close() {
	close(ch)
}

func _[T any](uq BoundedQueue[T]) Queue[T] {
	return uq
}

// UnboundedQueue is an unbounded channel.
type UnboundedQueue[T any] struct {
	deque    *Deque[T]
	notifier chan<- struct{}
	receiver <-chan T
	closeFn  context.CancelFunc
}

func _[T any](uq *UnboundedQueue[T]) Queue[T] {
	return uq
}

// Creates a new UnboundedQueue with the specified initial capacity.
func NewUnboundedQueue[T any](initialCapacity int) *UnboundedQueue[T] {
	deque := NewDeque[T](initialCapacity)
	notifier := make(chan struct{}, 1)
	receiver := make(chan T)

	receiverCtx, cancelFunc := context.WithCancel(context.Background())

	go receiverLoop(receiverCtx, deque, notifier, receiver)

	return &UnboundedQueue[T]{
		deque:    deque,
		notifier: notifier,
		receiver: receiver,
		closeFn:  cancelFunc,
	}
}

// Receiver returns the channel that can be used for receiving from this UnboundedQueue.
func (uq *UnboundedQueue[T]) Receiver() <-chan T {
	return uq.receiver
}

func (uq *UnboundedQueue[T]) Length() int {
	return uq.deque.Len()
}

func (uq *UnboundedQueue[T]) Close() {
	uq.closeFn()
}

func (uq *UnboundedQueue[T]) GetAndResetLength() int {
	return uq.deque.GetAndResetLength()
}

func InitMetricLoop[T any, TagsT metrics.Tags](uq Queue[T], metricsClient metrics.Client, tags TagsT) {
	metrics.NewMonitor(metricsClient, tags, func() float64 { return float64(uq.Length()) })
}

// Sends an item to the queue.
//
// Since the channel capacity is unbounded, send operations always succeed and never block.
func (uq *UnboundedQueue[T]) Send(obj T) {
	uq.deque.PushBack(obj)

	select {
	case uq.notifier <- struct{}{}:
		// notified the receiver goroutine
	default:
		// no goroutine is receiving, but notifier is already nonempty anyway
	}
}

func receiverLoop[T any](ctx context.Context, deque *Deque[T], notifier <-chan struct{}, receiver chan<- T) {
	defer shutdown.RecoverPanic(logrus.New())

	for {
		item, hasItem := deque.PopFront()

		if hasItem {
			select {
			case <-ctx.Done():
				// queue stopped, close the receiver
				close(receiver)
				return
			case receiver <- item:
				// since notifier only has one buffer signal, there may be multiple actual items.
				// therefore, we call `popFront` again without waiting for the notifier.
				continue
			}
		}

		// item is nil, we have to wait
		// since there is one buffer signal in notifier,
		// even if a new item is sent on this line,
		// notifier will still receive something.

		select {
		case <-ctx.Done():
			// queue stopped, close the receiver
			close(receiver)
			return
		case <-notifier:
			// deque has been updated
		}
	}
}

// Deque is a typical double-ended queue implemented through a ring buffer.
//
// All operations on Deque locks on its own mutex, so all operations are concurrency-safe.
type Deque[T any] struct {
	data      []T
	start     int
	end       int
	maxLength int
	lock      sync.Mutex
}

// NewDeque constructs a new deque with the specified initial capacity.
//
// deque capacity is doubled when the length reaches the capacity,
// i.e. a deque can never be full.
func NewDeque[T any](initialCapacity int) *Deque[T] {
	return &Deque[T]{
		data:  make([]T, initialCapacity),
		start: 0,
		end:   0,
	}
}

// Returns all valid data in two slices.
func (q *Deque[T]) LockedGetAll() [2][]T {
	if q.end < q.start {
		return [2][]T{
			q.data[q.start:],
			q.data[:q.end],
		}
	}

	return [2][]T{
		q.data[q.start:q.end],
		nil,
	}
}

// PushBack pushes an object to the end of the queue.
//
// This method has amortized O(1) time complexity and expands the capacity on demand.
func (q *Deque[T]) PushBack(obj T) {
	q.lock.Lock()
	defer q.lock.Unlock()

	q.LockedPushBack(obj)
}

func (q *Deque[T]) LockedPushBack(obj T) {
	q.data[q.end] = obj
	q.end = (q.end + 1) % len(q.data)

	if q.end == q.start {
		// when deque is unlocked, q.end == q.start implies empty deque.
		// therefore, we need to expand it now.

		newData := make([]T, len(q.data)*2)

		for i := q.start; i < len(q.data); i++ {
			newData[i-q.start] = q.data[i]
		}

		leftOffset := len(q.data) - q.start
		for i := 0; i < q.end; i++ {
			newData[leftOffset+i] = q.data[i]
		}

		q.start = 0
		q.end = len(q.data)

		q.data = newData
	}

	length := q.lockedLen()
	if length > q.maxLength {
		q.maxLength = length
	}
}

func (q *Deque[T]) GetAndResetLength() int {
	q.lock.Lock()
	defer q.lock.Unlock()

	maxLength := q.maxLength
	q.maxLength = 0
	return maxLength
}

// PopFront pops an object from the start of the queue.
//
// This method has O(1) time complexity.
func (q *Deque[T]) PopFront() (T, bool) {
	q.lock.Lock()
	defer q.lock.Unlock()

	return q.LockedPopFront()
}

func (q *Deque[T]) LockedPopFront() (T, bool) {
	if q.start == q.end {
		// we assume the deque is in sound state,
		// i.e. q.start == q.end implies empty queue.
		return zero[T](), false
	}

	ret := q.data[q.start]

	// we need to unset this pointer to allow GC
	q.data[q.start] = zero[T]()

	q.start = (q.start + 1) % len(q.data)

	return ret, true
}

func (q *Deque[T]) LockedPeekFront() (T, bool) {
	if q.start == q.end {
		return zero[T](), false
	}

	return q.data[q.start], true
}

func (q *Deque[T]) Len() int {
	q.lock.Lock()
	defer q.lock.Unlock()

	return q.lockedLen()
}

func (q *Deque[T]) lockedLen() int {
	delta := q.end - q.start
	if delta < 0 {
		delta += len(q.data)
	}
	return delta
}

func (q *Deque[T]) Cap() int {
	return len(q.data)
}

// Compact reallocates the buffer with capacity = Len * ratio.
func (q *Deque[T]) Compact(ratio float64) {
	length := q.Len()

	q.lock.Lock()
	defer q.lock.Unlock()

	capacity := int(math.Ceil(float64(length) * ratio))
	data := make([]T, capacity)

	if q.end < q.start {
		firstLength := len(q.data) - q.start
		copy(data[:firstLength], q.data[q.start:])
		copy(data[firstLength:], q.data[:q.end])
	} else {
		copy(data, q.data[q.start:q.end])
		data = q.data[q.start:q.end]
	}

	q.data = data
	q.start = 0
	q.end = length
}

func zero[T any]() (zero T) { return }

func NoisyWaitChannelClose(ctx context.Context, ch <-chan struct{}, period time.Duration, doLog func(string)) {
	for {
		select {
		case <-ch:
			return
		case <-time.After(period):
			doLog("waiting for channel close...")
		case <-ctx.Done():
			doLog("base context closed")
			return
		}
	}
}
