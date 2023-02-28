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
	"math"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

// UnboundedQueue is an unbounded channel.
type UnboundedQueue struct {
	deque    *Deque
	notifier chan<- struct{}
	receiver <-chan interface{}
	stopCh   chan<- struct{}
}

// Creates a new UnboundedQueue with the specified initial capacity.
func NewUnboundedQueue(initialCapacity int) *UnboundedQueue {
	deque := NewDeque(initialCapacity)
	notifier := make(chan struct{}, 1)
	receiver := make(chan interface{})
	stopCh := make(chan struct{}, 1)

	go receiverLoop(deque, notifier, receiver, stopCh)

	return &UnboundedQueue{
		deque:    deque,
		notifier: notifier,
		receiver: receiver,
		stopCh:   stopCh,
	}
}

// Receiver returns the channel that can be used for receiving from this UnboundedQueue.
func (uq *UnboundedQueue) Receiver() <-chan interface{} {
	return uq.receiver
}

func (uq *UnboundedQueue) Close() {
	uq.stopCh <- struct{}{}
}

func (uq *UnboundedQueue) Length() int {
	return uq.deque.Len()
}

func (uq *UnboundedQueue) InitMetricLoop(metricsClient metrics.Client, metricName string, tags interface{}) {
	metricsClient.NewMonitor(metricName, tags, func() int64 { return int64(uq.deque.GetAndResetLength()) })
}

// Sends an item to the queue.
//
// Since the channel capacity is unbounded, send operations always succeed and never block.
func (uq *UnboundedQueue) Send(obj interface{}) {
	uq.deque.PushBack(obj)

	select {
	case uq.notifier <- struct{}{}:
		// notified the receiver goroutine
	default:
		// no goroutine is receiving, but notifier is already nonempty anyway
	}
}

func receiverLoop(deque *Deque, notifier <-chan struct{}, receiver chan<- interface{}, stopCh <-chan struct{}) {
	defer shutdown.RecoverPanic(logrus.New())

	for {
		item := deque.PopFront()

		if item != nil {
			select {
			case <-stopCh:
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
		case <-stopCh:
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
type Deque struct {
	data      []interface{}
	start     int
	end       int
	maxLength int
	lock      sync.Mutex
}

// NewDeque constructs a new deque with the specified initial capacity.
//
// deque capacity is doubled when the length reaches the capacity,
// i.e. a deque can never be full.
func NewDeque(initialCapacity int) *Deque {
	return &Deque{
		data:  make([]interface{}, initialCapacity),
		start: 0,
		end:   0,
	}
}

// PushBack pushes an object to the end of the queue.
//
// This method has amortized O(1) time complexity and expands the capacity on demand.
func (q *Deque) PushBack(obj interface{}) {
	q.lock.Lock()
	defer q.lock.Unlock()

	q.LockedPushBack(obj)
}

func (q *Deque) LockedPushBack(obj interface{}) {
	q.data[q.end] = obj
	q.end = (q.end + 1) % len(q.data)

	if q.end == q.start {
		// when deque is unlocked, q.end == q.start implies empty deque.
		// therefore, we need to expand it now.

		newData := make([]interface{}, len(q.data)*2)

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

func (q *Deque) GetAndResetLength() int {
	q.lock.Lock()
	defer q.lock.Unlock()

	maxLength := q.maxLength
	q.maxLength = 0
	return maxLength
}

// PopFront pops an object from the start of the queue.
//
// This method has O(1) time complexity.
func (q *Deque) PopFront() interface{} {
	q.lock.Lock()
	defer q.lock.Unlock()

	return q.LockedPopFront()
}

func (q *Deque) LockedPopFront() interface{} {
	if q.start == q.end {
		// we assume the deque is in sound state,
		// i.e. q.start == q.end implies empty queue.
		return nil
	}

	ret := q.data[q.start]

	// we need to unset this pointer to allow GC
	q.data[q.start] = nil

	q.start = (q.start + 1) % len(q.data)

	return ret
}

func (q *Deque) LockedPeekFront() interface{} {
	if q.start == q.end {
		return nil
	}

	return q.data[q.start]
}

func (q *Deque) Len() int {
	q.lock.Lock()
	defer q.lock.Unlock()

	return q.lockedLen()
}

func (q *Deque) lockedLen() int {
	delta := q.end - q.start
	if delta < 0 {
		delta += len(q.data)
	}
	return delta
}

func (q *Deque) Cap() int {
	return len(q.data)
}

// Compact reallocates the buffer with capacity = Len * ratio.
func (q *Deque) Compact(ratio float64) {
	length := q.Len()

	q.lock.Lock()
	defer q.lock.Unlock()

	capacity := int(math.Ceil(float64(length) * ratio))
	data := make([]interface{}, capacity)

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
