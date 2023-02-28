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

package auditforward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/audit"
	auditwebhook "github.com/kubewharf/kelemetry/pkg/audit/webhook"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("audit-forward", New)
}

type options struct {
	forwardUrls map[string]string
	timeout     time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringToStringVar(&options.forwardUrls, "audit-forward-url", map[string]string{}, "forward audit messages to these URLs")
	fs.DurationVar(&options.timeout, "audit-forward-timeout", time.Second*30, "forward proxy client timeout")
}

func (options *options) EnableFlag() *bool {
	isForwardUrlsNonEmpty := len(options.forwardUrls) != 0
	return &isForwardUrlsNonEmpty
}

type comp struct {
	options options
	logger  logrus.FieldLogger
	webhook auditwebhook.Webhook
	metrics metrics.Client
	ctx     context.Context
	proxies []*proxy
	stopWg  sync.WaitGroup
}

func New(
	logger logrus.FieldLogger,
	webhook auditwebhook.Webhook,
	metrics metrics.Client,
) *comp {
	return &comp{
		logger:  logger,
		webhook: webhook,
		metrics: metrics,
	}
}

type proxyMetric struct {
	Upstream string
	Cluster  string
	Success  bool
}

func (comp *comp) Options() manager.Options {
	return &comp.options
}

func (comp *comp) Init(ctx context.Context) error {
	comp.ctx = ctx

	comp.proxies = make([]*proxy, 0, len(comp.options.forwardUrls))
	for upstreamName, url := range comp.options.forwardUrls {
		proxy := newProxy(
			comp.logger.WithField("url", url).WithField("upstream", upstreamName),
			comp.metrics.New("audit_forward_proxy", &proxyMetric{}),
			&comp.options,
			upstreamName,
			url,
			comp.webhook.AddRawSubscriber(fmt.Sprintf("audit-forward-%s-subscriber", upstreamName)),
		)
		comp.proxies = append(comp.proxies, proxy)
	}

	return nil
}

func (comp *comp) Start(stopCh <-chan struct{}) error {
	comp.stopWg.Add(len(comp.proxies))

	for _, proxy := range comp.proxies {
		go proxy.run(stopCh, &comp.stopWg)
	}

	return nil
}

func (comp *comp) Close() error {
	comp.stopWg.Wait()
	return nil
}

type proxy struct {
	logger       logrus.FieldLogger
	metric       metrics.Metric
	upstreamName string
	url          string
	subscriber   <-chan *audit.RawMessage
	client       http.Client
}

func newProxy(
	logger logrus.FieldLogger,
	metric metrics.Metric,
	options *options,
	upstreamName string,
	url string,
	subscriber <-chan *audit.RawMessage,
) *proxy {
	proxy := &proxy{
		logger:       logger,
		metric:       metric,
		upstreamName: upstreamName,
		url:          url,
		subscriber:   subscriber,
	}
	proxy.client.Timeout = options.timeout
	return proxy
}

func (proxy *proxy) run(stopCh <-chan struct{}, wg *sync.WaitGroup) {
	defer shutdown.RecoverPanic(proxy.logger)
	defer wg.Done()

	proxy.logger.Info("Starting audit proxy")

	for {
		select {
		case <-stopCh:
			return
		case message, chOpen := <-proxy.subscriber:
			if !chOpen {
				return
			}

			go func(message *audit.RawMessage) {
				defer shutdown.RecoverPanic(proxy.logger)

				if err := proxy.handleEvent(message); err != nil {
					proxy.logger.WithError(err).Error()
				}
			}(message)
		}
	}
}

func (proxy *proxy) handleEvent(message *audit.RawMessage) error {
	metric := &proxyMetric{
		Upstream: proxy.upstreamName,
		Cluster:  message.Cluster,
	}
	defer proxy.metric.DeferCount(time.Now(), metric)

	jsonBuf, err := json.Marshal(message.EventList)
	if err != nil {
		return fmt.Errorf("cannot reserialize EventList: %w", err)
	}

	url := strings.ReplaceAll(proxy.url, ":cluster", message.Cluster)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBuf))
	if err != nil {
		return fmt.Errorf("cannot create new request: %w", err)
	}

	req.Header.Add("x-forwarded-for", message.SourceAddr)
	req.Header.Add("x-kubetrace-webhook-forward", "from-kubetrace")
	req.Header.Add("content-type", "application/json")

	resp, err := proxy.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post error: %w", err)
	}

	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("cannot close post request: %w", err)
	}

	metric.Success = true

	return nil
}
