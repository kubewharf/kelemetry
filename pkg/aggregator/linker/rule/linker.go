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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	kelemetryv1a1util "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1/util"
	"github.com/kubewharf/kelemetry/pkg/crds/client/informers/externalversions/apis/v1alpha1"
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
		"duration to wait for the initial LinkRule list to become ready; timeout has no effect other than potentially missing link rules in some objects",
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

	listersMu sync.RWMutex
	listers   map[string]*listerEntry
}

type listerEntry struct {
	readyCh    <-chan struct{}
	lister     kelemetryv1a1listers.LinkRuleLister
	err        error
	resetTimer func(time.Duration)
}

var _ manager.Component = &Controller{}

func (ctrl *Controller) Options() manager.Options { return &ctrl.options }
func (ctrl *Controller) Init() error {
	ctrl.listers = make(map[string]*listerEntry)
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

	lister, err := ctrl.getLister(ctx, object.Cluster)
	if err != nil {
		return nil, fmt.Errorf("lister is unavailable for cluster %q", object.Cluster)
	}

	var results []linker.LinkerResult

	rules, err := lister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("cannot list rules")
	}

	for _, rule := range rules {
		matched, err := kelemetryv1a1util.TestObject(object.Key, raw, &rule.SourceFilter)
		if err != nil {
			logger.WithField("rule", rule.Name).WithError(err).Error("error testing object for rule match")
			continue
		}

		if !matched {
			continue
		}

		parentRef, err := kelemetryv1a1util.GenerateTarget(&rule.TargetTemplate, object, raw)
		if err != nil {
			return nil, fmt.Errorf("cannot infer parent object: %w", err)
		}

		logger.WithField("rule", rule.Name).WithFields(parentRef.AsFields("parent")).Debug("Testing if parent exists")
		parentRaw, err := ctrl.ObjectCache.Get(ctx, parentRef)
		if err != nil {
			return nil, fmt.Errorf("cannot get parent specified by rule: %w", err)
		}

		role, err := zconstants.LinkRoleValueFromTargetRole(rule.Link.TargetRole)
		if err != nil {
			return nil, fmt.Errorf("cannot parse target role: %w", err)
		}

		results = append(results, linker.LinkerResult{
			Object: utilobject.Rich{
				VersionedKey: parentRef,
				Uid:          parentRaw.GetUID(),
				Raw:          parentRaw,
			},
			Role: role,
		})
	}

	return results, nil
}

func (ctrl *Controller) getLister(ctx context.Context, cluster string) (kelemetryv1a1listers.LinkRuleLister, error) {
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
		return nil, err
	}

	informer := v1alpha1.NewLinkRuleInformer(client.KelemetryClient(), 0, cache.Indexers{})

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

	return entry.lister, nil
}

func (ctrl *Controller) tryGetLister(cluster string) (*listerEntry, bool) {
	ctrl.listersMu.RLock()
	defer ctrl.listersMu.RUnlock()
	lister, exists := ctrl.listers[cluster]
	return lister, exists
}

func (ctrl *Controller) setPendingLister(cluster string) (_ *listerEntry, _readyCh chan<- struct{}) {
	ctrl.listersMu.Lock()
	defer ctrl.listersMu.Unlock()

	lister, exists := ctrl.listers[cluster]
	if exists {
		return lister, nil
	}

	readyCh := make(chan struct{})

	entry := &listerEntry{
		readyCh: readyCh,
	}
	ctrl.listers[cluster] = entry

	return entry, readyCh
}

func (entry *listerEntry) touch(ttl time.Duration) (kelemetryv1a1listers.LinkRuleLister, error) {
	<-entry.readyCh
	if entry.err != nil {
		return nil, entry.err
	}

	entry.resetTimer(ttl)
	return entry.lister, nil
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
