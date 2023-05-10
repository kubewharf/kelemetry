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

package kelemetrix

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/audit/mq"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

func init() {
	manager.Global.Provide("kelemetrix-consumer", manager.Ptr(&Consumer{}))
}

type Options struct {
	enable        bool
	consumerGroup string
	partitions    []int32
	logTags       bool
	logQuantities bool
}

func (options *Options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "kelemetrix-enable", false, "enable kelemetrix")
	fs.StringVar(&options.consumerGroup, "kelemetrix-consumer-group", "kelemetrix", "audit consumer group name")
	fs.Int32SliceVar(
		&options.partitions,
		"kelemetrix-consumer-partition",
		[]int32{0, 1, 2, 3, 4},
		"audit message queue partitions to consume",
	)

	fs.BoolVar(&options.logTags, "kelemetrix-debug-log-tags", false, "log metric tags for debugging")
	fs.BoolVar(&options.logQuantities, "kelemetrix-debug-log-quantities", false, "log metric quantities for debugging")
}

func (options *Options) EnableFlag() *bool { return &options.enable }

type Consumer struct {
	options  Options
	Mq       mq.Queue
	Metrics  metrics.Client
	Registry *kelemetrix.Registry
	Config   config.Provider

	consumers           []mq.Consumer
	requiredTags        sets.Set[string]
	requiredQuantifiers sets.Set[string]

	startupReady chan struct{}
	tagNames     []string
	tagProviders []indexedTagProvider
	quantifiers  []kelemetrix.Quantifier
	handlers     []metricHandler
}

var _ manager.Component = &Consumer{}

func (consumer *Consumer) Options() manager.Options { return &consumer.options }

func (consumer *Consumer) Init() error {
	consumer.startupReady = make(chan struct{})

	for _, partition := range consumer.options.partitions {
		group := mq.ConsumerGroup(consumer.options.consumerGroup)
		partition := mq.PartitionId(partition)
		mqConsumer, err := consumer.Mq.CreateConsumer(
			group,
			partition,
			func(ctx context.Context, fieldLogger logrus.FieldLogger, msgKey []byte, msgValue []byte) {
				consumer.handleMessage(fieldLogger, msgValue, partition)
			},
		)
		if err != nil {
			return fmt.Errorf("cannot create consumer for partition %d: %w", partition, err)
		}

		consumer.consumers = append(consumer.consumers, mqConsumer)
	}

	requiredTags := sets.New[string]()
	requiredQuantifiers := sets.New[string]()

	for _, metric := range consumer.Config.Get().Metrics {
		requiredTags.Insert(metric.Tags...)
		for _, filter := range metric.Filters {
			requiredTags.Insert(filter.Tag)
		}

		requiredQuantifiers.Insert(metric.Quantifier)
	}

	consumer.requiredTags = requiredTags
	consumer.requiredQuantifiers = requiredQuantifiers

	return nil
}

type indexedTagProvider struct {
	provider   kelemetrix.TagProvider
	startIndex int
	endIndex   int
}

type tagProviderIndex struct {
	names       []string
	nameToIndex map[string]int
	providers   []indexedTagProvider
}

func indexTagProviders(requiredTags sets.Set[string], registry *kelemetrix.Registry) (*tagProviderIndex, error) {
	requiredTagProviders := sets.New[int]()
	for tag := range requiredTags {
		index, hasTag := registry.TagProviderNameIndex[tag]
		if !hasTag {
			return nil, fmt.Errorf("tag %q does not exist", tag)
		}

		requiredTagProviders.Insert(index)
	}

	tagNames := []string{}
	tagProviders := []indexedTagProvider{}
	for requiredTagProvider := range requiredTagProviders {
		provider := registry.TagProviders[requiredTagProvider]

		indexed := indexedTagProvider{
			provider:   provider,
			startIndex: len(tagNames),
			endIndex:   len(tagNames) + len(provider.TagNames()),
		}

		tagNames = append(tagNames, provider.TagNames()...)
		tagProviders = append(tagProviders, indexed)
	}

	tagNameToIndex := map[string]int{}
	for i, tagName := range tagNames {
		tagNameToIndex[tagName] = i
	}

	return &tagProviderIndex{
		names:       tagNames,
		nameToIndex: tagNameToIndex,
		providers:   tagProviders,
	}, nil
}

type tagFilter struct {
	tagIndex int
	isRegex  bool
	choices  []string
	regexs   []*regexp.Regexp
	negate   bool
}

func newTagFilter(tags *tagProviderIndex, filter config.Filter) (tagFilter, error) {
	tagIndex := tags.nameToIndex[filter.Tag]
	choices := filter.OneOf

	var regexs []*regexp.Regexp
	isRegex := filter.IsRegex
	if isRegex {
		for _, choice := range choices {
			regex, err := regexp.Compile(choice)
			if err != nil {
				return tagFilter{}, fmt.Errorf("error compiling regex %s: %w", choice, err)
			}
			regexs = append(regexs, regex)
		}
	}

	return tagFilter{
		tagIndex: tagIndex,
		isRegex:  isRegex,
		regexs:   regexs,
		choices:  choices,
		negate:   filter.Negate,
	}, nil
}

func (filter tagFilter) test(tagValues []string) bool {
	value := tagValues[filter.tagIndex]
	for i := range filter.choices {
		var predicate bool
		if filter.isRegex {
			predicate = filter.regexs[i].MatchString(value)
		} else {
			predicate = filter.choices[i] == value
		}

		if filter.negate {
			predicate = !predicate
		}

		if predicate {
			return true
		}
	}

	return false
}

func (consumer *Consumer) Start(ctx context.Context) error {
	tags, err := indexTagProviders(consumer.requiredTags, consumer.Registry)
	if err != nil {
		return err
	}

	quantifiers := []kelemetrix.Quantifier{}
	quantifierNameToIndex := map[string]int{}
	for quantifier := range consumer.requiredQuantifiers {
		registryIndex, hasQuantifier := consumer.Registry.QuantifierNameIndex[quantifier]
		if !hasQuantifier {
			return fmt.Errorf("quantifier %q does not exist", quantifier)
		}

		quantifierIndex := len(quantifiers)
		quantifiers = append(quantifiers, consumer.Registry.Quantifiers[registryIndex])
		quantifierNameToIndex[quantifier] = quantifierIndex
	}

	handlers := []metricHandler{}
	for _, metric := range consumer.Config.Get().Metrics {
		quantifierIndex := quantifierNameToIndex[metric.Quantifier]

		tagSet := make([]int, len(metric.Tags))
		for i, tag := range metric.Tags {
			tagSet[i] = tags.nameToIndex[tag]
		}

		filters := []tagFilter{}
		for _, filterConfig := range metric.Filters {
			filter, err := newTagFilter(tags, filterConfig)
			if err != nil {
				return err
			}

			filters = append(filters, filter)
		}

		metricImpl := metrics.NewDynamic(consumer.Metrics, metric.Name, metric.Tags)
		metricType := quantifiers[quantifierIndex].Type()

		var metricFn func(int64, []string)
		switch metricType {
		case kelemetrix.MetricTypeCount:
			metricFn = metricImpl.Count
		case kelemetrix.MetricTypeGauge:
			metricFn = metricImpl.Gauge
		case kelemetrix.MetricTypeHistogram:
			metricFn = metricImpl.Histogram
		default:
			panic(fmt.Sprintf("unknown metric type %#v", metricType))
		}

		handlers = append(handlers, metricHandler{
			quantifierIndex: quantifierIndex,
			filters:         filters,
			tagSet:          tagSet,
			metricFn:        metricFn,
		})
	}

	consumer.tagNames = tags.names
	consumer.tagProviders = tags.providers
	consumer.quantifiers = quantifiers
	consumer.handlers = handlers

	close(consumer.startupReady)

	return nil
}

type metricHandler struct {
	quantifierIndex int
	filters         []tagFilter
	tagSet          []int
	metricFn        func(int64, []string)
}

func (h metricHandler) handle(tagValues []string, quantities []int64, hasQuantities []bool) {
	if !hasQuantities[h.quantifierIndex] {
		return
	}

	for _, filter := range h.filters {
		if !filter.test(tagValues) {
			return
		}
	}

	outputTags := make([]string, len(h.tagSet))
	for i, tagIndex := range h.tagSet {
		outputTags[i] = tagValues[tagIndex]
	}

	quantity := quantities[h.quantifierIndex]

	h.metricFn(quantity, outputTags)
}

func (consumer *Consumer) handleMessage(
	fieldLogger logrus.FieldLogger,
	msgValue []byte,
	partition mq.PartitionId,
) {
	<-consumer.startupReady

	logger := fieldLogger.WithField("mod", "audit-consumer").WithField("partition", partition)

	message := &audit.Message{}
	if err := json.Unmarshal(msgValue, message); err != nil {
		logger.WithError(err).Error("error decoding audit data")
		return
	}

	tagValues := make([]string, len(consumer.tagNames))
	for _, indexed := range consumer.tagProviders {
		indexed.provider.ProvideValues(message, tagValues[indexed.startIndex:indexed.endIndex])
	}

	taggedLogger := logger
	if consumer.options.logTags {
		for i, tagValue := range tagValues {
			taggedLogger = taggedLogger.WithField(fmt.Sprintf("tag.%s", consumer.tagNames[i]), tagValue)
		}
	}

	quantities := make([]int64, len(consumer.quantifiers))
	hasQuantities := make([]bool, len(consumer.quantifiers))

	for i, quantifier := range consumer.quantifiers {
		quantity, ok := quantifier.Quantify(message)
		quantities[i] = quantity
		hasQuantities[i] = ok

		if ok && consumer.options.logQuantities {
			taggedLogger = taggedLogger.WithField(fmt.Sprintf("quantity.%s", quantifier.Name()), quantity)
		}
	}

	for _, handler := range consumer.handlers {
		handler.handle(tagValues, quantities, hasQuantities)
	}

	taggedLogger.Debug("export metrics")
}

func (consumer *Consumer) Close(ctx context.Context) error { return nil }
