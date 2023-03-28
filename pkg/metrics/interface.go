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

package metrics

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/errors"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

const MonitorPeriod = time.Second * 30

func init() {
	manager.Global.Provide("metrics", newMux)
}

type Client interface {
	New(name string, tagsType any) Metric
	NewMonitor(name string, tags any, getter func() int64)
}

type Metric struct {
	impl    MetricImpl
	tagType reflect.Type
}

func (metric Metric) getTagValues(tags any) []string {
	tagsValue := reflect.ValueOf(tags).Elem()
	if tagsValue.Type() != metric.tagType {
		panic(fmt.Sprintf("metric tag type is inconsistent (%s != %s)", tagsValue.Type(), metric.tagType))
	}

	values := make([]string, tagsValue.NumField())
	for i := 0; i < tagsValue.NumField(); i++ {
		field := tagsValue.Field(i)

		value := fmt.Sprint(field)
		if field.Type() == reflect.TypeOf(func(LabeledError) {}).In(0) && !field.IsNil() {
			object := field.Interface().(LabeledError)

			items := []string{}
			for _, item := range errors.GetLabels(object, errorTypeTagLabel) {
				items = append(items, item.(string))
			}

			value = strings.Join(items, "/")
		}

		values[i] = value
	}
	return values
}

func (metric Metric) With(tags any) TaggedMetric {
	tagValues := metric.getTagValues(tags)
	return TaggedMetric{
		impl:      metric.impl,
		tagValues: tagValues,
	}
}

func (metric Metric) DeferCount(start time.Time, tags any) {
	tagged := metric.With(tags)
	tagged.Count(1)
	tagged.Defer(start)
}

type TaggedMetric struct {
	impl      MetricImpl
	tagValues []string
}

func (metric TaggedMetric) Count(value int64) {
	metric.impl.Count(value, metric.tagValues)
}

func (metric TaggedMetric) Histogram(value int64) {
	metric.impl.Histogram(value, metric.tagValues)
}

func (metric TaggedMetric) Gauge(value int64) {
	metric.impl.Gauge(value, metric.tagValues)
}

func (metric TaggedMetric) Defer(start time.Time) {
	metric.impl.Defer(start, metric.tagValues)
}

func (metric TaggedMetric) DeferCount(start time.Time) {
	metric.Count(1)
	metric.Defer(start)
}

type mux struct {
	*manager.Mux
	logger     logrus.FieldLogger
	monitors   []func(context.Context)
	metricPool map[string]Metric
}

func newMux(logger logrus.FieldLogger) Client {
	return &mux{
		Mux:        manager.NewMux("metrics", false),
		logger:     logger,
		metricPool: map[string]Metric{},
	}
}

func (mux *mux) New(name string, tagsType any) Metric {
	if metric, exists := mux.metricPool[name]; exists {
		return metric
	}

	tagNames := []string{}
	ty := reflect.TypeOf(tagsType).Elem()
	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		tagName, exists := field.Tag.Lookup("metric")
		if !exists {
			tagName = field.Name
			tagName = strings.ToLower(tagName[0:1]) + tagName[1:]
		}
		tagNames = append(tagNames, tagName)
	}

	impl := mux.Impl().(Impl).New(name, tagNames)
	metric := Metric{
		impl:    impl,
		tagType: ty,
	}
	mux.metricPool[name] = metric
	return metric
}

func (mux *mux) NewMonitor(name string, tags any, getter func() int64) {
	tagged := mux.New(name, tags).With(tags)
	loop := func(ctx context.Context) {
		defer shutdown.RecoverPanic(mux.logger)

		wait.UntilWithContext(ctx, func(ctx context.Context) {
			value := getter()
			tagged.Gauge(value)
		}, MonitorPeriod)
	}

	mux.monitors = append(mux.monitors, loop)
}

func (mux *mux) Start(ctx context.Context) error {
	if err := mux.Mux.Start(ctx); err != nil {
		return err
	}

	for _, monitor := range mux.monitors {
		go monitor(ctx)
	}
	mux.monitors = nil

	return nil
}

type Impl interface {
	New(name string, tagNames []string) MetricImpl
}

type MetricImpl interface {
	Count(value int64, tags []string)
	Histogram(value int64, tags []string)
	Gauge(value int64, tags []string)
	Defer(start time.Time, tags []string)
}

type LabeledError interface {
	error
}

const errorTypeTagLabel = "metrics/errorTypeTag"

func LabelError(err error, value string) LabeledError {
	return errors.Label(err, errorTypeTagLabel, value)
}

func MakeLabeledError(value string) LabeledError {
	return LabelError(errors.New(value), value)
}
