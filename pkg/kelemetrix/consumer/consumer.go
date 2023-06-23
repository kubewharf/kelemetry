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

package kelemetrixconsumer

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
	Enable        bool
	ConsumerGroup string
	Partitions    []int32
	LogTags       bool
	LogQuantities bool
}

func (options *Options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.Enable, "kelemetrix-enable", false, "enable kelemetrix")
	fs.StringVar(&options.ConsumerGroup, "kelemetrix-consumer-group", "kelemetrix", "audit consumer group name")
	fs.Int32SliceVar(
		&options.Partitions,
		"kelemetrix-consumer-partition",
		[]int32{0, 1, 2, 3, 4},
		"audit message queue partitions to consume",
	)

	fs.BoolVar(&options.LogTags, "kelemetrix-debug-log-tags", false, "log metric tags for debugging")
	fs.BoolVar(&options.LogQuantities, "kelemetrix-debug-log-quantities", false, "log metric quantities for debugging")
}

func (options *Options) EnableFlag() *bool { return &options.Enable }

type Consumer struct {
	options  Options
	Mq       mq.Queue
	Metrics  metrics.Client
	Registry *kelemetrix.Registry
	Config   config.Provider

	consumers []mq.Consumer

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

	for _, partition := range consumer.options.Partitions {
		group := mq.ConsumerGroup(consumer.options.ConsumerGroup)
		partition := mq.PartitionId(partition)
		mqConsumer, err := consumer.Mq.CreateConsumer(
			group,
			partition,
			func(ctx context.Context, fieldLogger logrus.FieldLogger, msgKey []byte, msgValue []byte) {
				consumer.handleMessageRaw(fieldLogger, msgValue, partition)
			},
		)
		if err != nil {
			return fmt.Errorf("cannot create consumer for partition %d: %w", partition, err)
		}

		consumer.consumers = append(consumer.consumers, mqConsumer)
	}

	return nil
}

func (consumer *Consumer) initTags() sets.Set[string] {
	requiredTags := sets.New[string]()

	for _, metric := range consumer.Config.Get().Metrics {
		requiredTags.Insert(metric.Tags...)
		for _, filter := range metric.TagFilters {
			requiredTags.Insert(filter.Tag)
		}
	}

	return requiredTags
}

func (consumer *Consumer) initQuantifiers() sets.Set[string] {
	requiredQuantifiers := sets.New[string]()

	for _, metric := range consumer.Config.Get().Metrics {
		requiredQuantifiers.Insert(metric.Quantifier)
		for _, filter := range metric.QuantityFilters {
			requiredQuantifiers.Insert(filter.Quantity)
		}
	}

	return requiredQuantifiers
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

func newTagFilter(tags *tagProviderIndex, filter config.TagFilter) (tagFilter, error) {
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

type quantifierIndex struct {
	quantifiers []kelemetrix.Quantifier
	nameToIndex map[string]int
}

func indexQuantifiers(requiredQuantifiers sets.Set[string], registry *kelemetrix.Registry) (*quantifierIndex, error) {
	quantifiers := []kelemetrix.Quantifier{}
	quantifierNameToIndex := map[string]int{}
	for quantifier := range requiredQuantifiers {
		registryIndex, hasQuantifier := registry.QuantifierNameIndex[quantifier]
		if !hasQuantifier {
			return nil, fmt.Errorf("quantifier %q does not exist", quantifier)
		}

		quantifierIndex := len(quantifiers)
		quantifiers = append(quantifiers, registry.Quantifiers.Impls[registryIndex])
		quantifierNameToIndex[quantifier] = quantifierIndex
	}

	return &quantifierIndex{quantifiers: quantifiers, nameToIndex: quantifierNameToIndex}, nil
}

type quantityFilter struct {
	quantityIndex int
	threshold     float64

	acceptLeftOfThreshold  bool
	acceptEqualThreshold   bool
	acceptRightOfThreshold bool
}

func newQuantityFilter(quantities *quantifierIndex, filter config.QuantityFilter) (quantityFilter, error) {
	quantityIndex := quantities.nameToIndex[filter.Quantity]

	qf := quantityFilter{
		quantityIndex: quantityIndex,
		threshold:     filter.Threshold,
	}

	switch filter.Operator {
	case config.QuantityOperatorGreater:
		qf.acceptLeftOfThreshold, qf.acceptEqualThreshold, qf.acceptRightOfThreshold = false, false, true
	case config.QuantityOperatorLess:
		qf.acceptLeftOfThreshold, qf.acceptEqualThreshold, qf.acceptRightOfThreshold = true, false, false
	case config.QuantityOperatorGreaterEq:
		qf.acceptLeftOfThreshold, qf.acceptEqualThreshold, qf.acceptRightOfThreshold = false, true, true
	case config.QuantityOperatorLessEq:
		qf.acceptLeftOfThreshold, qf.acceptEqualThreshold, qf.acceptRightOfThreshold = true, true, false
	default:
		return qf, fmt.Errorf("unknown quantity operator %q", filter.Operator)
	}

	return qf, nil
}

func (filter quantityFilter) test(quantities []float64) bool {
	isRight := quantities[filter.quantityIndex] > filter.threshold
	isEqual := quantities[filter.quantityIndex] == filter.threshold
	isLeft := quantities[filter.quantityIndex] < filter.threshold
	return filter.acceptRightOfThreshold && isRight || filter.acceptEqualThreshold && isEqual || filter.acceptLeftOfThreshold && isLeft
}

func (consumer *Consumer) PrepareTagsQuantifiers() error {
	requiredTags := consumer.initTags()
	requiredQuantifiers := consumer.initQuantifiers()

	tags, err := indexTagProviders(requiredTags, consumer.Registry)
	if err != nil {
		return err
	}

	quantifiers, err := indexQuantifiers(requiredQuantifiers, consumer.Registry)
	if err != nil {
		return err
	}

	handlers := []metricHandler{}
	for _, metric := range consumer.Config.Get().Metrics {
		quantifierIndex := quantifiers.nameToIndex[metric.Quantifier]

		tagSet := make([]int, len(metric.Tags))
		for i, tag := range metric.Tags {
			tagSet[i] = tags.nameToIndex[tag]
		}

		tagFilters := []tagFilter{}
		for _, filterConfig := range metric.TagFilters {
			filter, err := newTagFilter(tags, filterConfig)
			if err != nil {
				return err
			}

			tagFilters = append(tagFilters, filter)
		}

		quantityFilters := []quantityFilter{}
		for _, filterConfig := range metric.QuantityFilters {
			filter, err := newQuantityFilter(quantifiers, filterConfig)
			if err != nil {
				return err
			}

			quantityFilters = append(quantityFilters, filter)
		}

		metricImpl := metrics.NewDynamic(consumer.Metrics, metric.Name, metric.Tags)
		metricType := quantifiers.quantifiers[quantifierIndex].Type()

		var metricFn func(float64, []string)
		switch metricType {
		case kelemetrix.MetricTypeCount:
			metricFn = metricImpl.Count
		case kelemetrix.MetricTypeHistogram:
			metricFn = metricImpl.Histogram
		case kelemetrix.MetricTypeSummary:
			metricFn = metricImpl.Summary
		default:
			panic(fmt.Sprintf("unknown metric type %#v", metricType))
		}

		handlers = append(handlers, metricHandler{
			quantifierIndex: quantifierIndex,
			tagFilters:      tagFilters,
			quantityFilters: quantityFilters,
			tagSet:          tagSet,
			metricFn:        metricFn,
		})
	}

	consumer.tagNames = tags.names
	consumer.tagProviders = tags.providers
	consumer.quantifiers = quantifiers.quantifiers
	consumer.handlers = handlers

	return nil
}

func (consumer *Consumer) Start(ctx context.Context) error {
	if err := consumer.PrepareTagsQuantifiers(); err != nil {
		return err
	}

	close(consumer.startupReady)

	return nil
}

type metricHandler struct {
	quantifierIndex int
	tagFilters      []tagFilter
	quantityFilters []quantityFilter
	tagSet          []int
	metricFn        func(float64, []string)
}

func (h metricHandler) handle(tagValues []string, quantities []float64, hasQuantities []bool) {
	if !hasQuantities[h.quantifierIndex] {
		return
	}

	for _, filter := range h.tagFilters {
		if !filter.test(tagValues) {
			return
		}
	}

	for _, filter := range h.quantityFilters {
		if !filter.test(quantities) {
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

func (consumer *Consumer) handleMessageRaw(
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

	consumer.HandleMessage(logger, message)
}

func (consumer *Consumer) HandleMessage(logger logrus.FieldLogger, message *audit.Message) {
	tagValues := make([]string, len(consumer.tagNames))
	for _, indexed := range consumer.tagProviders {
		indexed.provider.ProvideValues(message, tagValues[indexed.startIndex:indexed.endIndex])
	}

	if consumer.options.LogTags {
		for i, tagValue := range tagValues {
			logger = logger.WithField(fmt.Sprintf("tag.%s", consumer.tagNames[i]), tagValue)
		}
	}

	quantities := make([]float64, len(consumer.quantifiers))
	hasQuantities := make([]bool, len(consumer.quantifiers))

	for i, quantifier := range consumer.quantifiers {
		quantity, ok := quantifier.Quantify(message)
		quantities[i] = quantity
		hasQuantities[i] = ok

		if ok && consumer.options.LogQuantities {
			logger = logger.WithField(fmt.Sprintf("quantity.%s", quantifier.Name()), quantity)
		}
	}

	for _, handler := range consumer.handlers {
		handler.handle(tagValues, quantities, hasQuantities)
	}

	logger.Debug("export metrics")
}

func (consumer *Consumer) Close(ctx context.Context) error { return nil }
