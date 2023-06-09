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

package metricsprometheus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

func init() {
	manager.Global.ProvideMuxImpl("metrics/prom", manager.Ptr(&prom{}), func(metrics.Client) {})
}

type options struct {
	address string
	port    uint16

	readTimeout       time.Duration
	readHeaderTimeout time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.address, "metrics-address", "", "HTTP server bind address")
	fs.Uint16Var(&options.port, "metrics-port", 9090, "HTTP server port")
	fs.DurationVar(&options.readTimeout, "metrics-read-timeout", 0, "HTTP server timeout for reading requests")
	fs.DurationVar(
		&options.readHeaderTimeout,
		"metrics-read-header-timeout",
		0,
		"HTTP server timeout for reading request headers",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type prom struct {
	manager.MuxImplBase
	Logger  logrus.FieldLogger
	Clock   clock.Clock
	options options

	registry *prometheus.Registry
	http     *http.Server
}

var _ metrics.Impl = &prom{}

func (_ *prom) MuxImplName() (name string, isDefault bool) { return "prom", false }

func (prom *prom) Options() manager.Options { return &prom.options }

func (prom *prom) Init() error {
	prom.registry = prometheus.NewRegistry()
	prom.http = &http.Server{
		Addr:              net.JoinHostPort(prom.options.address, fmt.Sprint(prom.options.port)),
		ReadHeaderTimeout: prom.options.readHeaderTimeout,
		ReadTimeout:       prom.options.readTimeout,
		Handler:           promhttp.HandlerFor(prom.registry, promhttp.HandlerOpts{Registry: prom.registry}),
	}
	return nil
}

func (prom *prom) Start(ctx context.Context) error {
	go func() {
		if err := prom.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			prom.Logger.WithError(err).Error()
		}
	}()

	return nil
}

func (prom *prom) Close(ctx context.Context) error { return nil }

func (prom *prom) New(name string, tagNames []string) metrics.MetricImpl {
	factory := promauto.With(prom.registry)

	return &metric{factory: factory, clock: prom.Clock, name: name, tagNames: tagNames}
}

type metric struct {
	factory  promauto.Factory
	clock    clock.Clock
	name     string
	tagNames []string

	counterOnce, histogramOnce, summaryOnce, gaugeOnce sync.Once

	counterVec   *prometheus.CounterVec
	histogramVec *prometheus.HistogramVec
	summaryVec   *prometheus.SummaryVec
	gaugeVec     *prometheus.GaugeVec
}

func (metric *metric) Count(value float64, tags []string) {
	metric.counterOnce.Do(func() {
		metric.counterVec = metric.factory.NewCounterVec(prometheus.CounterOpts{Name: metric.name + "_count"}, metric.tagNames)
	})

	metric.counterVec.WithLabelValues(tags...).Add(value)
}

func (metric *metric) Histogram(value float64, tags []string) {
	metric.histogramOnce.Do(func() {
		metric.histogramVec = metric.factory.NewHistogramVec(prometheus.HistogramOpts{Name: metric.name + "_histogram"}, metric.tagNames)
	})

	metric.histogramVec.WithLabelValues(tags...).Observe(value)
}

func (metric *metric) Summary(value float64, tags []string) {
	metric.summaryOnce.Do(func() {
		metric.summaryVec = metric.factory.NewSummaryVec(prometheus.SummaryOpts{Name: metric.name + "_summary"}, metric.tagNames)
	})

	metric.summaryVec.WithLabelValues(tags...).Observe(value)
}

func (metric *metric) Gauge(value float64, tags []string) {
	metric.gaugeOnce.Do(func() {
		metric.gaugeVec = metric.factory.NewGaugeVec(prometheus.GaugeOpts{Name: metric.name + "_gauge"}, metric.tagNames)
	})

	metric.gaugeVec.WithLabelValues(tags...).Add(value)
}

func (metric *metric) Defer(time time.Time, tags []string) {
	metric.Histogram(float64(metric.clock.Since(time).Nanoseconds()), tags)
}
