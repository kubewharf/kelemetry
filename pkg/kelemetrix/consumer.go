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

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/audit/mq"
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
	Registry *Registry

	quantifiers  []initedQuantifier
	consumers    []mq.Consumer
	startupReady chan struct{}
}

type initedQuantifier struct {
	quantifier Quantifier
	metric     func(int64, []string)
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

	return nil
}

func (consumer *Consumer) Start(ctx context.Context) error {
	for _, quantifier := range consumer.Registry.quantifiers {
		filteredFns := []func(int64, []string){}

		for setName, setFilter := range quantifier.TagSets() {
			matched := []int{}
			for i, tagName := range consumer.Registry.tagNames {
				if setFilter(tagName) {
					matched = append(matched, i)
				}
			}

			filteredTagNames := make([]string, len(matched))
			for filteredIndex, grossIndex := range matched {
				filteredTagNames[filteredIndex] = consumer.Registry.tagNames[grossIndex]
			}

			metric := metrics.NewDynamic(consumer.Metrics, fmt.Sprintf("%s_%s", quantifier.Name(), setName), filteredTagNames)

			var baseFn func(int64, []string)
			switch quantifier.Type() {
			case MetricTypeCount:
				baseFn = metric.Count
			case MetricTypeGauge:
				baseFn = metric.Gauge
			case MetricTypeHistogram:
				baseFn = metric.Histogram
			default:
				panic(fmt.Sprintf("unknown metric type %#v", quantifier.Type()))
			}

			filteredFn := func(quantity int64, tagValues []string) {
				filteredTagValues := make([]string, len(filteredTagNames))
				for filteredIndex, grossIndex := range matched {
					filteredTagValues[filteredIndex] = tagValues[grossIndex]
				}

				baseFn(quantity, filteredTagValues)
			}

			filteredFns = append(filteredFns, filteredFn)
		}

		consumer.quantifiers = append(
			consumer.quantifiers,
			initedQuantifier{quantifier: quantifier, metric: func(quantity int64, tagValues []string) {
				for _, fn := range filteredFns {
					fn(quantity, tagValues)
				}
			}},
		)
	}

	close(consumer.startupReady)

	return nil
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

	tagValues := make([]string, len(consumer.Registry.tagNames))
	for _, indexed := range consumer.Registry.tagProviders {
		indexed.provider.ProvideValues(message, tagValues[indexed.startTagIndex:indexed.endTagIndex])
	}

	taggedLogger := logger
	if consumer.options.logTags {
		for i, tagName := range consumer.Registry.tagNames {
			taggedLogger = taggedLogger.WithField(fmt.Sprintf("tag.%s", tagName), tagValues[i])
		}
	}

	for _, quantifier := range consumer.quantifiers {
		quantity, ok := quantifier.quantifier.Quantify(message)
		if ok {
			quantifier.metric(quantity, tagValues)
			if consumer.options.logQuantities {
				taggedLogger = taggedLogger.WithField(fmt.Sprintf("quantity.%s", quantifier.quantifier.Name()), quantity)
			}
		}
	}

	taggedLogger.Debug("export metrics")
}

func (consumer *Consumer) Close(ctx context.Context) error {
	// TODO wait for consumer close
	return nil
}
