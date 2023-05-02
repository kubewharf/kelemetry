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

package decorator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/audit"
	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("diff-decorator", manager.Ptr(&decorator{}))
}

type decoratorOptions struct {
	enable            bool
	fetchBackoff      time.Duration
	fetchEventTimeout time.Duration
	fetchTotalTimeout time.Duration
}

func (options *decoratorOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(
		&options.enable,
		"diff-decorator-enable",
		false,
		"enable object diff audit decorator",
	)
	fs.DurationVar(
		&options.fetchBackoff,
		"diff-decorator-fetch-backoff",
		time.Second,
		"backoff time between multiple attempts to fetch diff",
	)
	fs.DurationVar(
		&options.fetchEventTimeout,
		"diff-decorator-fetch-event-timeout",
		time.Second*15,
		"backoff is halted if this duration has elapsed since ResponseComplete stageTimestamp",
	)
	fs.DurationVar(
		&options.fetchTotalTimeout,
		"diff-decorator-fetch-total-timeout",
		time.Second*10,
		"maximum total time in worker to wait for fetching a single resource",
	)
}

func (options *decoratorOptions) EnableFlag() *bool { return &options.enable }

type decorator struct {
	options decoratorOptions
	Logger  logrus.FieldLogger
	Clock   clock.Clock
	Cache   diffcache.Cache
	List    audit.DecoratorList

	DiffMetric            *metrics.Metric[*diffMetric]
	InformerLatencyMetric *metrics.Metric[*informerLatencyMetric]
	RetryCountMetric      *metrics.Metric[*retryCountMetric]
}

var _ = func() manager.Component { return &decorator{} }

type diffMetric struct {
	Verb     string
	CacheHit string
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
	Error    metrics.LabeledError
}

func (*diffMetric) MetricName() string { return "diff_decorator" }

type informerLatencyMetric struct {
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
}

func (*informerLatencyMetric) MetricName() string { return "diff_informer_latency" }

type retryCountMetric struct {
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
	Success  bool
}

func (*retryCountMetric) MetricName() string { return "diff_decorator_retry_count" }

func (decorator *decorator) Options() manager.Options {
	return &decorator.options
}

func (decorator *decorator) Init() error {
	decorator.List.AddDecorator(decorator)
	return nil
}

func (decorator *decorator) Start(ctx context.Context) error {
	return nil
}

func (decorator *decorator) Close(ctx context.Context) error {
	return nil
}

func (decorator *decorator) Decorate(ctx context.Context, message *audit.Message, event *aggregatorevent.Event) {
	logger := decorator.Logger.
		WithField("audit-id", message.AuditID).
		WithField("title", event.Title).
		WithField("verb", message.Verb)

	metric := &diffMetric{
		Verb:     message.Verb,
		CacheHit: "unknown",
		Cluster:  message.Cluster,
		ApiGroup: schema.GroupVersion{
			Group:   message.ObjectRef.APIGroup,
			Version: message.ObjectRef.APIVersion,
		},
		Resource: message.ObjectRef.Resource,
	}
	defer decorator.DiffMetric.DeferCount(decorator.Clock.Now(), metric)

	cacheHit, err := decorator.tryDecorate(ctx, logger, message, event)
	if err != nil {
		event.Log(zconstants.LogTypeKelemetryError, err.Error())
		metric.Error = err
	}

	metric.CacheHit = string(cacheHit)
}

type cacheHitType string

const (
	cacheHitTypeTrue            cacheHitType = "true"
	cacheHitTypeFalse           cacheHitType = "false"
	cacheHitTypeError           cacheHitType = "failedRequest"
	cacheHitTypeNoObjectRef     cacheHitType = "noObjectRef"
	cacheHitTypeRawJsonDecode   cacheHitType = "rawJsonDecodeError"
	cacheHitTypeNoop            cacheHitType = "noopUpdate"
	cacheHitTypeFiltered        cacheHitType = "filtered"
	cacheHitTypeSameRv          cacheHitType = "sameRv"
	cacheHitTypeSubresource     cacheHitType = "subresource"
	cacheHitTypeUnsupportedVerb cacheHitType = "unsupportedVerb"
)

func (decorator *decorator) tryDecorate(
	ctx context.Context,
	logger logrus.FieldLogger,
	message *audit.Message,
	event *aggregatorevent.Event,
) (cacheHitType, metrics.LabeledError) {
	if message.ResponseStatus != nil && message.ResponseStatus.Code >= 300 {
		event.Log(zconstants.LogTypeRealError, fmt.Sprintf("%s: %s", message.ResponseStatus.Reason, message.ResponseStatus.Message))
		if message.ResponseStatus.Details != nil {
			for _, detail := range message.ResponseStatus.Details.Causes {
				event.Log(zconstants.LogTypeRealError, fmt.Sprintf("%s: %s", detail.Type, detail.Message))
			}
		}

		return cacheHitTypeError, nil
	}

	if message.ObjectRef == nil {
		logger.Warn("audit event has no ObjectRef")
		return cacheHitTypeNoObjectRef, nil
	}

	var respObj *metav1.PartialObjectMetadata
	if message.ResponseObject != nil && message.ResponseObject.Raw != nil {
		if err := json.Unmarshal(message.ResponseObject.Raw, &respObj); err != nil {
			event.Log(zconstants.LogTypeKelemetryError, "Error decoding raw object")
			return cacheHitTypeRawJsonDecode, metrics.LabelError(fmt.Errorf("Error decoding raw object: %w", err), "RawJsonDecodeError")
		}
	}

	responseRv := ""
	if respObj != nil {
		responseRv = respObj.ResourceVersion
	}

	hasDiff := message.Verb == audit.VerbUpdate || message.Verb == audit.VerbPatch
	if hasDiff && respObj != nil && message.ObjectRef.ResourceVersion == respObj.ResourceVersion {
		event.Log(zconstants.LogTypeRealVerbose, "No-op update")
		return cacheHitTypeNoop, nil
	}

	if !decoratesResource(message) {
		return cacheHitTypeFiltered, nil
	}

	// NOTE: UID may be empty, but we don't use it anyway
	object := util.ObjectRefFromAudit(message.ObjectRef, message.Cluster, message.ObjectRef.UID)

	var tryOnce func(context.Context) (bool, error)

	switch message.Verb {
	case audit.VerbUpdate, audit.VerbPatch:
		oldRv := message.ObjectRef.ResourceVersion

		var newRv *string
		if responseRv != "" {
			newRv = pointer.String(responseRv)
		}

		if newRv != nil && oldRv == *newRv {
			event.Log(zconstants.LogTypeRealVerbose, "No-op update")
			return cacheHitTypeSameRv, nil
		}

		logger = logger.WithField("oldRv", oldRv).WithField("newRv", newRv)

		tryOnce = func(ctx context.Context) (bool, error) {
			return decorator.tryUpdateOnce(ctx, object, oldRv, newRv, event, message)
		}

	case audit.VerbCreate, audit.VerbDelete:
		if message.ObjectRef.Subresource != "" {
			return cacheHitTypeSubresource, nil
		}

		snapshotName := diffcache.VerbToSnapshotName[message.Verb]
		tryOnce = func(ctx context.Context) (shouldRetry bool, err error) {
			return decorator.tryCreateDeleteOnce(ctx, object, snapshotName, event)
		}
		// try response object if it exists
	default:
		return cacheHitTypeUnsupportedVerb, nil
	}

	// this context will interrupt the Fetch call
	totalCtx, totalCancelFunc := context.WithTimeout(ctx, decorator.options.fetchTotalTimeout)
	defer totalCancelFunc()

	// this context only interrupts PollImmediateUntilWithContext
	retryCtx, retryCancelFunc := context.WithDeadline(totalCtx, message.StageTimestamp.Time.Add(decorator.options.fetchEventTimeout))
	defer retryCancelFunc()

	var retryCount int64
	var cacheHit bool
	var err error

	// the implementation never returns error
	_ = wait.PollUntilContextCancel(retryCtx, decorator.options.fetchBackoff, true, func(context.Context) (done bool, _ error) {
		retryCount += 1
		cacheHit, err = tryOnce(totalCtx)
		return cacheHit, nil
	})

	decorator.RetryCountMetric.With(&retryCountMetric{
		Cluster: message.Cluster,
		ApiGroup: schema.GroupVersion{
			Group:   message.ObjectRef.APIGroup,
			Version: message.ObjectRef.APIVersion,
		},
		Resource: message.ObjectRef.Resource,
		Success:  cacheHit,
	}).Histogram(retryCount)

	if cacheHit {
		return cacheHitTypeTrue, nil
	} else {
		logger.WithField("object", message.ObjectRef).Warn("cannot associate diff cache")
		return cacheHitTypeFalse, err
	}
}

func decoratesResource(message *audit.Message) bool {
	return message.Verb != audit.VerbPatch
}

func (decorator *decorator) tryUpdateOnce(
	ctx context.Context,
	object util.ObjectRef,
	oldRv string,
	newRv *string,
	event *aggregatorevent.Event,
	message *audit.Message,
) (bool, error) {
	var err error
	patch, err := decorator.Cache.Fetch(ctx, object, oldRv, newRv)
	if err != nil || patch == nil {
		return false, err
	}

	// fetch patch success, write to event

	if patch.Redacted {
		event.Log(zconstants.LogTypeKelemetryError, "Sensitive object content has been redacted")
		return true, nil
	}

	diffInfo := ""
	for _, diff := range patch.DiffList.Diffs {
		diffInfo += fmt.Sprintf("%s %#v -> %#v\n", diff.JsonPath, diff.Old, diff.New)
	}
	event.Log(zconstants.LogTypeObjectDiff, diffInfo)

	informerLatency := patch.InformerTime.Sub(message.StageTimestamp.Time)
	decorator.InformerLatencyMetric.With(&informerLatencyMetric{
		Cluster: message.Cluster,
		ApiGroup: schema.GroupVersion{
			Group:   message.ObjectRef.APIGroup,
			Version: message.ObjectRef.APIVersion,
		},
		Resource: message.ObjectRef.Resource,
	}).Histogram(informerLatency.Nanoseconds())
	event.WithTag("informer latency", informerLatency)

	return true, nil
}

func (decorator *decorator) tryCreateDeleteOnce(
	ctx context.Context,
	object util.ObjectRef,
	snapshotName string,
	event *aggregatorevent.Event,
) (bool, error) {
	snapshot, err := decorator.Cache.FetchSnapshot(ctx, object, snapshotName)
	if err != nil || snapshot == nil {
		return false, err
	}

	if !snapshot.Redacted {
		event.Log(zconstants.LogTypeObjectSnapshot, string(snapshot.Value))
	}

	return true, nil
}
