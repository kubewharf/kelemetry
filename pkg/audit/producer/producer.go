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

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

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
	enable           bool
	workerCount      int
	partitionKeyType partitionKeyType
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "audit-producer-enable", false, "enable audit producer")
	fs.IntVar(&options.workerCount, "audit-producer-worker-count", 1, "audit producer worker count")

	options.partitionKeyType = partitionKeyTypeAuditId
	fs.Var(
		&options.partitionKeyType,
		"audit-producer-partition-key-type",
		fmt.Sprintf(
			"how to partition audit messages in the message queue. Possible values are %q.",
			[]string{"cluster", "object", "audit-id"},
		),
	)
}

func (options *options) EnableFlag() *bool { return &options.enable }

type partitionKeyType uint32

const (
	partitionKeyTypeCluster partitionKeyType = iota
	partitionKeyTypeObject
	partitionKeyTypeAuditId
)

func (ty *partitionKeyType) String() string {
	switch *ty {
	case partitionKeyTypeCluster:
		return "cluster"
	case partitionKeyTypeObject:
		return "object"
	case partitionKeyTypeAuditId:
		return "audit-id"
	default:
		panic("invalid value")
	}
}

func (ty *partitionKeyType) Set(input string) error {
	switch input {
	case "cluster":
		*ty = partitionKeyTypeCluster
	case "object":
		*ty = partitionKeyTypeObject
	case "audit-id":
		*ty = partitionKeyTypeAuditId
	default:
		return fmt.Errorf("unsupported partition key type")
	}

	return nil
}
func (ty *partitionKeyType) Type() string { return "partitionKeyType" }

type producer struct {
	options       options
	logger        logrus.FieldLogger
	clock         clock.Clock
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
	clock clock.Clock,
	webhook auditwebhook.Webhook,
	queue mq.Queue,
	metrics metrics.Client,
) *producer {
	return &producer{
		logger:  logger,
		clock:   clock,
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
	defer producer.produceMetric.DeferCount(producer.clock.Now(), &produceMetric{
		Cluster: message.Cluster,
	})

	partitionKey := []byte(nil)

	switch producer.options.partitionKeyType {
	case partitionKeyTypeCluster:
		partitionKey = []byte(fmt.Sprintf("%s/", message.Cluster))
	case partitionKeyTypeObject:
		partitionKey = []byte(fmt.Sprintf("%s/", message.Cluster))
		if message.Event.ObjectRef != nil {
			partitionKey = append(partitionKey, []byte(fmt.Sprintf(
				"%s/%s/%s/%s/%s/%s",
				message.Cluster,
				message.Event.ObjectRef.APIGroup,
				message.Event.ObjectRef.APIVersion,
				message.Event.ObjectRef.Resource,
				message.Event.ObjectRef.Namespace,
				message.Event.ObjectRef.Name,
			))...)
		}
	case partitionKeyTypeAuditId:
		partitionKey = []byte(fmt.Sprintf("%s/%s", message.Cluster, message.AuditID))
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
