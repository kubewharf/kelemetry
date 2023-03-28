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

package auditwebhook

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/audit/webhook/clustername"
	"github.com/kubewharf/kelemetry/pkg/http"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/channel"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("audit-webhook", New)
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "audit-webhook-enable", false, "enable audit webhook")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type Webhook interface {
	manager.Component

	// AddSubscriber registers a new event handler.
	// Handlers must consume all events, otherwise this would leak memory.
	AddSubscriber(name string) <-chan *audit.Message

	// AddRawSubscriber registers a new event list handler.
	// Handlers must consume all event lists, otherwise this would leak memory.
	AddRawSubscriber(name string) <-chan *audit.RawMessage
}

type webhook struct {
	options             options
	logger              logrus.FieldLogger
	clock               clock.Clock
	metrics             metrics.Client
	clusterNameResolver clustername.Resolver
	server              http.Server

	requestMetric  metrics.Metric
	sendRateMetric metrics.Metric
	subscribers    []namedQueue[*audit.Message]
	rawSubscribers []namedQueue[*audit.RawMessage]
}

type namedQueue[T any] struct {
	name  string
	queue *channel.UnboundedQueue[T]
}

type requestMetric struct {
	Cluster string
}

type queueMetricTags struct {
	Name string
}

func New(
	logger logrus.FieldLogger,
	clock clock.Clock,
	metrics metrics.Client,
	clusterNameResolver clustername.Resolver,
	server http.Server,
) Webhook {
	return &webhook{
		logger:              logger,
		clock:               clock,
		metrics:             metrics,
		clusterNameResolver: clusterNameResolver,
		server:              server,
	}
}

func (webhook *webhook) Options() manager.Options {
	return &webhook.options
}

func (webhook *webhook) Init(ctx context.Context) error {
	webhook.requestMetric = webhook.metrics.New("audit_webhook_request", &requestMetric{})
	webhook.sendRateMetric = webhook.metrics.New("audit_webhook_send_rate", &queueMetricTags{})

	webhook.server.Routes().POST("/audit", webhook.handleRequest)
	webhook.server.Routes().POST("/audit/:cluster", webhook.handleRequest)

	return nil
}

func (webhook *webhook) handleRequest(ctx *gin.Context) {
	logger := webhook.logger.WithField("source", ctx.Request.RemoteAddr)
	defer shutdown.RecoverPanic(logger)
	metric := &requestMetric{}
	defer webhook.requestMetric.DeferCount(webhook.clock.Now(), metric)

	if err := webhook.handle(ctx, logger, metric); err != nil {
		logger.WithError(err).Error()
	}
}

func (webhook *webhook) handle(ctx *gin.Context, logger logrus.FieldLogger, metric *requestMetric) error {
	cluster := ctx.Param("cluster")
	if cluster == "" {
		cluster = webhook.clusterNameResolver.Resolve(ctx.ClientIP())
	}

	metric.Cluster = cluster
	logger = logger.WithField("cluster", cluster)

	postData, err := ctx.GetRawData()
	if err != nil {
		return fmt.Errorf("cannot read POST data: %w", err)
	}

	eventList := &auditv1.EventList{}
	err = json.Unmarshal(postData, eventList)
	if err != nil {
		return fmt.Errorf("cannot decode POST data: %w", err)
	}

	logger.WithField("itemCount", len(eventList.Items)).Debug("Received EventList")

	// TODO optimize these loops to reduce memory usage

	rawMessage := &audit.RawMessage{
		Cluster:    cluster,
		SourceAddr: ctx.ClientIP(),
		EventList:  eventList,
	}
	for _, ch := range webhook.rawSubscribers {
		webhook.sendRateMetric.With(&queueMetricTags{Name: ch.name}).Count(1)
		ch.queue.Send(rawMessage)
	}

	for _, auditEvent := range eventList.Items {
		message := &audit.Message{
			Cluster:    cluster,
			SourceAddr: ctx.ClientIP(),
			Event:      auditEvent,
		}

		for _, ch := range webhook.subscribers {
			webhook.sendRateMetric.With(&queueMetricTags{Name: ch.name}).Count(1)
			ch.queue.Send(message)
		}
	}

	return nil
}

func (webhook *webhook) Start(ctx context.Context) error { return nil }

func (webhook *webhook) Close(ctx context.Context) error {
	for _, subscriber := range webhook.subscribers {
		subscriber.queue.Close()
	}

	for _, subscriber := range webhook.rawSubscribers {
		subscriber.queue.Close()
	}

	return nil
}

func (webhook *webhook) AddSubscriber(name string) <-chan *audit.Message {
	queue := channel.NewUnboundedQueue[*audit.Message](1)
	queue.InitMetricLoop(webhook.metrics, "audit_webhook_subscriber_lag", &queueMetricTags{Name: name})

	webhook.subscribers = append(webhook.subscribers, namedQueue[*audit.Message]{name: name, queue: queue})
	return queue.Receiver()
}

func (webhook *webhook) AddRawSubscriber(name string) <-chan *audit.RawMessage {
	queue := channel.NewUnboundedQueue[*audit.RawMessage](1)
	queue.InitMetricLoop(webhook.metrics, "audit_webhook_subscriber_lag", &queueMetricTags{Name: name})

	webhook.rawSubscribers = append(webhook.rawSubscribers, namedQueue[*audit.RawMessage]{name: name, queue: queue})
	return queue.Receiver()
}
