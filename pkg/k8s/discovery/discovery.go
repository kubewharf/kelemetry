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

package discovery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("discovery", NewDiscoveryCache)
}

type discoveryOptions struct {
	resyncInterval time.Duration
}

func (options *discoveryOptions) Setup(fs *pflag.FlagSet) {
	fs.DurationVar(&options.resyncInterval, "discovery-resync-interval", time.Hour, "frequency of refreshing discovery API")
}

func (options *discoveryOptions) EnableFlag() *bool { return nil }

type (
	GvrDetails = map[schema.GroupVersionResource]*metav1.APIResource
	GvrToGvk   = map[schema.GroupVersionResource]schema.GroupVersionKind
	GvkToGvr   = map[schema.GroupVersionKind]schema.GroupVersionResource
)

type DiscoveryCache interface {
	manager.Component

	GetAll() GvrDetails
	LookupKind(gvr schema.GroupVersionResource) (schema.GroupVersionKind, bool)
	LookupResource(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool)
	RequestAndWaitResync()
	// AddResyncHandler adds a channel that sends when at least one GVR has changed.
	// Should only be called during Init stage.
	AddResyncHandler() <-chan struct{}
}

type discoveryCache struct {
	options discoveryOptions
	logger  logrus.FieldLogger
	clock   clock.Clock
	clients k8s.Clients
	metrics metrics.Client

	ctx          context.Context
	resyncMetric metrics.Metric

	resyncRequestCh chan struct{}
	onResyncCh      []chan<- struct{}

	dataLock   sync.RWMutex
	gvrDetails GvrDetails
	gvrToGvk   GvrToGvk
	gvkToGvr   GvkToGvr
}

type resyncMetric struct{}

func NewDiscoveryCache(
	logger logrus.FieldLogger,
	clock clock.Clock,
	clients k8s.Clients,
	metrics metrics.Client,
) DiscoveryCache {
	return &discoveryCache{
		logger:          logger,
		clock:           clock,
		clients:         clients,
		metrics:         metrics,
		resyncRequestCh: make(chan struct{}, 1),
	}
}

func (dc *discoveryCache) Options() manager.Options {
	return &dc.options
}

func (dc *discoveryCache) Init(ctx context.Context) error {
	dc.ctx = ctx
	dc.resyncMetric = dc.metrics.New("discovery_resync", &resyncMetric{})
	return nil
}

func (dc *discoveryCache) Start(stopCh <-chan struct{}) error {
	go dc.run(stopCh)
	return nil
}

func (dc *discoveryCache) Close() error {
	return nil
}

func (dc *discoveryCache) run(stopCh <-chan struct{}) {
	defer shutdown.RecoverPanic(dc.logger)

	for {
		if err := dc.doResync(); err != nil {
			dc.logger.Error(err)
		}

		select {
		case <-stopCh:
			return
		case <-dc.clock.After(dc.options.resyncInterval):
		case <-dc.resyncRequestCh:
		}
	}
}

func (dc *discoveryCache) doResync() error {
	metric := &resyncMetric{}
	defer dc.resyncMetric.DeferCount(dc.clock.Now(), metric)

	// TODO also sync non-target clusters
	lists, err := dc.clients.TargetCluster().KubernetesClient().Discovery().ServerPreferredResources()
	if err != nil {
		return fmt.Errorf("query discovery API failed: %w", err)
	}

	gvrDetails := GvrDetails{}
	gvrToGvk := GvrToGvk{}
	gvkToGvr := GvkToGvr{}

	for _, list := range lists {
		listGroupVersion, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			return fmt.Errorf("discovery API result contains invalid groupVersion: %w", err)
		}

		for _, res := range list.APIResources {
			group := res.Group
			if group == "" {
				group = listGroupVersion.Group
			}

			version := res.Version
			if version == "" {
				version = listGroupVersion.Version
			}

			gvr := schema.GroupVersionResource{
				Group:    group,
				Version:  version,
				Resource: res.Name,
			}
			gvk := schema.GroupVersionKind{
				Group:   group,
				Version: version,
				Kind:    res.Kind,
			}

			resCopy := res
			gvrDetails[gvr] = &resCopy
			gvrToGvk[gvr] = gvk
			gvkToGvr[gvk] = gvr
		}
	}

	dc.dataLock.Lock()
	oldGvrToGvk := dc.gvrToGvk
	dc.gvrToGvk = gvrToGvk
	dc.gvkToGvr = gvkToGvr
	dc.gvrDetails = gvrDetails
	dc.dataLock.Unlock()

	if !haveSameKeys(oldGvrToGvk, gvrToGvk) {
		dc.logger.WithField("resourceCount", len(gvrDetails)).Info("Discovery API response changed, triggering resync")
		for _, onResyncCh := range dc.onResyncCh {
			select {
			case onResyncCh <- struct{}{}:
			default:
			}
		}
	}

	return nil
}

func haveSameKeys(m1, m2 GvrToGvk) bool {
	if len(m1) != len(m2) {
		return false
	}

	for k := range m1 {
		if _, exists := m2[k]; !exists {
			return false
		}
	}

	return true
}

func (dc *discoveryCache) GetAll() GvrDetails {
	dc.dataLock.RLock()
	defer dc.dataLock.RUnlock()

	return dc.gvrDetails
}

func (dc *discoveryCache) LookupKind(gvr schema.GroupVersionResource) (schema.GroupVersionKind, bool) {
	dc.dataLock.RLock()
	gvk, exists := dc.gvrToGvk[gvr]
	dc.dataLock.RUnlock()

	if !exists {
		dc.RequestAndWaitResync()

		dc.dataLock.RLock()
		gvk, exists = dc.gvrToGvk[gvr]
		dc.dataLock.RUnlock()
	}

	return gvk, exists
}

func (dc *discoveryCache) LookupResource(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool) {
	if gvk.Group == "" && gvk.Version == "" {
		gvk.Version = "v1"
	}

	dc.dataLock.RLock()
	gvr, exists := dc.gvkToGvr[gvk]
	dc.dataLock.RUnlock()

	if !exists {
		dc.RequestAndWaitResync()

		dc.dataLock.RLock()
		gvr, exists = dc.gvkToGvr[gvk]
		dc.dataLock.RUnlock()
	}

	return gvr, exists
}

func (dc *discoveryCache) RequestAndWaitResync() {
	select {
	case dc.resyncRequestCh <- struct{}{}:
	default:
	}

	dc.clock.Sleep(time.Second * 5) // FIXME: notify resync completion from doResync
}

func (dc *discoveryCache) AddResyncHandler() <-chan struct{} {
	ch := make(chan struct{}, 1)
	dc.onResyncCh = append(dc.onResyncCh, ch)
	return ch
}

func SplitGroupVersion(apiVersion string) schema.GroupVersion {
	gv := strings.SplitN(apiVersion, "/", 2)
	return schema.GroupVersion{
		Group:   gv[0],
		Version: gv[1],
	}
}
