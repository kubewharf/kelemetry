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

package auditconsumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/audit/mq"
	"github.com/kubewharf/kelemetry/pkg/filter"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("audit-consumer", manager.Ptr(&receiver{
		consumers: map[mq.PartitionId]mq.Consumer{},
	}))
}

type options struct {
	enable            bool
	consumerGroup     string
	partitions        []int32
	clusterFilter     string
	ignoreImpersonate bool
	enableSubObject   bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "audit-consumer-enable", false, "enable audit consumer")
	fs.StringVar(&options.consumerGroup, "audit-consumer-group", "kelemetry", "audit consumer group name")
	fs.Int32SliceVar(&options.partitions, "audit-consumer-partition", []int32{0, 1, 2, 3, 4}, "audit message queue partitions to consume")
	fs.StringVar(
		&options.clusterFilter,
		"audit-consumer-filter-cluster-name",
		"",
		"if nonempty, only audit events with this cluster name are processed",
	)
	fs.BoolVar(
		&options.ignoreImpersonate,
		"audit-consumer-ignore-impersonate-username",
		false,
		"if set to true, direct username is always used even with impersonation",
	)
	fs.BoolVar(&options.enableSubObject, "audit-consumer-group-failures", false, "whether failed requests should be grouped together")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type receiver struct {
	options        options
	Logger         logrus.FieldLogger
	Clock          clock.Clock
	Aggregator     aggregator.Aggregator
	Mq             mq.Queue
	DecoratorList  *manager.List[audit.Decorator]
	Filter         filter.Filter
	Metrics        metrics.Client
	DiscoveryCache discovery.DiscoveryCache

	ConsumeMetric    *metrics.Metric[*consumeMetric]
	E2eLatencyMetric *metrics.Metric[*e2eLatencyMetric]

	consumers map[mq.PartitionId]mq.Consumer
}

var _ manager.Component = &receiver{}

type consumeMetric struct {
	Cluster       string
	ConsumerGroup mq.ConsumerGroup
	Partition     mq.PartitionId
	HasTrace      bool
}

func (*consumeMetric) MetricName() string { return "audit_consumer_event" }

type e2eLatencyMetric struct {
	Cluster  string
	ApiGroup schema.GroupVersion
	Resource string
}

func (*e2eLatencyMetric) MetricName() string { return "audit_consumer_e2e_latency" }

func (recv *receiver) Options() manager.Options {
	return &recv.options
}

func (recv *receiver) Init() error {
	for _, partition := range recv.options.partitions {
		group := mq.ConsumerGroup(recv.options.consumerGroup)
		partition := mq.PartitionId(partition)
		consumer, err := recv.Mq.CreateConsumer(
			group,
			partition,
			func(ctx context.Context, fieldLogger logrus.FieldLogger, msgKey []byte, msgValue []byte) {
				recv.handleMessage(ctx, fieldLogger, msgKey, msgValue, group, partition)
			},
		)
		if err != nil {
			return fmt.Errorf("cannot create consumer for partition %d: %w", partition, err)
		}
		recv.consumers[partition] = consumer
	}

	return nil
}

func (recv *receiver) Start(ctx context.Context) error { return nil }
func (recv *receiver) Close(ctx context.Context) error { return nil }

func (recv *receiver) handleMessage(
	ctx context.Context,
	fieldLogger logrus.FieldLogger,
	msgKey []byte,
	msgValue []byte,
	consumerGroup mq.ConsumerGroup,
	partition mq.PartitionId,
) {
	logger := fieldLogger.WithField("mod", "audit-consumer")

	metric := &consumeMetric{
		ConsumerGroup: consumerGroup,
		Partition:     partition,
	}
	defer recv.ConsumeMetric.DeferCount(recv.Clock.Now(), metric)

	// The first part of the message key is always the cluster no matter what partitioning method we use.
	cluster := strings.SplitN(string(msgKey), "/", 2)[0]
	metric.Cluster = cluster

	if recv.options.clusterFilter != "" && recv.options.clusterFilter != cluster {
		return
	}

	message := &audit.Message{}
	if err := json.Unmarshal(msgValue, message); err != nil {
		logger.WithError(err).Error("error decoding audit data")
		return
	}

	recv.handleItem(ctx, logger.WithField("auditId", message.Event.AuditID), message, metric)
}

var supportedVerbs = sets.NewString(
	audit.VerbCreate,
	audit.VerbUpdate,
	audit.VerbDelete,
	audit.VerbPatch,
)

func (recv *receiver) handleItem(
	ctx context.Context,
	logger logrus.FieldLogger,
	message *audit.Message,
	metric *consumeMetric,
) {
	defer shutdown.RecoverPanic(logger)

	if !supportedVerbs.Has(message.Verb) {
		return
	}

	if message.Stage != auditv1.StageResponseComplete {
		return
	}

	if !recv.Filter.TestAuditEvent(&message.Event) {
		return
	}

	if message.ObjectRef == nil || message.ObjectRef.Name == "" {
		// try reconstructing ObjectRef from ResponseObject
		if err := recv.inferObjectRef(message); err != nil {
			logger.WithError(err).Debug("Invalid objectRef, cannot infer")
			return
		}
	}

	objectRef := utilobject.RichFromAudit(message.ObjectRef, message.Cluster)

	if message.ResponseObject != nil {
		objectRef.Raw = &unstructured.Unstructured{
			Object: map[string]any{},
		}

		err := json.Unmarshal(message.ResponseObject.Raw, &objectRef.Raw.Object)
		if err != nil {
			logger.Errorf("cannot decode responseObject: %s", err.Error())
			return
		}
	}

	e2eLatency := recv.Clock.Since(message.StageTimestamp.Time)

	fieldLogger := logger.
		WithField("verb", message.Verb).
		WithField("cluster", message.Cluster).
		WithField("resource", message.ObjectRef.Resource).
		WithField("namespace", message.ObjectRef.Namespace).
		WithField("name", message.ObjectRef.Name).
		WithField("latency", e2eLatency)

	username := message.User.Username
	if message.ImpersonatedUser != nil && !recv.options.ignoreImpersonate {
		username = message.ImpersonatedUser.Username
	}
	title := fmt.Sprintf("%s %s", username, message.Verb)
	if message.ObjectRef.Subresource != "" {
		title += fmt.Sprintf(" %s", message.ObjectRef.Subresource)
	}
	if message.ResponseStatus.Code >= 300 {
		title += fmt.Sprintf(" (%s)", http.StatusText(int(message.ResponseStatus.Code)))
	}

	event := aggregatorevent.NewEvent(title, message.RequestReceivedTimestamp.Time, zconstants.TraceSourceAudit).
		SetEndTime(message.StageTimestamp.Time).
		SetTag("auditId", message.AuditID).
		SetTag("username", username).
		SetTag("userAgent", message.UserAgent).
		SetTag("responseCode", message.ResponseStatus.Code).
		SetTag("resourceVersion", message.ObjectRef.ResourceVersion).
		SetTag("apiserver", message.ApiserverAddr).
		SetTag("tag", message.Verb)

	if len(message.SourceIPs) > 0 {
		event = event.SetTag("sourceIP", message.SourceIPs[0])

		if len(message.SourceIPs) > 1 {
			event = event.SetTag("proxy", message.SourceIPs[1:])
		}
	}

	for _, decorator := range recv.DecoratorList.Impls {
		decorator.Decorate(ctx, message, event)
	}

	recv.E2eLatencyMetric.With(&e2eLatencyMetric{
		Cluster:  message.Cluster,
		ApiGroup: objectRef.GroupVersion(),
		Resource: objectRef.Resource,
	}).Summary(float64(e2eLatency.Nanoseconds()))

	err := recv.Aggregator.Send(ctx, objectRef, event)
	if err != nil {
		fieldLogger.WithError(err).Error()
	} else {
		fieldLogger.Debug("Send")
	}

	metric.HasTrace = true
}

func (receiver *receiver) inferObjectRef(message *audit.Message) error {
	if message.ResponseObject != nil {
		var partial metav1.PartialObjectMetadata
		if err := json.Unmarshal(message.ResponseObject.Raw, &partial); err != nil {
			return fmt.Errorf("unmarshal raw object: %w", err)
		}

		gv, err := schema.ParseGroupVersion(partial.APIVersion)
		if err != nil {
			return fmt.Errorf("object has invalid GroupVersion: %w", err)
		}

		cdc, err := receiver.DiscoveryCache.ForCluster(message.Cluster)
		if err != nil {
			return fmt.Errorf("no ClusterDiscoveryCache: %w", err)
		}

		gvr, ok := cdc.LookupResource(gv.WithKind(partial.Kind))
		if !ok {
			return fmt.Errorf("conversion of response to GVR failed")
		}

		message.ObjectRef = &auditv1.ObjectReference{
			APIGroup:   gvr.Group,
			APIVersion: gvr.Version,
			Resource:   gvr.Resource,
			Namespace:  partial.Namespace,
			Name:       partial.Name,
			UID:        partial.UID,
			// do not set as partial.ResourceVersion, otherwise diff decorator will think this is a no-op
			ResourceVersion: "",
			// TODO try to infer subresource from RequestObject
			Subresource: "",
		}

		return nil
	}

	return fmt.Errorf("no ResponseObject to infer objectRef from")
}
