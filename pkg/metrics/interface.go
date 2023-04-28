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
	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

const MonitorPeriod = time.Second * 30

func init() {
	manager.Global.Provide("metrics", manager.Ptr[Client](&mux{
		Mux:        manager.NewMux("metrics", false),
		metricPool: map[string]AnyMetric{},
	}))
}

type Client interface {
	impl() *mux
}

type Metric[T Tags] struct {
	impl MetricImpl
}

func (*Metric[T]) ImplementsGenericUtilMarker() {}
func (*Metric[T]) UtilReqs() []reflect.Type {
	return []reflect.Type{
		reflectutil.TypeOf[*manager.UtilContext](),
		reflectutil.TypeOf[Client](),
	}
}

func (gm *Metric[T]) Construct(values []reflect.Value) error {
	ctx := values[0].Interface().(*manager.UtilContext)
	mux := values[1].Interface().(Client).impl()

	ctx.AddOnInit(func() error {
		initGenericMetric(gm, mux)
		return nil
	})

	return nil
}

func initGenericMetric[T Tags](gm *Metric[T], mux *mux) {
	name := reflectutil.ZeroOf[T]().MetricName()
	tagsTy := reflectutil.TypeOf[T]()
	if tagsTy.Kind() == reflect.Pointer {
		tagsTy = tagsTy.Elem()
	}

	if metric, exists := mux.metricPool[name]; exists {
		if metric.MetricType() != tagsTy {
			panic(fmt.Sprintf("cannot reuse metric name %q for types %v and %v", name, tagsTy, metric.MetricType()))
		}

		gm.impl = metric.MetricImpl()
		return
	}

	tagNames := []string{}

	for i := 0; i < tagsTy.NumField(); i++ {
		field := tagsTy.Field(i)
		tagName, exists := field.Tag.Lookup("metric")
		if !exists {
			tagName = field.Name
			tagName = strings.ToLower(tagName[0:1]) + tagName[1:]
		}
		tagNames = append(tagNames, tagName)
	}

	gm.impl = mux.Impl().(Impl).New(name, tagNames)
	mux.metricPool[name] = gm
}

func (*Metric[T]) MetricType() reflect.Type {
	ty := reflectutil.TypeOf[T]()
	if ty.Kind() == reflect.Pointer {
		ty = ty.Elem()
	}
	return ty
}
func (gm *Metric[T]) MetricImpl() MetricImpl { return gm.impl }

type AnyMetric interface {
	MetricType() reflect.Type
	MetricImpl() MetricImpl
}

type Tags interface {
	MetricName() string
}

func (metric *Metric[T]) getTagValues(tags T) []string {
	tagsValue := reflect.ValueOf(tags).Elem()

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

func (metric *Metric[T]) With(tags T) TaggedMetric {
	if metric.impl == nil {
		panic(fmt.Sprintf("metric %q was not initialized", tags.MetricName()))
	}

	tagValues := metric.getTagValues(tags)
	return TaggedMetric{
		impl:      metric.impl,
		tagValues: tagValues,
	}
}

func (metric *Metric[T]) DeferCount(start time.Time, tags T) {
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
	Logger     logrus.FieldLogger
	monitors   []func(context.Context)
	metricPool map[string]AnyMetric
}

func (mux *mux) impl() *mux { return mux }

func New[T Tags](client Client) *Metric[T] {
	gm := &Metric[T]{}
	initGenericMetric(gm, client.impl())
	return gm
}

func NewMonitor[T Tags](client Client, tags T, getter func() int64) {
	mux := client.impl()
	tagged := New[T](client).With(tags)
	loop := func(ctx context.Context) {
		defer shutdown.RecoverPanic(mux.Logger)

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
