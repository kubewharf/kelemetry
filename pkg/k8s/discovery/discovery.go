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
	// Gets the discovery cache for a specific cluster.
	// Lazily initializes the cache if it is not present.
	ForCluster(name string) (ClusterDiscoveryCache, error)
}

type ClusterDiscoveryCache interface {
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

	// Discovery loops should run indefinitely but lazily.
	// For now we do not consider the need for removing clusters.
	ctx context.Context //nolint:containedctx

	resyncMetric metrics.Metric

	clusters sync.Map
}

type clusterDiscoveryCache struct {
	initLock sync.Once
	initErr  error

	options      *discoveryOptions
	logger       logrus.FieldLogger
	clock        clock.Clock
	resyncMetric metrics.TaggedMetric
	client       k8s.Client

	resyncRequestCh chan struct{}
	onResyncCh      []chan<- struct{}

	dataLock   sync.RWMutex
	gvrDetails GvrDetails
	gvrToGvk   GvrToGvk
	gvkToGvr   GvkToGvr
}

type resyncMetric struct {
	Cluster string
}

func NewDiscoveryCache(
	logger logrus.FieldLogger,
	clock clock.Clock,
	clients k8s.Clients,
	metrics metrics.Client,
) DiscoveryCache {
	return &discoveryCache{
		logger:  logger,
		clock:   clock,
		clients: clients,
		metrics: metrics,
	}
}

func (dc *discoveryCache) Options() manager.Options { return &dc.options }

func (dc *discoveryCache) Init(ctx context.Context) error {
	dc.resyncMetric = dc.metrics.New("discovery_resync", &resyncMetric{})
	return nil
}

func (dc *discoveryCache) Start(ctx context.Context) error {
	dc.ctx = ctx
	return nil
}

func (dc *discoveryCache) Close(ctx context.Context) error { return nil }

func (dc *discoveryCache) ForCluster(cluster string) (ClusterDiscoveryCache, error) {
	cacheAny, _ := dc.clusters.LoadOrStore(cluster, &clusterDiscoveryCache{})
	cdc := cacheAny.(*clusterDiscoveryCache)
	// no matter we loaded or stored, someone has to initialize it the first time.
	cdc.initLock.Do(func() {
		cdc.logger = dc.logger.WithField("cluster", cluster)
		cdc.clock = dc.clock
		cdc.options = &dc.options
		cdc.resyncMetric = dc.resyncMetric.With(&resyncMetric{
			Cluster: cluster,
		})
		cdc.resyncRequestCh = make(chan struct{}, 1)
		cdc.client, cdc.initErr = dc.clients.Cluster(cluster)
		if cdc.initErr != nil {
			return
		}

		cdc.logger.Info("initialized cluster discovery cache")
		go cdc.run(dc.ctx)
	})
	return cdc, cdc.initErr
}

func (cdc *clusterDiscoveryCache) run(ctx context.Context) {
	defer shutdown.RecoverPanic(cdc.logger)

	for {
		if err := cdc.doResync(); err != nil {
			cdc.logger.Error(err)
		}

		select {
		case <-ctx.Done():
			return
		case <-cdc.clock.After(cdc.options.resyncInterval):
		case <-cdc.resyncRequestCh:
		}
	}
}

func (cdc *clusterDiscoveryCache) doResync() error {
	defer cdc.resyncMetric.DeferCount(cdc.clock.Now())

	// TODO also sync non-target clusters
	lists, err := cdc.client.KubernetesClient().Discovery().ServerPreferredResources()
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

	cdc.dataLock.Lock()
	oldGvrToGvk := cdc.gvrToGvk
	cdc.gvrToGvk = gvrToGvk
	cdc.gvkToGvr = gvkToGvr
	cdc.gvrDetails = gvrDetails
	cdc.dataLock.Unlock()

	if !haveSameKeys(oldGvrToGvk, gvrToGvk) {
		cdc.logger.WithField("resourceCount", len(gvrDetails)).Info("Discovery API response changed, triggering resync")
		for _, onResyncCh := range cdc.onResyncCh {
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

func (cdc *clusterDiscoveryCache) GetAll() GvrDetails {
	cdc.dataLock.RLock()
	defer cdc.dataLock.RUnlock()

	return cdc.gvrDetails
}

func (cdc *clusterDiscoveryCache) LookupKind(gvr schema.GroupVersionResource) (schema.GroupVersionKind, bool) {
	cdc.dataLock.RLock()
	gvk, exists := cdc.gvrToGvk[gvr]
	cdc.dataLock.RUnlock()

	if !exists {
		cdc.RequestAndWaitResync()

		cdc.dataLock.RLock()
		gvk, exists = cdc.gvrToGvk[gvr]
		cdc.dataLock.RUnlock()
	}

	return gvk, exists
}

func (cdc *clusterDiscoveryCache) LookupResource(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool) {
	if gvk.Group == "" && gvk.Version == "" {
		gvk.Version = "v1"
	}

	cdc.dataLock.RLock()
	gvr, exists := cdc.gvkToGvr[gvk]
	cdc.dataLock.RUnlock()

	if !exists {
		cdc.RequestAndWaitResync()

		cdc.dataLock.RLock()
		gvr, exists = cdc.gvkToGvr[gvk]
		cdc.dataLock.RUnlock()
	}

	return gvr, exists
}

func (cdc *clusterDiscoveryCache) RequestAndWaitResync() {
	select {
	case cdc.resyncRequestCh <- struct{}{}:
	default:
	}

	cdc.clock.Sleep(time.Second * 5) // FIXME: notify resync completion from doResync
}

func (cdc *clusterDiscoveryCache) AddResyncHandler() <-chan struct{} {
	ch := make(chan struct{}, 1)
	cdc.onResyncCh = append(cdc.onResyncCh, ch)
	return ch
}

func SplitGroupVersion(apiVersion string) schema.GroupVersion {
	gv := strings.SplitN(apiVersion, "/", 2)
	return schema.GroupVersion{
		Group:   gv[0],
		Version: gv[1],
	}
}
