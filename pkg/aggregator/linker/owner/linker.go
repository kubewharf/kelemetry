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

package ownerlinker

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	diffcache "github.com/kubewharf/kelemetry/pkg/diff/cache"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideListImpl("owner-linker", manager.Ptr(&Controller{}), &manager.List[linker.Linker]{})
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "owner-linker-enable", false, "enable owner linker")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type Controller struct {
	options        options
	Clock          clock.Clock
	Logger         logrus.FieldLogger
	Clients        k8s.Clients
	DiscoveryCache discovery.DiscoveryCache
	ObjectCache    *objectcache.ObjectCache
	DiffCache      diffcache.Cache
	LinkerMetric   *metrics.Metric[*linkerMetric]
}

type linkerMetric struct {
	Cluster string
	Fetch   string
}

func (*linkerMetric) MetricName() string { return "owner_linker_lookup" }

var _ manager.Component = &Controller{}

func (ctrl *Controller) Options() manager.Options        { return &ctrl.options }
func (ctrl *Controller) Init() error                     { return nil }
func (ctrl *Controller) Start(ctx context.Context) error { return nil }
func (ctrl *Controller) Close(ctx context.Context) error { return nil }

func (ctrl *Controller) LinkerName() string { return "owner-linker" }
func (ctrl *Controller) Lookup(ctx context.Context, object utilobject.Rich) ([]linker.LinkerResult, error) {
	metric := &linkerMetric{Cluster: object.Cluster, Fetch: "Unknown"}
	defer ctrl.LinkerMetric.DeferCount(ctrl.Clock.Now(), metric)

	raw := object.Raw

	logger := ctrl.Logger.WithFields(object.AsFields("object"))

	if raw == nil {
		logger.Debug("Fetching dynamic object")

		// try creation snapshot first
		snapshot, err := ctrl.DiffCache.FetchSnapshot(ctx, object.Key, diffcache.SnapshotNameCreation)
		if err != nil {
			return nil, fmt.Errorf("cannot fallback to snapshot: %w", err)
		}

		if snapshot != nil {
			raw = &unstructured.Unstructured{}
			if err := raw.UnmarshalJSON(snapshot.Value); err != nil {
				return nil, fmt.Errorf("decode snapshot err: %w", err)
			}
			metric.Fetch = "Snapshot"
		} else {
			var err error
			raw, err = ctrl.ObjectCache.Get(ctx, object.VersionedKey)
			if err != nil {
				return nil, fmt.Errorf("cannot fetch object value from cache: %w", err)
			}
			metric.Fetch = "ObjectCache"
		}

		if raw == nil {
			logger.Debug("object no longer exists")
			return nil, nil
		}
	} else {
		metric.Fetch = "Raw"
	}

	var results []linker.LinkerResult

	for _, owner := range raw.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			groupVersion, err := schema.ParseGroupVersion(owner.APIVersion)
			if err != nil {
				logger.WithError(err).Warn("invalid owner apiVersion")
				continue
			}

			gvk := groupVersion.WithKind(owner.Kind)
			cdc, err := ctrl.DiscoveryCache.ForCluster(object.Cluster)
			if err != nil {
				logger.WithError(err).Error("cannot access cluster from object reference")
				continue
			}

			gvr, exists := cdc.LookupResource(gvk)
			if !exists {
				logger.WithField("gvk", gvk).Warn("Object contains owner reference of unknown GVK")
				continue
			}

			parentRef := utilobject.Rich{
				VersionedKey: utilobject.VersionedKey{
					Key: utilobject.Key{
						Cluster:   object.Cluster,
						Group:     gvr.Group,
						Resource:  gvr.Resource,
						Namespace: object.Namespace,
						Name:      owner.Name,
					},
					Version: gvr.Version,
				},
				Uid: owner.UID,
			}
			logger.WithField("owner", parentRef).Debug("Resolved owner")

			results = append(results, linker.LinkerResult{
				Object:  parentRef,
				Role:    zconstants.LinkRoleParent,
				Class:   "children",
				DedupId: "ownerReference",
			})
		}
	}

	return results, nil
}
