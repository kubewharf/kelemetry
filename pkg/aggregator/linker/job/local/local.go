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

package linkerjoblocal

import (
	"context"
	"sync"

	"github.com/sirupsen/logrus"

	linkjob "github.com/kubewharf/kelemetry/pkg/aggregator/linker/job"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/channel"
)

func init() {
	manager.Global.Provide("linker-job-local-queue", manager.Ptr(&queue{}))
	manager.Global.ProvideMuxImpl("linker-job-local-publisher", manager.Ptr(&publisher{}), linkjob.Publisher.Publish)
	manager.Global.ProvideMuxImpl("linker-job-local-subscriber", manager.Ptr(&subscriber{}), linkjob.Subscriber.Subscribe)
}

type queue struct {
	Logger logrus.FieldLogger

	subscribersMu sync.RWMutex
	subscribers   map[*subscriberKey]*channel.UnboundedQueue[*linkjob.LinkJob]
}

type subscriberKey struct{}

func (queue *queue) Options() manager.Options { return &manager.NoOptions{} }
func (queue *queue) Init() error {
	queue.subscribers = map[*subscriberKey]*channel.UnboundedQueue[*linkjob.LinkJob]{}
	return nil
}
func (queue *queue) Start(ctx context.Context) error { return nil }
func (queue *queue) Close(ctx context.Context) error { return nil }

type publisher struct {
	Queue *queue
	manager.MuxImplBase
}

func (publisher *publisher) Options() manager.Options                   { return &manager.NoOptions{} }
func (publisher *publisher) Init() error                                { return nil }
func (publisher *publisher) Start(ctx context.Context) error            { return nil }
func (publisher *publisher) Close(ctx context.Context) error            { return nil }
func (publisher *publisher) MuxImplName() (name string, isDefault bool) { return "local", true }
func (publisher *publisher) Publish(job *linkjob.LinkJob) {
	publisher.Queue.subscribersMu.RLock()
	defer publisher.Queue.subscribersMu.RUnlock()

	for _, sub := range publisher.Queue.subscribers {
		sub.Send(job)
	}
}

type subscriber struct {
	Queue   *queue
	Metrics metrics.Client
	manager.MuxImplBase
}

type queueMetricTags struct {
	Name string
}

func (*queueMetricTags) MetricName() string { return "linker_local_worker_lag" }

func (subscriber *subscriber) Options() manager.Options                   { return &manager.NoOptions{} }
func (subscriber *subscriber) Init() error                                { return nil }
func (subscriber *subscriber) Start(ctx context.Context) error            { return nil }
func (subscriber *subscriber) Close(ctx context.Context) error            { return nil }
func (subscriber *subscriber) MuxImplName() (name string, isDefault bool) { return "local", true }
func (subscriber *subscriber) Subscribe(ctx context.Context, name string) <-chan *linkjob.LinkJob {
	queue := channel.NewUnboundedQueue[*linkjob.LinkJob](16)
	channel.InitMetricLoop(queue, subscriber.Metrics, &queueMetricTags{Name: name})

	subscriber.Queue.subscribersMu.Lock()
	defer subscriber.Queue.subscribersMu.Unlock()

	key := &subscriberKey{}
	subscriber.Queue.subscribers[key] = queue

	go func() {
		<-ctx.Done()

		subscriber.Queue.subscribersMu.Lock()
		defer subscriber.Queue.subscribersMu.Unlock()
		delete(subscriber.Queue.subscribers, key)
	}()

	return queue.Receiver()
}
