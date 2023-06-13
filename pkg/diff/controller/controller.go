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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"

	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	diffcmp "github.com/kubewharf/kelemetry/pkg/diff/cmp"
	"github.com/kubewharf/kelemetry/pkg/filter"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/multileader"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
	"github.com/kubewharf/kelemetry/pkg/util/channel"
	"github.com/kubewharf/kelemetry/pkg/util/informer"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

const (
	LabelKeyRedacted = "kelemetry.kubewharf.io/diff-redacted"
)

func init() {
	manager.Global.Provide("diff-controller", manager.Ptr(&controller{
		monitors: map[schema.GroupVersionResource]*monitor{},
		taskPool: channel.NewUnboundedQueue[workerTask](16),
	}))
}

type (
	workerTask = func() writerTask
	writerTask = func(context.Context)
)

type ctrlOptions struct {
	enable           bool
	redact           string
	deletionSnapshot bool

	storeTimeout time.Duration
	workerCount  int

	watchElectorOptions multileader.Config
	writeElectorOptions multileader.Config
}

func (options *ctrlOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "diff-controller-enable", false, "enable controller for watching and computing object update diff")
	fs.StringVar(
		&options.redact,
		"diff-controller-redact-pattern",
		"$this matches nothing^",
		"only informer time and resource version are traced for objects matching this regexp pattern in the form g/v/r/ns/name",
	)
	fs.BoolVar(&options.deletionSnapshot, "diff-controller-deletion-snapshot", true, "take a snapshot of objects during deletion")
	fs.DurationVar(&options.storeTimeout, "diff-controller-store-timeout", time.Second*10, "timeout for storing cache")
	fs.IntVar(&options.workerCount, "diff-controller-worker-count", 8, "number of workers for all object types to compute diff")
	options.watchElectorOptions.SetupOptions(fs, "diff-controller", "diff controller", 2)
	options.writeElectorOptions.SetupOptions(fs, "diff-writer", "diff controller cache writer", 1)
}

func (options *ctrlOptions) EnableFlag() *bool { return &options.enable }

type controller struct {
	options   ctrlOptions
	Logger    logrus.FieldLogger
	Clock     clock.Clock
	Clients   k8s.Clients
	Discovery discovery.DiscoveryCache
	Cache     diffcache.Cache
	Filter    filter.Filter
	Metrics   metrics.Client

	redactRegex       *regexp.Regexp
	discoveryResyncCh <-chan struct{}
	OnCreateMetric    *metrics.Metric[*onCreateMetric]
	OnUpdateMetric    *metrics.Metric[*onUpdateMetric]
	OnDeleteMetric    *metrics.Metric[*onDeleteMetric]
	watchElector      *multileader.Elector
	monitors          map[schema.GroupVersionResource]*monitor
	monitorsLock      sync.RWMutex

	// A queue of tasks to execute in each worker.
	// Returns a function that must be executed once writer lease is acquired.
	taskPool *channel.UnboundedQueue[workerTask]

	// Indicates how many active leader terms we acquired.
	// Might be greater than 1 if we lost and immediately re-acquired leader lease.
	isWriteLeader       atomic.Int32
	setWriteLeaderChans []chan<- bool
}

var _ manager.Component = &controller{}

type onCreateMetric struct {
	ApiGroup schema.GroupVersion
	Resource string
}

func (*onCreateMetric) MetricName() string { return "diff_controller_on_create" }

type onUpdateMetric struct {
	ApiGroup schema.GroupVersion
	Resource string
}

func (*onUpdateMetric) MetricName() string { return "diff_controller_on_update" }

type onDeleteMetric struct {
	ApiGroup schema.GroupVersion
	Resource string
}

func (*onDeleteMetric) MetricName() string { return "diff_controller_on_delete" }

type taskPoolMetric struct{}

func (*taskPoolMetric) MetricName() string { return "diff_controller_task_pool" }

func (ctrl *controller) Options() manager.Options {
	return &ctrl.options
}

func (ctrl *controller) Init() (err error) {
	ctrl.redactRegex, err = regexp.Compile(ctrl.options.redact)
	if err != nil {
		return fmt.Errorf("cannot compile --diff-controller-redact-pattern value: %w", err)
	}

	cdc, err := ctrl.Discovery.ForCluster(ctrl.Clients.TargetCluster().ClusterName())
	if err != nil {
		return fmt.Errorf("cannot initialize discovery cache for target cluster: %w", err)
	}

	ctrl.discoveryResyncCh = cdc.AddResyncHandler()

	ctrl.watchElector, err = multileader.NewElector(
		"kelemetry-diff-controller",
		ctrl.Logger.WithField("submod", "watch-leader-elector"),
		ctrl.Clock,
		&ctrl.options.watchElectorOptions,
		ctrl.Clients.TargetCluster(),
		ctrl.Metrics,
	)
	if err != nil {
		return fmt.Errorf("cannot create watch leader elector: %w", err)
	}

	channel.InitMetricLoop(ctrl.taskPool, ctrl.Metrics, &taskPoolMetric{})

	return nil
}

func (ctrl *controller) Start(ctx context.Context) error {
	setWriteLeaderChans := make([]chan<- bool, ctrl.options.workerCount)

	for i := 0; i < ctrl.options.workerCount; i++ {
		ch := make(chan bool, 1)
		setWriteLeaderChans[i] = ch
		go runWorker(ctx, ctrl.Logger.WithField("worker", i), ctrl.taskPool, ctrl.options.watchElectorOptions.LeaseDuration, ch)
	}

	ctrl.setWriteLeaderChans = setWriteLeaderChans

	go ctrl.watchElector.Run(ctx, ctrl.resyncMonitorsLoop)
	go ctrl.watchElector.RunLeaderMetricLoop(ctx)

	return nil
}

func runWorker(
	ctx context.Context,
	logger logrus.FieldLogger,
	taskPool *channel.UnboundedQueue[workerTask],
	retentionPeriod time.Duration,
	setWriteLeader <-chan bool,
) {
	defer shutdown.RecoverPanic(logger)

	type timedFunc struct {
		expiry time.Time
		fn     writerTask
	}

	isWriteLeader := false
	writeQueue := channel.NewDeque[timedFunc](16)

	for {
		select {
		case <-ctx.Done():
			return
		case task, ok := <-taskPool.Receiver():
			if !ok {
				return
			}

			writer := task()

			if writer != nil {
				if isWriteLeader {
					writer(ctx)
				} else {
					if first, hasFirst := writeQueue.LockedPeekFront(); hasFirst {
						if time.Now().After(first.expiry) { // expired
							writeQueue.LockedPopFront()
						}
					}
					writeQueue.LockedPushBack(timedFunc{
						expiry: time.Now().Add(retentionPeriod),
						fn:     writer,
					})
				}
			}
		case newValue := <-setWriteLeader:
			isWriteLeader = newValue
			oldWriteQueue := writeQueue
			writeQueue = channel.NewDeque[timedFunc](16)

			for _, slice := range oldWriteQueue.LockedGetAll() {
				for _, fn := range slice {
					fn.fn(ctx)
				}
			}
		}
	}
}

func (ctrl *controller) Close(ctx context.Context) error { return nil }

func (ctrl *controller) broadcastWriteLeader(b bool) {
	for _, ch := range ctrl.setWriteLeaderChans {
		go func(ch chan<- bool, b bool) {
			ch <- b
		}(ch, b)
	}
}

func (ctrl *controller) resyncMonitorsLoop(ctx context.Context) {
	logger := ctrl.Logger.WithField("submod", "resync")

	defer shutdown.RecoverPanic(logger)

	writeElector, err := multileader.NewElector(
		"kelemetry-diff-cache-writer",
		ctrl.Logger.WithField("submod", "write-leader-elector"),
		ctrl.Clock,
		&ctrl.options.writeElectorOptions,
		ctrl.Clients.TargetCluster(),
		ctrl.Metrics,
	)
	if err != nil {
		logger.WithError(err).Error("cannot create write leader elector")
		return
	}

	go writeElector.Run(ctx, func(ctx context.Context) {
		count := ctrl.isWriteLeader.Add(1)
		if count == 1 {
			ctrl.broadcastWriteLeader(true)
		}

		defer func() {
			count := ctrl.isWriteLeader.Add(-1)
			if count == 0 {
				ctrl.broadcastWriteLeader(false)
			}
		}()

		<-ctx.Done()
		ctrl.Clock.Sleep(ctrl.options.watchElectorOptions.LeaseDuration)
	})

	go writeElector.RunLeaderMetricLoop(ctx)

	for {
		stopped := false

		select {
		case <-ctx.Done():
			stopped = true
		case <-ctrl.discoveryResyncCh:
		}

		if stopped {
			break
		}

		err := retry.OnError(retry.DefaultBackoff, func(_ error) bool { return true }, func() error {
			err := ctrl.resyncMonitors()
			if err != nil {
				logger.WithError(err).Warn("resync monitors")
			}
			return err
		})
		if err != nil {
			logger.WithError(err).Error("resync monitors failed")
		}
	}

	ctrl.monitorsLock.Lock()
	defer ctrl.monitorsLock.Unlock()
	for _, monitor := range ctrl.drainAllMonitors() {
		monitor.close()
	}
}

func (ctrl *controller) shouldMonitorType(gvr schema.GroupVersionResource, apiResource *metav1.APIResource) bool {
	canListWatch := 0
	for _, verb := range apiResource.Verbs {
		if verb == "list" || verb == "watch" {
			canListWatch += 1
		}
	}

	if canListWatch != 2 {
		return false
	}

	if !ctrl.Filter.TestGvr(gvr) {
		return false
	}

	return true
}

func (ctrl *controller) shouldMonitorObject(gvr schema.GroupVersionResource, namespace string, name string) bool {
	// TODO consider partitioning to reduce memory usage
	return true
}

func (ctrl *controller) resyncMonitors() error {
	cdc, err := ctrl.Discovery.ForCluster(ctrl.Clients.TargetCluster().ClusterName())
	if err != nil {
		return fmt.Errorf("cannot get discovery cache for target cluster: %w", err)
	}
	expected := cdc.GetAll()
	ctrl.Logger.WithField("expectedLength", len(expected)).Info("resync monitors")

	toStart, toStop := ctrl.compareMonitors(expected)

	newMonitors := make([]*monitor, 0, len(toStart))
	for _, gvr := range toStart {
		monitor := ctrl.startMonitor(gvr, expected[gvr])
		newMonitors = append(newMonitors, monitor)
	}
	ctrl.addMonitors(newMonitors)

	oldMonitors := ctrl.drainMonitors(toStop)
	for _, monitor := range oldMonitors {
		monitor.close()
	}

	return nil
}

func (ctrl *controller) compareMonitors(expected discovery.GvrDetails) (toStart, toStop []schema.GroupVersionResource) {
	toStart = []schema.GroupVersionResource{}
	toStop = []schema.GroupVersionResource{}

	ctrl.monitorsLock.RLock()
	defer ctrl.monitorsLock.RUnlock()
	for gvr, apiResource := range expected {
		if !ctrl.shouldMonitorType(gvr, apiResource) {
			continue
		}

		if _, exists := ctrl.monitors[gvr]; !exists {
			toStart = append(toStart, gvr)
		}
	}
	for gvr := range ctrl.monitors {
		if _, exists := expected[gvr]; !exists {
			toStop = append(toStop, gvr)
		}
	}

	return toStart, toStop
}

func (ctrl *controller) addMonitors(monitors []*monitor) {
	ctrl.monitorsLock.Lock()
	defer ctrl.monitorsLock.Unlock()

	for _, monitor := range monitors {
		ctrl.monitors[monitor.gvr] = monitor
	}
}

func (ctrl *controller) drainMonitors(gvrs []schema.GroupVersionResource) []*monitor {
	ctrl.monitorsLock.Lock()
	defer ctrl.monitorsLock.Unlock()

	monitors := make([]*monitor, 0, len(gvrs))
	for _, gvr := range gvrs {
		monitors = append(monitors, ctrl.monitors[gvr])
		delete(ctrl.monitors, gvr)
	}
	return monitors
}

func (ctrl *controller) drainAllMonitors() []*monitor {
	ctrl.monitorsLock.Lock()
	defer ctrl.monitorsLock.Unlock()

	monitors := make([]*monitor, 0, len(ctrl.monitors))
	for gvr := range ctrl.monitors {
		monitors = append(monitors, ctrl.monitors[gvr])
		delete(ctrl.monitors, gvr)
	}
	ctrl.monitors = map[schema.GroupVersionResource]*monitor{}
	return monitors
}

func (ctrl *controller) startMonitor(gvr schema.GroupVersionResource, apiResource *metav1.APIResource) *monitor {
	logger := ctrl.Logger.WithField("submod", "monitor").WithField("gvr", gvr)
	logger.Debug("Starting")

	ctx, cancelFunc := context.WithCancel(context.Background())

	monitor := &monitor{
		ctrl:        ctrl,
		logger:      logger,
		gvr:         gvr,
		apiResource: apiResource,
		cancelCtx:   cancelFunc,
		onCreateMetric: ctrl.OnCreateMetric.With(&onCreateMetric{
			ApiGroup: gvr.GroupVersion(),
			Resource: gvr.Resource,
		}),
		onUpdateMetric: ctrl.OnUpdateMetric.With(&onUpdateMetric{
			ApiGroup: gvr.GroupVersion(),
			Resource: gvr.Resource,
		}),
		onDeleteMetric: ctrl.OnDeleteMetric.With(&onDeleteMetric{
			ApiGroup: gvr.GroupVersion(),
			Resource: gvr.Resource,
		}),
	}

	store := informerutil.NewPrepushUndeltaStore(
		logger,
		func(obj *unstructured.Unstructured) bool {
			return ctrl.shouldMonitorObject(gvr, obj.GetNamespace(), obj.GetName())
		},
	)

	hasSynced := new(atomic.Bool)

	store.OnPostReplace = func() {
		hasSynced.Store(true)
	}
	store.OnAdd = func(newObj *unstructured.Unstructured) {
		if hasSynced.Load() {
			ctrl.taskPool.Send(func() writerTask { return monitor.onNeedSnapshot(ctx, newObj, diffcache.SnapshotNameCreation) })
		}
	}
	store.OnUpdate = func(oldObj, newObj *unstructured.Unstructured) {
		ctrl.taskPool.Send(func() writerTask { return monitor.onUpdate(ctx, oldObj, newObj) })
	}
	store.OnDelete = func(oldObj *unstructured.Unstructured) {
		ctrl.taskPool.Send(func() writerTask { return monitor.onNeedSnapshot(ctx, oldObj, diffcache.SnapshotNameDeletion) })
	}

	nsableReflectorClient := ctrl.Clients.TargetCluster().DynamicClient().Resource(gvr)
	var reflectorClient dynamic.ResourceInterface
	if apiResource.Namespaced {
		reflectorClient = nsableReflectorClient.Namespace(metav1.NamespaceAll)
	} else {
		reflectorClient = nsableReflectorClient
	}

	lw := &toolscache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			if options.ResourceVersion == "" {
				options.ResourceVersion = "0"
			}

			return reflectorClient.List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return reflectorClient.Watch(ctx, options)
		},
	}

	reflector := toolscache.NewReflector(
		lw,
		&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": gvr.GroupVersion().String(),
				"kind":       apiResource.Kind,
			},
		},
		store,
		0,
	)

	go func() {
		defer shutdown.RecoverPanic(logger)
		reflector.Run(ctx.Done())
	}()

	return monitor
}

type monitor struct {
	ctrl           *controller
	logger         logrus.FieldLogger
	gvr            schema.GroupVersionResource
	apiResource    *metav1.APIResource
	cancelCtx      context.CancelFunc
	onCreateMetric metrics.TaggedMetric
	onUpdateMetric metrics.TaggedMetric
	onDeleteMetric metrics.TaggedMetric
}

func (monitor *monitor) close() {
	monitor.logger.Info("Closing")
	monitor.cancelCtx()
}

func (monitor *monitor) onUpdate(
	ctx context.Context,
	oldObj, newObj *unstructured.Unstructured,
) writerTask {
	defer monitor.onUpdateMetric.DeferCount(monitor.ctrl.Clock.Now())

	if oldObj.GetResourceVersion() == newObj.GetResourceVersion() {
		// no change
		return nil
	}

	if oldDelTs, newDelTs := oldObj.GetDeletionTimestamp(), newObj.GetDeletionTimestamp(); oldDelTs.IsZero() && !newDelTs.IsZero() {
		monitor.onNeedSnapshot(ctx, newObj, diffcache.SnapshotNameDeletion)
	}

	patch := &diffcache.Patch{
		InformerTime:       monitor.ctrl.Clock.Now(),
		OldResourceVersion: oldObj.GetResourceVersion(),
		NewResourceVersion: newObj.GetResourceVersion(),
	}

	redacted := monitor.testRedacted(oldObj) || monitor.testRedacted(newObj)

	if redacted {
		patch.Redacted = true
		patch.DiffList = diffcmp.DiffList{Diffs: []diffcmp.Diff{{
			JsonPath: "metadata.resourceVersion",
			Old:      oldObj.GetResourceVersion(),
			New:      newObj.GetResourceVersion(),
		}}}
	} else {
		patch.DiffList = diffcmp.Compare(oldObj.Object, newObj.Object)
	}

	objectRef := util.ObjectRefFromUnstructured(newObj, monitor.ctrl.Clients.TargetCluster().ClusterName(), monitor.gvr).Clone()

	return func(ctx context.Context) {
		ctx, cancelFunc := context.WithTimeout(ctx, monitor.ctrl.options.storeTimeout)
		defer cancelFunc()

		monitor.ctrl.Cache.Store(
			ctx,
			objectRef,
			patch,
		)
	}
}

func (monitor *monitor) onNeedSnapshot(
	ctx context.Context,
	obj *unstructured.Unstructured,
	snapshotName string,
) writerTask {
	defer monitor.onDeleteMetric.DeferCount(monitor.ctrl.Clock.Now())

	redacted := monitor.testRedacted(obj)

	ctx, cancelFunc := context.WithTimeout(ctx, monitor.ctrl.options.storeTimeout)
	defer cancelFunc()

	objRaw, err := json.Marshal(obj)
	if err != nil {
		monitor.logger.WithError(err).
			WithField("kind", obj.GetKind()).
			WithField("namespace", obj.GetNamespace()).
			WithField("name", obj.GetName()).
			Error("cannot re-marshal unstructured object")
	}

	objectRef := util.ObjectRefFromUnstructured(obj, monitor.ctrl.Clients.TargetCluster().ClusterName(), monitor.gvr).Clone()
	snapshotRv := obj.GetResourceVersion()

	return func(ctx context.Context) {
		ctx, cancelFunc := context.WithTimeout(ctx, monitor.ctrl.options.storeTimeout)
		defer cancelFunc()

		monitor.ctrl.Cache.StoreSnapshot(
			ctx,
			objectRef,
			snapshotName,
			&diffcache.Snapshot{
				ResourceVersion: snapshotRv,
				Redacted:        redacted, // we still persist redacted objects for ownerReferences lookup
				Value:           objRaw,
			},
		)
	}
}

func (monitor *monitor) testRedacted(obj *unstructured.Unstructured) bool {
	_, redacted := obj.GetLabels()[LabelKeyRedacted]
	if !redacted {
		gvrnn := fmt.Sprintf(
			"%s/%s/%s/%s/%s",
			monitor.gvr.Group,
			monitor.gvr.Version,
			monitor.gvr.Resource,
			obj.GetNamespace(),
			obj.GetName(),
		)
		redacted = monitor.ctrl.redactRegex.MatchString(gvrnn)
	}

	return redacted
}
