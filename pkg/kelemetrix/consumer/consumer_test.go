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
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/kubewharf/kelemetry/pkg/audit"
	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix/config"
	kelemetrixconsumer "github.com/kubewharf/kelemetry/pkg/kelemetrix/consumer"
	defaultquantities "github.com/kubewharf/kelemetry/pkg/kelemetrix/defaults/quantities"
	defaulttags "github.com/kubewharf/kelemetry/pkg/kelemetrix/defaults/tags"
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

func TestListTimeout(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	tagProviders := []kelemetrix.TagProvider{}
	defaulttags.Register(
		logger,
		func(tp kelemetrix.TagProvider) { tagProviders = append(tagProviders, tp) },
	)

	clock := clocktesting.NewFakeClock(time.Time{})

	stats := doTestWith(
		t,
		logger,
		clock,
		[]string{"username", "groupResource"},
		[]config.TagFilter{
			{Tag: "code", OneOf: []string{"200"}},
			{Tag: "verb", OneOf: []string{"list"}},
		},
		[]config.QuantityFilter{
			{Quantity: "request_latency_ratio", Operator: config.QuantityOperatorLess, Threshold: 1.0},
		},
		"request_count",
		tagProviders,
		[]kelemetrix.Quantifier{
			kelemetrix.NewBaseQuantifierForTest(defaultquantities.RequestCount{}),
			kelemetrix.NewBaseQuantifierForTest(defaultquantities.RequestLatencyRatio{
				Config: &k8sconfig.MockConfig{
					TargetClusterName: "tracetest",
					Clusters: map[string]*k8sconfig.Cluster{
						"tracetest": {
							DefaultRequestTimeout: time.Minute,
						},
					},
				},
			}),
		},
		[]*audit.Message{
			{
				Cluster:       "tracetest",
				ApiserverAddr: "0.0.0.0",
				Event: auditv1.Event{
					Stage:      auditv1.StageResponseComplete,
					RequestURI: "/api/v1/pods?resourceVersion=123",
					Verb:       "list",
					User: authenticationv1.UserInfo{
						Username: "proxy",
					},
					SourceIPs: []string{"0.0.0.0"},
					ImpersonatedUser: &authenticationv1.UserInfo{
						Username: "controller",
					},
					UserAgent: "controller",
					ObjectRef: &auditv1.ObjectReference{
						APIGroup:        "",
						APIVersion:      "v1",
						Resource:        "pods",
						ResourceVersion: "123",
					},
					RequestReceivedTimestamp: metav1.NewMicroTime(clock.Now().Add(-time.Second * 61)),
					StageTimestamp:           metav1.NewMicroTime(clock.Now()),
					ResponseStatus: &metav1.Status{
						Code: 200,
					},
				},
			},
		},
	)

	assert := assert.New(t)
	assert.Equal(1.0, stats.Get("metric_name", map[string]string{"username": "controller", "groupResource": "pods"}).GetIntUnsafe())
}

func doTest(
	t *testing.T,
	tags []string,
	tagFilters []config.TagFilter,
	quantityFilters []config.QuantityFilter,
) *metrics.Mock {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	return doTestWith(
		t, logger, clocktesting.NewFakeClock(time.Time{}),
		tags, tagFilters, quantityFilters,
		"q1",
		[]kelemetrix.TagProvider{
			constantTagProvider{key: "k1", value: "v1"},
			constantTagProvider{key: "k2", value: "v2"},
			constantTagProvider{key: "k3", value: "v3"},
		},
		[]kelemetrix.Quantifier{
			constantQuantifier{name: "q1", ty: kelemetrix.MetricTypeCount, quantity: 1},
			constantQuantifier{name: "q2", ty: kelemetrix.MetricTypeCount, quantity: 5},
		},
		[]*audit.Message{
			{
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
			},
		},
	)
}

func doTestWith(
	t *testing.T,
	logger logrus.FieldLogger,
	clock clock.Clock,
	tags []string,
	tagFilters []config.TagFilter,
	quantityFilters []config.QuantityFilter,
	mainQuantifierName string,
	tagProviders []kelemetrix.TagProvider,
	quantifiers []kelemetrix.Quantifier,
	auditMessages []*audit.Message,
) *metrics.Mock {
	t.Helper()
	assert := assert.New(t)

	metrics, stats := metrics.NewMock(clock)

	configProvider := &config.MockProvider{
		Config: &config.Config{
			Metrics: []config.Metric{
				{
					Name:            "metric_name",
					Quantifier:      mainQuantifierName,
					Tags:            tags,
					TagFilters:      tagFilters,
					QuantityFilters: quantityFilters,
				},
			},
		},
	}

	registry := kelemetrix.NewRegistry()

	for _, tagProvider := range tagProviders {
		registry.AddTagProvider(tagProvider)
	}
	for _, quantifier := range quantifiers {
		registry.AddQuantifier(quantifier)
	}

	consumer := &kelemetrixconsumer.Consumer{
		Metrics:  metrics,
		Registry: registry,
		Config:   configProvider,
	}
	{
		options := consumer.Options().(*kelemetrixconsumer.Options)
		options.LogTags = true
		options.LogQuantities = true
	}

	assert.NoError(consumer.PrepareTagsQuantifiers())

	for _, message := range auditMessages {
		consumer.HandleMessage(logger, message)
	}

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
