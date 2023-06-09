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

package kelemetrixconsumer_test

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix/config"
	kelemetrixconsumer "github.com/kubewharf/kelemetry/pkg/kelemetrix/consumer"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

func TestMatching(t *testing.T) {
	stats := doTest(
		t,
		[]string{"k2"},
		[]config.TagFilter{
			{Tag: "k2", OneOf: []string{"v2"}},
		},
		[]config.QuantityFilter{
			{Quantity: "q2", Operator: config.QuantityOperatorGreater, Threshold: 4},
		},
	)

	assert := assert.New(t)
	assert.Equal(1.0, stats.Get("metric_name", map[string]string{"k2": "v2"}).GetIntUnsafe())
}

func TestMatchingWithNewTags(t *testing.T) {
	stats := doTest(
		t,
		[]string{"k1"},
		[]config.TagFilter{
			{Tag: "k2", OneOf: []string{"v2"}},
		},
		[]config.QuantityFilter{
			{Quantity: "q2", Operator: config.QuantityOperatorGreater, Threshold: 4},
		},
	)

	assert := assert.New(t)
	assert.Equal(1.0, stats.Get("metric_name", map[string]string{"k1": "v1"}).GetIntUnsafe())
}

func TestQuantityMismatch(t *testing.T) {
	stats := doTest(
		t,
		[]string{"k1"},
		[]config.TagFilter{
			{Tag: "k2", OneOf: []string{"v2"}},
		},
		[]config.QuantityFilter{
			{Quantity: "q2", Operator: config.QuantityOperatorLess, Threshold: 4},
		},
	)

	assert := assert.New(t)
	assert.Equal(0.0, stats.Get("metric_name", map[string]string{"k1": "v1"}).GetIntUnsafe())
}

func TestTagMismatch(t *testing.T) {
	stats := doTest(
		t,
		[]string{"k1"},
		[]config.TagFilter{
			{Tag: "k2", OneOf: []string{"v3"}},
		},
		[]config.QuantityFilter{
			{Quantity: "q2", Operator: config.QuantityOperatorGreater, Threshold: 4},
		},
	)

	assert := assert.New(t)
	assert.Equal(0.0, stats.Get("metric_name", map[string]string{"k1": "v1"}).GetIntUnsafe())
}

func doTest(
	t *testing.T,
	tags []string,
	tagFilters []config.TagFilter,
	quantityFilters []config.QuantityFilter,
) *metrics.Mock {
	t.Helper()

	assert := assert.New(t)

	logger := logrus.New()
	metrics, stats := metrics.NewMock(clocktesting.NewFakeClock(time.Time{}))

	config := &config.MockProvider{
		Config: &config.Config{
			Metrics: []config.Metric{
				{
					Name:            "metric_name",
					Quantifier:      "q1",
					Tags:            tags,
					TagFilters:      tagFilters,
					QuantityFilters: quantityFilters,
				},
			},
		},
	}

	registry := kelemetrix.NewRegistry()

	registry.AddTagProvider(constantTagProvider{key: "k1", value: "v1"})
	registry.AddTagProvider(constantTagProvider{key: "k2", value: "v2"})
	registry.AddTagProvider(constantTagProvider{key: "k3", value: "v3"})
	registry.AddQuantifier(constantQuantifier{name: "q1", ty: kelemetrix.MetricTypeCount, quantity: 1})
	registry.AddQuantifier(constantQuantifier{name: "q2", ty: kelemetrix.MetricTypeCount, quantity: 5})

	consumer := &kelemetrixconsumer.Consumer{
		Metrics:  metrics,
		Registry: registry,
		Config:   config,
	}

	assert.NoError(consumer.PrepareTagsQuantifiers())

	consumer.HandleMessage(logger, &audit.Message{
		Cluster:       "cluster",
		ApiserverAddr: "",
		Event: auditv1.Event{
			ObjectRef: &auditv1.ObjectReference{
				Name:       "name",
				Namespace:  "ns",
				APIGroup:   "group",
				APIVersion: "v1",
				Resource:   "resources",
			},
		},
	})

	return stats
}

type constantTagProvider struct {
	key   string
	value string
}

func (p constantTagProvider) TagNames() []string {
	return []string{p.key}
}

func (p constantTagProvider) ProvideValues(message *audit.Message, slice []string) {
	slice[0] = p.value
}

type constantQuantifier struct {
	name     string
	ty       kelemetrix.MetricType
	quantity float64
}

func (q constantQuantifier) Name() string                                    { return q.name }
func (q constantQuantifier) Type() kelemetrix.MetricType                     { return q.ty }
func (q constantQuantifier) Quantify(message *audit.Message) (float64, bool) { return q.quantity, true }
