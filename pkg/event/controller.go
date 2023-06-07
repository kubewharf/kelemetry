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

package event

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/filter"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/multileader"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	informerutil "github.com/kubewharf/kelemetry/pkg/util/informer"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

const (
	configMapLastEventKey   = "timestamp"
	configMapLastUpdatedKey = "lastUpdated"
)

func init() {
	manager.Global.Provide("event-informer", manager.Ptr(&controller{
		configMapUpdateCh: make(chan func(*corev1.ConfigMap), 50),
	}))
}

type options struct {
	enable                bool
	configMapSyncInterval time.Duration
	electorOptions        multileader.Config
	configMapName         string
	configMapNamespace    string
	workerCount           int
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(
		&options.enable,
		"event-informer-enable",
		false,
		"enable event informer",
	)
	fs.DurationVar(
		&options.configMapSyncInterval,
		"event-informer-configmap-sync-interval",
		time.Second*5,
		"interval to update the ConfigMap that stores the last logged event",
	)
	fs.StringVar(
		&options.configMapName,
		"event-informer-configmap-name",
		"kelemetry-event-controller",
		"name of the ConfigMap that stores the last logged event",
	)
	fs.StringVar(
		&options.configMapNamespace,
		"event-informer-configmap-namespace",
		"default",
		"namespace of the ConfigMap that stores the last logged event",
	)
	fs.IntVar(
		&options.workerCount,
		"event-informer-worker-count",
		8,
		"number of worker counts",
	)
	options.electorOptions.SetupOptions(
		fs,
		"event-informer",
		"event controller",
		1,
	)
}

func (options *options) EnableFlag() *bool { return &options.enable }

type controller struct {
	options        options
	Logger         logrus.FieldLogger
	Clock          clock.Clock
	Aggregator     aggregator.Aggregator
	Clients        k8s.Clients
	Filter         filter.Filter
	Metrics        metrics.Client
	DiscoveryCache discovery.DiscoveryCache

	EventHandleMetric  *metrics.Metric[*eventHandleMetric]
	EventLatencyMetric *metrics.Metric[*eventLatencyMetric]

	elector           *multileader.Elector
	isLeader          uint32
	shutdownWg        sync.WaitGroup
	configMapClient   corev1client.ConfigMapInterface
	lastEventTime     time.Time
	configMapUpdateCh chan func(*corev1.ConfigMap)
}

var _ manager.Component = &controller{}

type eventHandleMetric struct {
	Group         string
	Version       string
	Kind          string
	Resource      string
	TimestampType string
	Error         metrics.LabeledError
}

func (*eventHandleMetric) MetricName() string { return "event_handle" }

type eventLatencyMetric struct {
	Group    string
	Version  string
	Resource string
}

func (*eventLatencyMetric) MetricName() string { return "event_latency" }

func (ctrl *controller) Options() manager.Options {
	return &ctrl.options
}

func (ctrl *controller) Init() (err error) {
	client := ctrl.Clients.TargetCluster()

	ctrl.elector, err = multileader.NewElector(
		"kelemetry-event-controller",
		ctrl.Logger.WithField("submod", "leader-elector"),
		ctrl.Clock,
		&ctrl.options.electorOptions,
		client,
		ctrl.Metrics,
	)
	if err != nil {
		return fmt.Errorf("cannot create leader elector: %w", err)
	}

	ctrl.configMapClient = client.KubernetesClient().CoreV1().ConfigMaps(ctrl.options.configMapNamespace)

	return nil
}

func (ctrl *controller) Start(ctx context.Context) error {
	go ctrl.elector.Run(ctx, ctrl.runLeader)
	go ctrl.elector.RunLeaderMetricLoop(ctx)

	return nil
}

func (ctrl *controller) Close(ctx context.Context) error {
	ctrl.shutdownWg.Wait()
	return nil
}

func (ctrl *controller) runLeader(ctx context.Context) {
	atomic.StoreUint32(&ctrl.isLeader, 1)
	defer atomic.StoreUint32(&ctrl.isLeader, 0)

	startReadyCh := make(chan struct{})

	eventClient := ctrl.Clients.TargetCluster().KubernetesClient().CoreV1().Events(metav1.NamespaceAll)
	lw := toolscache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return eventClient.List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return eventClient.Watch(ctx, options)
		},
	}

	store := informerutil.NewDecayingInformer[*corev1.Event]()
	addCh := store.SetAddCh()
	replaceCh := store.SetReplaceCh()

	reflector := toolscache.NewReflector(&lw, &corev1.Event{}, store, 0)
	go func() {
		defer shutdown.RecoverPanic(ctrl.Logger)
		reflector.Run(ctx.Done())
	}()

	for workerId := 0; workerId < ctrl.options.workerCount; workerId++ {
		go func(workerId int) {
			defer shutdown.RecoverPanic(ctrl.Logger.WithField("worker", workerId))

			<-startReadyCh

			for {
				select {
				case event := <-addCh:
					ctrl.handleEvent(ctx, event)
				case event := <-replaceCh:
					ctrl.handleEvent(ctx, event)
				case <-ctx.Done():
					return
				}
			}
		}(workerId)
	}

	ctrl.syncConfigMap(ctx, startReadyCh)
}

func (ctrl *controller) handleEvent(ctx context.Context, event *corev1.Event) {
	isLeader := atomic.LoadUint32(&ctrl.isLeader)
	if isLeader == 0 {
		return
	}

	metric := &eventHandleMetric{
		Group:   event.InvolvedObject.GroupVersionKind().Group,
		Version: event.InvolvedObject.GroupVersionKind().Version,
		Kind:    event.InvolvedObject.Kind,
	}
	defer ctrl.EventHandleMetric.DeferCount(ctrl.Clock.Now(), metric)

	logger := ctrl.Logger.WithField("event", event.Name).WithField("subject", event.InvolvedObject)

	eventTime := event.EventTime.Time
	metric.TimestampType = "EventTime"
	if eventTime.IsZero() {
		eventTime = event.LastTimestamp.Time
		metric.TimestampType = "LastTimestamp"
	}
	if eventTime.IsZero() {
		metric.TimestampType = ""
		metric.Error = metrics.MakeLabeledError("InferTimestamp")
		logger.WithField("object", event).Warn("cannot infer timestamp")
		return
	}

	if eventTime.Before(ctrl.lastEventTime) {
		metric.Error = metrics.MakeLabeledError("BeforeRestart")
		return
	}

	if ctrl.Clock.Since(eventTime) > 5*time.Minute {
		metric.Error = metrics.MakeLabeledError("EventTooOld")
		return
	}

	logger = logger.WithField("eventTime", eventTime)
	ctrl.configMapUpdateCh <- func(cm *corev1.ConfigMap) {
		t, err := time.Parse(time.RFC3339, cm.Data[configMapLastEventKey])
		if err != nil || t.Before(eventTime) {
			cm.Data[configMapLastEventKey] = eventTime.Format(time.RFC3339)
		}
	}

	clusterName := ctrl.Clients.TargetCluster().ClusterName()

	if !ctrl.Filter.TestGvk(clusterName, event.InvolvedObject.GroupVersionKind()) {
		metric.Error = metrics.MakeLabeledError("Filtered")
		return
	}

	aggregatorEvent := aggregatorevent.NewEvent(zconstants.NestLevelStatus, event.Reason, eventTime, zconstants.TraceSourceEvent).
		WithTag("source", event.Source.Component).
		WithTag("action", event.Action).
		Log(zconstants.LogTypeEventMessage, event.Message)

	cdc, err := ctrl.DiscoveryCache.ForCluster(clusterName)
	if err != nil {
		logger.WithError(err).Error("cannot init discovery cache for target cluster")
		metric.Error = metrics.MakeLabeledError("invalid cluster")
		return
	}
	gvr, found := cdc.LookupResource(event.InvolvedObject.GroupVersionKind())
	if !found {
		logger.WithField("gvk", event.InvolvedObject.GroupVersionKind()).Error("unknown gvk")
		metric.Error = metrics.MakeLabeledError("UnknownGVK")
		return
	}

	metric.Resource = gvr.Resource

	ctrl.EventLatencyMetric.With(&eventLatencyMetric{
		Group:    gvr.Group,
		Version:  gvr.Version,
		Resource: gvr.Resource,
	}).Histogram(ctrl.Clock.Since(eventTime).Nanoseconds())

	if err := ctrl.Aggregator.Send(ctx, util.ObjectRef{
		Cluster:              clusterName,
		GroupVersionResource: gvr,
		Namespace:            event.InvolvedObject.Namespace,
		Name:                 event.InvolvedObject.Name,
		Uid:                  event.InvolvedObject.UID,
	}, aggregatorEvent, nil); err != nil {
		logger.WithError(err).Error("Cannot send trace")
		metric.Error = metrics.LabelError(err, "SendTrace")
		return
	}

	logger.Debug("Send")
}

func (ctrl *controller) syncConfigMap(ctx context.Context, startReadyCh chan<- struct{}) {
	logger := ctrl.Logger.WithField("subcomponent", "syncConfigMap")

	ctrl.shutdownWg.Add(1)
	defer ctrl.shutdownWg.Done()

	defer shutdown.RecoverPanic(logger)

	ctx, cancelFunc := context.WithCancel(ctx)
	defer cancelFunc()

	configMap, err := ctrl.configMapClient.Get(ctx, ctrl.options.configMapName, metav1.GetOptions{ResourceVersion: "0"})
	if k8serrors.IsNotFound(err) {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ctrl.options.configMapNamespace,
				Name:      ctrl.options.configMapName,
			},
			Data: map[string]string{
				configMapLastEventKey: ctrl.Clock.Now().Format(time.RFC3339),
			},
		}
		configMap, err = ctrl.configMapClient.Create(ctx, configMap, metav1.CreateOptions{})
		if err != nil {
			logger.WithError(err).Error("Cannot initialize ConfigMap")
			return
		}
	} else if err != nil {
		logger.WithError(err).Error("Cannot get ConfigMap")
		return
	}

	ctrl.lastEventTime, err = time.Parse(time.RFC3339, configMap.Data[configMapLastEventKey])
	if err != nil {
		logger.WithError(err).Error("Previous event time was invalid, resetting to current timestamp")
		ctrl.lastEventTime = ctrl.Clock.Now()
	}
	logger.WithField("startEventTime", ctrl.lastEventTime).Info("Start tracing events")

	close(startReadyCh) // worker queue can start running now

	for {
		changed, shutdown := ctrl.pollConfigLoop(ctx, ctrl.options.configMapSyncInterval, configMap, ctrl.configMapUpdateCh)

		if shutdown {
			break
		}

		if changed {
			configMap.Data[configMapLastUpdatedKey] = ctrl.Clock.Now().Format(time.RFC3339)
			newConfigMap, err := ctrl.configMapClient.Update(ctx, configMap, metav1.UpdateOptions{})
			if err != nil {
				logger.WithField("resourceVersion", configMap.ResourceVersion).WithError(err).Error("Cannot update ConfigMap")
				if k8serrors.IsConflict(err) {
					// penetrate the cache, since we're out-of-sync anyway
					configMap, err = ctrl.configMapClient.Get(ctx, ctrl.options.configMapName, metav1.GetOptions{})
					if err != nil {
						logger.WithError(err).Error("Cannot refresh ConfigMap")
					} else {
						logger.WithField("resourceVersion", configMap.ResourceVersion).Info("Refreshed ConfigMap")
					}
				}
			} else {
				configMap = newConfigMap
			}
		}
	}

	_, err = ctrl.configMapClient.Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		logger.WithError(err).Error("Cannot finalize ConfigMap")
	}
}

func (ctrl *controller) pollConfigLoop(
	ctx context.Context,
	interval time.Duration,
	configMap *corev1.ConfigMap,
	updateCh <-chan func(*corev1.ConfigMap),
) (_changed, _shutdown bool) {
	changed := false

	until := ctrl.Clock.After(interval)

	for {
		// ctx and until have higher priority than updateCh
		select {
		case <-ctx.Done():
			return changed, true
		case <-until:
			return changed, false
		default:
		}

		select {
		case <-ctx.Done():
			return changed, true
		case <-until:
			return changed, false
		case updater := <-updateCh:
			changed = true
			updater(configMap)
		}
	}
}
