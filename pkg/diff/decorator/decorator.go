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
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/kelemetry/pkg/aggregator"
	"github.com/kubewharf/kelemetry/pkg/audit"
	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("diff-decorator", newDecorator)
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
		time.Minute,
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
	logger  logrus.FieldLogger
	cache   diffcache.Cache
	list    audit.DecoratorList
	metrics metrics.Client

	ctx                   context.Context
	diffMetric            metrics.Metric
	informerLatencyMetric metrics.Metric
	retryCountMetric      metrics.Metric
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

type informerLatencyMetric struct {
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
}

type retryCountMetric struct {
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
	Success  bool
}

func newDecorator(
	logger logrus.FieldLogger,
	cache diffcache.Cache,
	list audit.DecoratorList,
	metrics metrics.Client,
) *decorator {
	return &decorator{
		logger:  logger,
		cache:   cache,
		list:    list,
		metrics: metrics,
	}
}

func (decorator *decorator) Options() manager.Options {
	return &decorator.options
}

func (decorator *decorator) Init(ctx context.Context) error {
	decorator.ctx = ctx
	decorator.list.AddDecorator(decorator)
	decorator.diffMetric = decorator.metrics.New("diff_decorator", &diffMetric{})
	decorator.informerLatencyMetric = decorator.metrics.New("diff_informer_latency", &informerLatencyMetric{})
	decorator.retryCountMetric = decorator.metrics.New("diff_decorator_retry_count", &retryCountMetric{})
	return nil
}

func (decorator *decorator) Start(stopCh <-chan struct{}) error {
	return nil
}

func (decorator *decorator) Close() error {
	return nil
}

func (decorator *decorator) Decorate(message *audit.Message, event *aggregator.Event) {
	logger := decorator.logger.
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
	defer decorator.diffMetric.DeferCount(time.Now(), metric)

	if message.ResponseStatus != nil && message.ResponseStatus.Code >= 300 {
		event.Log(zconstants.LogTypeRealError, fmt.Sprintf("%s: %s", message.ResponseStatus.Reason, message.ResponseStatus.Message))
		if message.ResponseStatus.Details != nil {
			for _, detail := range message.ResponseStatus.Details.Causes {
				event.Log(zconstants.LogTypeRealError, fmt.Sprintf("%s: %s", detail.Type, detail.Message))
			}
		}

		metric.CacheHit = "error"
		return
	}

	if message.ObjectRef == nil {
		logger.Warn("audit event has no ObjectRef")
		metric.CacheHit = "noObjectRef"
		return
	}

	var respObj *metav1.PartialObjectMetadata
	if message.ResponseObject != nil && message.ResponseObject.Raw != nil {
		if err := json.Unmarshal(message.ResponseObject.Raw, &respObj); err != nil {
			event.Log(zconstants.LogTypeKelemetryError, "Error decoding raw object")
			metric.CacheHit = "rawJsonDecodeError"
			metric.Error = metrics.LabelError(err, "RawJsonDecodeError")
			respObj = nil
		}
	}

	hasDiff := message.Verb == audit.VerbUpdate || message.Verb == audit.VerbPatch
	if hasDiff && respObj != nil && message.ObjectRef.ResourceVersion == respObj.ResourceVersion {
		event.Log(zconstants.LogTypeRealVerbose, "No-op update")
		return
	}

	if !decoratesResource(message.ObjectRef) {
		metric.CacheHit = "filtered"
		return
	}

	// NOTE: UID may be empty, but we don't use it anyway
	object := util.ObjectRefFromAudit(message.ObjectRef, message.Cluster, message.ObjectRef.UID)

	var tryOnce func(context.Context) (bool, error)

	switch message.Verb {
	case audit.VerbUpdate, audit.VerbPatch:
		oldRv := message.ObjectRef.ResourceVersion

		var newRv *string
		if respObj != nil && respObj.ResourceVersion != "" {
			newRv = pointer.String(respObj.ResourceVersion)
		}

		if newRv != nil && oldRv == *newRv {
			metric.CacheHit = "sameRv"
			event.Log(zconstants.LogTypeRealVerbose, "No-op update")
			return
		}

		logger = logger.WithField("oldRv", oldRv).WithField("newRv", newRv)

		tryOnce = func(ctx context.Context) (bool, error) {
			return decorator.tryUpdateOnce(ctx, object, oldRv, newRv, event, message)
		}

	case audit.VerbCreate, audit.VerbDelete:
		snapshotName, hasSnapshotName := diffcache.VerbToSnapshotName[message.Verb]
		if !hasSnapshotName {
			// TODO fallback to ResponseObject?
			return
		}

		tryOnce = func(ctx context.Context) (shouldRetry bool, err error) {
			return decorator.tryCreateDeleteOnce(ctx, object, snapshotName, event)
		}
	default:
		metric.CacheHit = "unsupportedVerb"
		return
	}

	// this context will interrupt the Fetch call
	totalCtx, totalCancelFunc := context.WithTimeout(decorator.ctx, decorator.options.fetchTotalTimeout)
	defer totalCancelFunc()

	// this context only interrupts PollImmediateUntilWithContext
	retryCtx, retryCancelFunc := context.WithDeadline(totalCtx, message.StageTimestamp.Time.Add(decorator.options.fetchEventTimeout))
	defer retryCancelFunc()

	var retryCount int64
	var cacheHit bool

	// the implementation never returns error
	_ = wait.PollImmediateUntil(decorator.options.fetchBackoff, func() (done bool, _ error) {
		retryCount += 1
		cacheHit, metric.Error = tryOnce(totalCtx)
		return cacheHit, nil
	}, retryCtx.Done())

	decorator.retryCountMetric.With(&retryCountMetric{
		Cluster:  metric.Cluster,
		ApiGroup: metric.ApiGroup,
		Resource: metric.Resource,
		Success:  cacheHit,
	}).Histogram(retryCount)
	metric.CacheHit = fmt.Sprint(cacheHit)

	if cacheHit {
		metric.CacheHit = "true"
	} else {
		logger.WithField("object", message.ObjectRef).Warn("cannot associate diff cache")
		event.Log(zconstants.LogTypeKelemetryError, "No object diff found")
		return
	}
}

func decoratesResource(objectRef *auditv1.ObjectReference) bool { return true } // TODO

func (decorator *decorator) tryUpdateOnce(
	ctx context.Context,
	object util.ObjectRef,
	oldRv string,
	newRv *string,
	event *aggregator.Event,
	message *audit.Message,
) (bool, error) {
	var err error
	patch, err := decorator.cache.Fetch(ctx, object, oldRv, newRv)
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
	decorator.informerLatencyMetric.With(&informerLatencyMetric{
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
	event *aggregator.Event,
) (bool, error) {
	snapshot, err := decorator.cache.FetchSnapshot(ctx, object, snapshotName)
	if err != nil || snapshot == nil {
		return false, err
	}

	if !snapshot.Redacted {
		event.Log(zconstants.LogTypeObjectSnapshot, string(snapshot.Value))
	}

	return true, nil
}
