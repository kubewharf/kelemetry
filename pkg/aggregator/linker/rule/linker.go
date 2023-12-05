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

package rulelinker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	kelemetryv1a1 "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1"
	kelemetryv1a1util "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1/util"
	kelemetryv1a1informers "github.com/kubewharf/kelemetry/pkg/crds/client/informers/externalversions/apis/v1alpha1"
	kelemetryv1a1listers "github.com/kubewharf/kelemetry/pkg/crds/client/listers/apis/v1alpha1"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideListImpl("rule-linker", manager.Ptr(&Controller{}), &manager.List[linker.Linker]{})
}

type Options struct {
	Enable           bool
	InformerTtl      time.Duration
	CacheSyncTimeout time.Duration
}

func (options *Options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.Enable, "rule-linker-enable", false, "enable rule linker")
	fs.DurationVar(
		&options.InformerTtl,
		"rule-linker-informer-ttl",
		time.Hour,
		"duration for which the LinkRule lister is unused before the reflector is closed",
	)
	fs.DurationVar(
		&options.CacheSyncTimeout,
		"rule-linker-sync-timeout",
		time.Second*5,
		"duration to wait for the initial LinkRule list to become ready; "+
			"timeout has no effect other than potentially missing link rules in some objects",
	)
}

func (options *Options) EnableFlag() *bool { return &options.Enable }

type Controller struct {
	options     Options
	Logger      logrus.FieldLogger
	Clock       clock.Clock
	Clients     k8s.Clients
	Discovery   discovery.DiscoveryCache
	ObjectCache *objectcache.ObjectCache

	clustersMu sync.RWMutex
	clusters   map[string]*clusterEntry
}

type clusterEntry struct {
	readyCh       <-chan struct{}
	eventRecorder record.EventRecorder
	lister        kelemetryv1a1listers.LinkRuleLister
	err           error
	resetTimer    func(time.Duration)
}

var _ manager.Component = &Controller{}

func (ctrl *Controller) Options() manager.Options { return &ctrl.options }
func (ctrl *Controller) Init() error {
	ctrl.clusters = make(map[string]*clusterEntry)
	return nil
}
func (ctrl *Controller) Start(ctx context.Context) error { return nil }
func (ctrl *Controller) Close(ctx context.Context) error { return nil }

var _ linker.Linker = &Controller{}

func (ctrl *Controller) LinkerName() string { return "rule-linker" }

func (ctrl *Controller) Lookup(ctx context.Context, object utilobject.Rich) ([]linker.LinkerResult, error) {
	logger := ctrl.Logger.WithFields(object.AsFields("object"))

	raw := object.Raw
	if raw == nil {
		logger.Debug("Fetching dynamic object")

		var err error
		raw, err = ctrl.ObjectCache.Get(ctx, object.VersionedKey)
		if err != nil {
			return nil, fmt.Errorf("cannot fetch object value: %w", err)
		}

		if raw == nil {
			return nil, fmt.Errorf("object does not exist")
		}
	}

	lister, eventRecorder, err := ctrl.getCluster(ctx, object.Cluster)
	if err != nil {
		return nil, fmt.Errorf("lister is unavailable for cluster %q", object.Cluster)
	}

	var results []linker.LinkerResult

	rules, err := lister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("cannot list rules")
	}

	for _, rule := range rules {
		if ruleResults, err := ctrl.processRule(ctx, logger.WithField("rule", rule.Name), object, raw, rule); err != nil {
			logger.WithField("rule", rule.Name).WithError(err).Error()
			eventRecorder.Eventf(
				rule,
				corev1.EventTypeWarning,
				"ProcessRule",
				"Error processing %s %s/%s: %s",
				object.Resource,
				object.Namespace,
				object.Name,
				err.Error(),
			)
		} else {
			results = append(results, ruleResults...)
		}
	}

	return results, nil
}

func (ctrl *Controller) processRule(
	ctx context.Context,
	logger logrus.FieldLogger,
	object utilobject.Rich,
	objectRaw *unstructured.Unstructured,
	rule *kelemetryv1a1.LinkRule,
) ([]linker.LinkerResult, error) {
	matched, err := kelemetryv1a1util.TestObject(object.Key, objectRaw, &rule.SourceFilter)
	if err != nil {
		return nil, fmt.Errorf("cannot test object for rule match: %w", err)
	}

	if !matched {
		return nil, nil
	}

	targetRefs, err := kelemetryv1a1util.GenerateTargets(&rule.TargetTemplate, object, objectRaw)
	if err != nil {
		return nil, fmt.Errorf("cannot infer target object: %w", err)
	}

	results := make([]linker.LinkerResult, 0)
	for _, targetRef := range targetRefs {
		targetLogger := logger.WithFields(targetRef.AsFields("target"))

		targetLogger.Debug("Testing if target exists")
		targetRaw, err := ctrl.ObjectCache.Get(ctx, targetRef)
		if err != nil {
			return nil, fmt.Errorf("cannot fetch target object from cache: %w", err)
		}

		if targetRaw == nil {
			targetLogger.Warn("target object does not exist")
			continue
		}

		role, err := zconstants.LinkRoleValueFromTargetRole(rule.Link.TargetRole)
		if err != nil {
			return nil, fmt.Errorf("cannot parse target role: %w", err)
		}

		result := linker.LinkerResult{
			Object: utilobject.Rich{
				VersionedKey: targetRef,
				Uid:          targetRaw.GetUID(),
				Raw:          targetRaw,
			},
			Role:    role,
			Class:   rule.Link.Class,
			DedupId: fmt.Sprintf("linkRule/%s", rule.Name),
		}
		results = append(results, result)
	}

	return results, nil
}

func (ctrl *Controller) getCluster(ctx context.Context, cluster string) (kelemetryv1a1listers.LinkRuleLister, record.EventRecorder, error) {
	if lister, exists := ctrl.tryGetLister(cluster); exists {
		return lister.touch(ctrl.options.InformerTtl)
	}

	entry, readyCh := ctrl.setPendingLister(cluster)
	if readyCh == nil {
		return entry.touch(ctrl.options.InformerTtl)
	}

	defer close(readyCh)

	client, err := ctrl.Clients.Cluster(cluster)
	if err != nil {
		err := fmt.Errorf("cannot initialize clients for cluster %q: %w", cluster, err)
		entry.err = err
		return nil, nil, err
	}

	informer := kelemetryv1a1informers.NewLinkRuleInformer(client.KelemetryClient(), 0, cache.Indexers{})

	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: client.KubernetesClient().CoreV1().Events(metav1.NamespaceAll)})
	entry.eventRecorder = broadcaster.NewRecorder(kubernetesscheme.Scheme, corev1.EventSource{Component: "kelemetry"})

	entry.lister = kelemetryv1a1listers.NewLinkRuleLister(informer.GetIndexer())

	timerCh, resetFunc := startTimer(ctrl.Clock, ctrl.options.InformerTtl)
	go informer.Run(timerCh)

	waitCtx, waitCtxCancelFunc := context.WithTimeout(ctx, ctrl.options.CacheSyncTimeout)
	defer waitCtxCancelFunc()
	populated := cache.WaitForCacheSync(waitCtx.Done(), informer.HasSynced)
	if !populated {
		ctrl.Logger.WithField("cluster", cluster).Warn("LinkRule lister has not synced, will continue as usual")
	}

	entry.resetTimer = resetFunc

	return entry.lister, entry.eventRecorder, nil
}

func (ctrl *Controller) tryGetLister(cluster string) (*clusterEntry, bool) {
	ctrl.clustersMu.RLock()
	defer ctrl.clustersMu.RUnlock()
	lister, exists := ctrl.clusters[cluster]
	return lister, exists
}

func (ctrl *Controller) setPendingLister(cluster string) (_ *clusterEntry, _readyCh chan<- struct{}) {
	ctrl.clustersMu.Lock()
	defer ctrl.clustersMu.Unlock()

	lister, exists := ctrl.clusters[cluster]
	if exists {
		return lister, nil
	}

	readyCh := make(chan struct{})

	entry := &clusterEntry{
		readyCh: readyCh,
	}
	ctrl.clusters[cluster] = entry

	return entry, readyCh
}

func (entry *clusterEntry) touch(ttl time.Duration) (kelemetryv1a1listers.LinkRuleLister, record.EventRecorder, error) {
	<-entry.readyCh
	if entry.err != nil {
		return nil, nil, entry.err
	}

	entry.resetTimer(ttl)
	return entry.lister, entry.eventRecorder, nil
}

// a specialized timer optimized for frequent resets and only supports strictly increasing resets.
func startTimer(clock clock.Clock, initialTtl time.Duration) (<-chan struct{}, func(time.Duration)) {
	timerCh := make(chan struct{})

	var targetAtomic atomic.Pointer[time.Time]
	targetAtomic.Store(reflectutil.Box(clock.Now().Add(initialTtl)))

	go func() {
		target := targetAtomic.Load()

		for {
			remaining := (*target).Sub(clock.Now())
			if remaining <= 0 {
				break
			}

			clock.Sleep(remaining)
			newTarget := targetAtomic.Load()
			if newTarget == target {
				break // break without checking clock.Now() to ensure we don't sleep on the same target multiple times
			}
			target = newTarget
		}

		timerCh <- struct{}{}
	}()

	return timerCh, func(newTtl time.Duration) {
		targetAtomic.Store(reflectutil.Box(clock.Now().Add(newTtl)))
	}
}
