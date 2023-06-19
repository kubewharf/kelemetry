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

package aggregator

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/aggregator/eventdecorator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	"github.com/kubewharf/kelemetry/pkg/aggregator/objectspandecorator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/spancache"
	"github.com/kubewharf/kelemetry/pkg/aggregator/tracer"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("aggregator", manager.Ptr[Aggregator](&aggregator{}))
}

type options struct {
	reserveTtl                   time.Duration
	spanTtl                      time.Duration
	spanFollowTtl                time.Duration
	spanExtraTtl                 time.Duration
	globalPseudoTags             map[string]string
	globalEventTags              map[string]string
	subObjectPrimaryPollInterval time.Duration
	subObjectPrimaryPollTimeout  time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.DurationVar(
		&options.reserveTtl,
		"aggregator-reserve-ttl",
		time.Second*10,
		"if an object span has not been created after this duration, "+
			"another goroutine/process will try to create it",
	)
	fs.DurationVar(&options.spanTtl,
		"aggregator-span-ttl",
		time.Minute*30,
		"duration of each span",
	)
	fs.DurationVar(&options.spanFollowTtl,
		"aggregator-span-follow-ttl",
		0,
		"duration after expiry of previous span within which new spans are considered FollowsFrom",
	)
	fs.DurationVar(&options.spanExtraTtl,
		"aggregator-span-extra-ttl",
		0,
		"duration for which an object span is retained after the FollowsFrom period has ended",
	)
	fs.StringToStringVar(&options.globalPseudoTags,
		"aggregator-pseudo-span-global-tags",
		map[string]string{},
		"tags applied to all object/spec/status/deletion pseudo-spans",
	)
	fs.StringToStringVar(&options.globalEventTags,
		"aggregator-event-span-global-tags",
		map[string]string{},
		"tags applied to all event spans",
	)
	fs.DurationVar(&options.subObjectPrimaryPollInterval,
		"aggregator-sub-object-primary-poll-interval",
		time.Second*5,
		"interval to poll primary event before promoting non-primary events",
	)
	fs.DurationVar(&options.subObjectPrimaryPollTimeout,
		"aggregator-sub-object-primary-poll-timeout",
		time.Second*5,
		"timeout to wait for primary event before promoting non-primary events "+
			"(increasing this timeout may lead to indefinite consumer lag",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type Aggregator interface {
	manager.Component

	// Send sends an event to the tracer backend.
	// The sub-object ID is an optional identifier that associates the event with an object-scoped context (e.g. resource version).
	// If an event is created with the same sub-object ID with Primary=false,
	// it waits for the primary event to be created and takes it as the parent.
	// If the primary event does not get created after options.subObjectPrimaryBackoff, this event is promoted as primary.
	// If multiple primary events are sent, the slower one (by SpanCache-authoritative timing) is demoted.
	Send(ctx context.Context, object util.ObjectRef, event *aggregatorevent.Event, subObjectId *SubObjectId) error
}

type SubObjectId struct {
	Id      string
	Primary bool
}

type aggregator struct {
	options   options
	Clock     clock.Clock
	Linkers   *manager.List[linker.Linker]
	Logger    logrus.FieldLogger
	SpanCache spancache.Cache
	Tracer    tracer.Tracer
	Metrics   metrics.Client

	EventDecorators      *manager.List[eventdecorator.Decorator]
	ObjectSpanDecorators *manager.List[objectspandecorator.Decorator]

	SendMetric               *metrics.Metric[*sendMetric]
	SinceEventMetric         *metrics.Metric[*sinceEventMetric]
	LazySpanMetric           *metrics.Metric[*lazySpanMetric]
	LazySpanRetryCountMetric *metrics.Metric[*lazySpanRetryCountMetric]
}

type sendMetric struct {
	Cluster        string
	TraceSource    string
	HasSubObjectId bool
	Primary        bool // whether the subObjectId is primary or not
	PrimaryChanged bool // whether the primary got demoted or non-primary got promoted
	Success        bool
	Error          metrics.LabeledError
}

func (*sendMetric) MetricName() string { return "aggregator_send" }

type sinceEventMetric struct {
	Cluster     string
	TraceSource string
}

func (*sinceEventMetric) MetricName() string { return "aggregator_send_since_event" }

type lazySpanMetric struct {
	Cluster string
	Field   string
	Result  string
}

func (*lazySpanMetric) MetricName() string { return "aggregator_lazy_span" }

type lazySpanRetryCountMetric lazySpanMetric

func (*lazySpanRetryCountMetric) MetricName() string { return "aggregator_lazy_span_retry_count" }

func (aggregator *aggregator) Options() manager.Options {
	return &aggregator.options
}

func (aggregator *aggregator) Init() error {
	if aggregator.options.spanFollowTtl > aggregator.options.spanTtl {
		return fmt.Errorf("invalid option: --span-ttl must not be shorter than --span-follow-ttl")
	}

	return nil
}

func (aggregator *aggregator) Start(ctx context.Context) error { return nil }

func (aggregator *aggregator) Close(ctx context.Context) error { return nil }

func (aggregator *aggregator) Send(
	ctx context.Context,
	object util.ObjectRef,
	event *aggregatorevent.Event,
	subObjectId *SubObjectId,
) (err error) {
	sendMetric := &sendMetric{Cluster: object.Cluster, TraceSource: event.TraceSource}
	defer aggregator.SendMetric.DeferCount(aggregator.Clock.Now(), sendMetric)

	aggregator.SinceEventMetric.
		With(&sinceEventMetric{Cluster: object.Cluster, TraceSource: event.TraceSource}).
		Summary(float64(aggregator.Clock.Since(event.Time).Nanoseconds()))

	var parentSpan tracer.SpanContext

	type primaryReservation struct {
		cacheKey string
		uid      spancache.Uid
	}
	var reservedPrimary *primaryReservation

	if subObjectId != nil {
		sendMetric.HasSubObjectId = true
		sendMetric.Primary = subObjectId.Primary

		cacheKey := aggregator.spanCacheKey(object, subObjectId.Id)

		if !subObjectId.Primary {
			pollCtx, cancelFunc := context.WithTimeout(ctx, aggregator.options.subObjectPrimaryPollTimeout)
			defer cancelFunc()

			if err := wait.PollUntilContextCancel(
				pollCtx,
				aggregator.options.subObjectPrimaryPollInterval,
				true,
				func(context.Context) (done bool, err error) {
					entry, err := aggregator.SpanCache.Fetch(pollCtx, cacheKey)
					if err != nil {
						sendMetric.Error = metrics.LabelError(err, "PrimaryEventPoll")
						return false, fmt.Errorf("%w during primary event poll", err)
					}

					if entry != nil {
						parentSpan, err = aggregator.Tracer.ExtractCarrier(entry.Value)
						if err != nil {
							sendMetric.Error = metrics.LabelError(err, "ExtractPrimaryCarrier")
							return false, fmt.Errorf("%w during decoding primary span", err)
						}

						return true, nil
					}

					return false, nil
				},
			); err != nil {
				if !wait.Interrupted(err) {
					if sendMetric.Error == nil {
						sendMetric.Error = metrics.LabelError(err, "UnknownPrimaryPoll")
						aggregator.Logger.
							WithFields(object.AsFields("object")).
							WithField("event", event.Title).
							WithError(err).
							Warn("Unknown error for primary poll")
					}
					return err
				}

				sendMetric.PrimaryChanged = parentSpan == nil

				// primary poll timeout, parentSpan == nil, so promote to primary
				sendMetric.Error = nil
			}
		}

		if parentSpan == nil {
			// either object ID is primary, or primary poll expired, in which case we should promote
			if err := retry.OnError(retry.DefaultBackoff, spancache.ShouldRetry, func() error {
				entry, err := aggregator.SpanCache.FetchOrReserve(ctx, cacheKey, aggregator.options.reserveTtl)
				if err != nil {
					sendMetric.Error = metrics.LabelError(err, "PrimaryReserve")
					return fmt.Errorf("%w during primary event fetch-or-reserve", err)
				}

				if entry.Value != nil {
					// another primary event was sent, demote this one
					sendMetric.PrimaryChanged = true
					event.Log(
						zconstants.LogTypeRealError,
						fmt.Sprintf("Kelemetry: multiple primary events for %s sent, demoted later event", subObjectId.Id),
					)

					parentSpan, err = aggregator.Tracer.ExtractCarrier(entry.Value)
					if err != nil {
						sendMetric.Error = metrics.LabelError(err, "ExtractAltPrimaryCarrier")
						return fmt.Errorf("%w during decoding primary span", err)
					}

					return nil
				}

				reservedPrimary = &primaryReservation{
					cacheKey: cacheKey,
					uid:      entry.LastUid,
				}

				return nil
			}); err != nil {
				if wait.Interrupted(err) {
					sendMetric.Error = metrics.LabelError(err, "PrimaryReserveTimeout")
				}
				return err
			}
		}
	}

	if parentSpan == nil {
		// there is no primary span to fallback to, so we are the primary
		parentSpan, err = aggregator.ensureFieldSpan(ctx, object, event.Field, event.Time)
		if err != nil {
			sendMetric.Error = metrics.LabelError(err, "EnsureFieldSpan")
			return fmt.Errorf("%w during fetching field span for primary span", err)
		}
	}

	for _, decorator := range aggregator.EventDecorators.Impls {
		decorator.Decorate(ctx, object, event)
	}

	span := tracer.Span{
		Type:       event.TraceSource,
		Name:       event.Title,
		StartTime:  event.Time,
		FinishTime: event.GetEndTime(),
		Parent:     parentSpan,
		Tags: map[string]string{
			"cluster":              object.Cluster,
			"namespace":            object.Namespace,
			"name":                 object.Name,
			"group":                object.Group,
			"version":              object.Version,
			"resource":             object.Resource,
			zconstants.TraceSource: event.TraceSource,
		},
		Logs: event.Logs,
	}
	for tagKey, tagValue := range event.Tags {
		span.Tags[tagKey] = fmt.Sprint(tagValue)
	}
	for tagKey, tagValue := range aggregator.options.globalPseudoTags {
		span.Tags[tagKey] = tagValue
	}

	sentSpan, err := aggregator.Tracer.CreateSpan(span)
	if err != nil {
		sendMetric.Error = metrics.LabelError(err, "CreateSpan")
		return fmt.Errorf("cannot create span: %w", err)
	}

	if reservedPrimary != nil {
		sentSpanRaw, err := aggregator.Tracer.InjectCarrier(sentSpan)
		if err != nil {
			sendMetric.Error = metrics.LabelError(err, "InjectCarrier")
			return fmt.Errorf("%w during serializing sent span ID", err)
		}

		if err := aggregator.SpanCache.SetReserved(
			ctx,
			reservedPrimary.cacheKey,
			sentSpanRaw,
			reservedPrimary.uid,
			aggregator.options.spanTtl,
		); err != nil {
			sendMetric.Error = metrics.LabelError(err, "SetReserved")
			return fmt.Errorf("%w during persisting primary span ID", err)
		}
	}

	sendMetric.Success = true

	aggregator.Logger.WithFields(object.AsFields("object")).
		WithField("event", event.Title).
		WithField("logs", len(event.Logs)).
		Debug("CreateSpan")

	return nil
}

func (aggregator *aggregator) ensureFieldSpan(
	ctx context.Context,
	object util.ObjectRef,
	field string,
	eventTime time.Time,
) (tracer.SpanContext, error) {
	return aggregator.getOrCreateSpan(ctx, object, field, eventTime, func() (tracer.SpanContext, error) {
		return aggregator.ensureObjectSpan(ctx, object, eventTime)
	})
}

func (aggregator *aggregator) ensureObjectSpan(
	ctx context.Context,
	object util.ObjectRef,
	eventTime time.Time,
) (tracer.SpanContext, error) {
	return aggregator.getOrCreateSpan(ctx, object, zconstants.NestLevelObject, eventTime, func() (_ tracer.SpanContext, err error) {
		// try to associate a parent object
		var parent *util.ObjectRef

		for _, linker := range aggregator.Linkers.Impls {
			parent = linker.Lookup(ctx, object)
			if parent != nil {
				break
			}
		}

		if parent == nil {
			return nil, nil
		}

		// ensure parent object has a span
		return aggregator.ensureChildrenSpan(ctx, *parent, eventTime)
	})
}

func (aggregator *aggregator) ensureChildrenSpan(
	ctx context.Context,
	object util.ObjectRef,
	eventTime time.Time,
) (tracer.SpanContext, error) {
	return aggregator.getOrCreateSpan(ctx, object, zconstants.NestLevelChildren, eventTime, func() (tracer.SpanContext, error) {
		return aggregator.ensureObjectSpan(ctx, object, eventTime)
	})
}

func (aggregator *aggregator) getOrCreateSpan(
	ctx context.Context,
	object util.ObjectRef,
	field string,
	eventTime time.Time,
	parentGetter func() (tracer.SpanContext, error),
) (tracer.SpanContext, error) {
	lazySpanMetric := &lazySpanMetric{
		Cluster: object.Cluster,
		Field:   field,
		Result:  "error",
	}
	defer aggregator.LazySpanMetric.DeferCount(aggregator.Clock.Now(), lazySpanMetric)

	cacheKey := aggregator.expiringSpanCacheKey(object, field, eventTime)

	logger := aggregator.Logger.
		WithField("step", "getOrCreateSpan").
		WithFields(object.AsFields("object")).
		WithField("field", field)

	var reserveUid spancache.Uid
	var returnSpan tracer.SpanContext
	var followsFrom tracer.SpanContext

	defer func() {
		logger.WithField("cacheKey", cacheKey).WithField("result", lazySpanMetric.Result).Debug("getOrCreateSpan")
	}()

	retries := int64(0)
	if err := retry.OnError(retry.DefaultBackoff, spancache.ShouldRetry, func() error {
		retries += 1
		entry, err := aggregator.SpanCache.FetchOrReserve(ctx, cacheKey, aggregator.options.reserveTtl)
		if err != nil {
			return metrics.LabelError(fmt.Errorf("%w during initial fetch-or-reserve", err), "FetchOrReserve")
		}

		if entry.Value != nil {
			// the entry already exists, no additional logic required
			reserveUid = []byte{}
			followsFrom = nil
			returnSpan, err = aggregator.Tracer.ExtractCarrier(entry.Value)
			if err != nil {
				return metrics.LabelError(fmt.Errorf("persisted span contains invalid data: %w", err), "BadCarrier")
			}

			return nil
		}

		// we created a new reservation
		reserveUid = entry.LastUid
		returnSpan = nil
		followsFrom = nil

		// check if this new span is a follower of the previous one
		followsTime := eventTime.Add(-aggregator.options.spanFollowTtl)
		followsKey := aggregator.expiringSpanCacheKey(object, field, followsTime)

		if followsKey == cacheKey {
			// previous span expired
			return nil
		}

		followsEntry, err := aggregator.SpanCache.Fetch(ctx, followsKey)
		if err != nil {
			return metrics.LabelError(fmt.Errorf("error fetching followed entry: %w", err), "FetchFollow")
		}

		if followsEntry == nil {
			// no following target
			return nil
		}

		if followsEntry.Value == nil {
			return metrics.LabelError(spancache.ErrAlreadyReserved, "FollowPending") // trigger retry
		}

		// we have a following target
		followsFrom, err = aggregator.Tracer.ExtractCarrier(followsEntry.Value)
		if err != nil {
			return metrics.LabelError(fmt.Errorf("followed persisted span contains invalid data: %w", err), "BadFollowCarrier")
		}

		return nil
	}); err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot reserve or fetch span %q: %w", cacheKey, err), "ReserveRetryLoop")
	}

	retryCountMetric := lazySpanRetryCountMetric(*lazySpanMetric)
	defer func() {
		aggregator.LazySpanRetryCountMetric.With(&retryCountMetric).Summary(float64(retries))
	}() // take the value of lazySpanMetric later

	logger = logger.
		WithField("returnSpan", returnSpan != nil).
		WithField("reserveUid", reserveUid).
		WithField("followsFrom", followsFrom != nil)

	if returnSpan != nil {
		lazySpanMetric.Result = "fetch"
		return returnSpan, nil
	}

	// we have a new reservation, need to initialize it now
	startTime := aggregator.Clock.Now()

	parent, err := parentGetter()
	if err != nil {
		return nil, fmt.Errorf("cannot fetch parent object: %w", err)
	}

	span, err := aggregator.createSpan(ctx, object, field, eventTime, parent, followsFrom)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot create span: %w", err), "CreateSpan")
	}

	entryValue, err := aggregator.Tracer.InjectCarrier(span)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot serialize span context: %w", err), "InjectCarrier")
	}

	totalTtl := aggregator.options.spanTtl + aggregator.options.spanFollowTtl + aggregator.options.spanExtraTtl
	err = aggregator.SpanCache.SetReserved(ctx, cacheKey, entryValue, reserveUid, totalTtl)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot persist reserved value: %w", err), "PersistCarrier")
	}

	logger.WithField("duration", aggregator.Clock.Since(startTime)).Debug("Created new span")

	if followsFrom != nil {
		lazySpanMetric.Result = "renew"
	} else {
		lazySpanMetric.Result = "create"
	}

	return span, nil
}

func (aggregator *aggregator) createSpan(
	ctx context.Context,
	object util.ObjectRef,
	nestLevel string,
	eventTime time.Time,
	parent tracer.SpanContext,
	followsFrom tracer.SpanContext,
) (tracer.SpanContext, error) {
	remainderSeconds := eventTime.Unix() % int64(aggregator.options.spanTtl.Seconds())
	startTime := eventTime.Add(-time.Duration(remainderSeconds) * time.Second)
	span := tracer.Span{
		Type:       nestLevel,
		Name:       fmt.Sprintf("%s/%s %s", object.Resource, object.Name, nestLevel),
		StartTime:  startTime,
		FinishTime: startTime.Add(aggregator.options.spanTtl),
		Parent:     parent,
		Follows:    followsFrom,
		Tags: map[string]string{
			"cluster":              object.Cluster,
			"namespace":            object.Namespace,
			"name":                 object.Name,
			"group":                object.Group,
			"version":              object.Version,
			"resource":             object.Resource,
			zconstants.NestLevel:   nestLevel,
			zconstants.TraceSource: zconstants.TraceSourceObject,
			"timeStamp":            startTime.Format(time.RFC3339),
		},
	}
	for tagKey, tagValue := range aggregator.options.globalPseudoTags {
		span.Tags[tagKey] = tagValue
	}

	if nestLevel == zconstants.NestLevelObject {
		for _, decorator := range aggregator.ObjectSpanDecorators.Impls {
			decorator.Decorate(ctx, object, span.Type, span.Tags)
		}
	}

	spanContext, err := aggregator.Tracer.CreateSpan(span)
	if err != nil {
		return nil, metrics.LabelError(fmt.Errorf("cannot create span: %w", err), "CreateSpan")
	}

	aggregator.Logger.
		WithFields(object.AsFields("object")).
		WithField("field", nestLevel).
		WithField("parent", parent).
		Debug("CreateSpan")

	return spanContext, nil
}

func (aggregator *aggregator) expiringSpanCacheKey(object util.ObjectRef, field string, timestamp time.Time) string {
	expiringWindow := timestamp.Unix() / int64(aggregator.options.spanTtl.Seconds())
	return aggregator.spanCacheKey(object, fmt.Sprintf("field=%s,window=%d", field, expiringWindow))
}

func (aggregator *aggregator) spanCacheKey(object util.ObjectRef, subObjectId string) string {
	return fmt.Sprintf("%s/%s", object.String(), subObjectId)
}
