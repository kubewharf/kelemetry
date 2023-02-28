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

package auditproducer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/audit/mq"
	auditwebhook "github.com/kubewharf/kelemetry/pkg/audit/webhook"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("audit-producer", New)
}

type options struct {
	enable      bool
	workerCount int
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "audit-producer-enable", false, "enable audit producer")
	fs.IntVar(&options.workerCount, "audit-producer-worker-count", 1, "audit producer worker count")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type producer struct {
	options       options
	logger        logrus.FieldLogger
	webhook       auditwebhook.Webhook
	queue         mq.Queue
	metrics       metrics.Client
	ctx           context.Context
	produceMetric metrics.Metric
	subscriber    <-chan *audit.Message
	producer      mq.Producer
}

type produceMetric struct {
	Cluster string
}

func New(
	logger logrus.FieldLogger,
	webhook auditwebhook.Webhook,
	queue mq.Queue,
	metrics metrics.Client,
) *producer {
	return &producer{
		logger:  logger,
		webhook: webhook,
		queue:   queue,
		metrics: metrics,
	}
}

func (producer *producer) Options() manager.Options {
	return &producer.options
}

func (producer *producer) Init(ctx context.Context) (err error) {
	producer.ctx = ctx
	producer.produceMetric = producer.metrics.New("audit_producer_produce", &produceMetric{})
	producer.subscriber = producer.webhook.AddSubscriber("audit-producer-subscriber")
	producer.producer, err = producer.queue.CreateProducer()
	if err != nil {
		return fmt.Errorf("cannot create mq producer for audit producer: %w", err)
	}
	return nil
}

func (producer *producer) Start(stopCh <-chan struct{}) error {
	for i := 0; i < producer.options.workerCount; i++ {
		go producer.workerLoop(stopCh, i)
	}

	return nil
}

func (producer *producer) workerLoop(stopCh <-chan struct{}, workerId int) {
	defer shutdown.RecoverPanic(producer.logger)

	for {
		select {
		case <-stopCh:
			return
		case event, chanOpen := <-producer.subscriber:
			if !chanOpen {
				return
			}

			logger := producer.logger.
				WithField("cluster", event.Cluster).
				WithField("auditId", event.Event.AuditID).
				WithField("workerId", workerId)

			if err := producer.handleEvent(event); err != nil {
				logger.WithError(err).Error("Error handling event")
			}
		}
	}
}

func (producer *producer) handleEvent(message *audit.Message) error {
	defer producer.produceMetric.DeferCount(time.Now(), &produceMetric{
		Cluster: message.Cluster,
	})

	partitionKey := []byte(nil)

	if message.Event.ObjectRef != nil {
		partitionKey = []byte(fmt.Sprintf(
			"%s/%s/%s/%s/%s/%s",
			message.Cluster,
			message.Event.ObjectRef.APIGroup,
			message.Event.ObjectRef.APIVersion,
			message.Event.ObjectRef.Resource,
			message.Event.ObjectRef.Namespace,
			message.Event.ObjectRef.Name,
		))
	}

	messageJson, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("cannot reserialize event JSON: %w", err)
	}

	err = producer.producer.Send(partitionKey, messageJson)
	if err != nil {
		return fmt.Errorf("cannot send event to message queue: %w", err)
	}

	return nil
}

func (producer *producer) Close() error {
	return nil
}
