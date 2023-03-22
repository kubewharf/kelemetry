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

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("owner-linker", manager.Ptr(&Controller{}))
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
	Logger         logrus.FieldLogger
	Linkers        linker.LinkerList
	Clients        k8s.Clients
	DiscoveryCache discovery.DiscoveryCache
	ObjectCache    *objectcache.ObjectCache
	ctx            context.Context
}

var _ manager.Component = &Controller{}

func (ctrl *Controller) Options() manager.Options {
	return &ctrl.options
}

func (ctrl *Controller) Init(ctx context.Context) error {
	ctrl.Linkers.AddLinker(ctrl)
	ctrl.ctx = ctx

	return nil
}

func (ctrl *Controller) Start(stopCh <-chan struct{}) error {
	return nil
}

func (ctrl *Controller) Close() error {
	return nil
}

func (ctrl *Controller) Lookup(ctx context.Context, object util.ObjectRef) *util.ObjectRef {
	raw := object.Raw

	logger := ctrl.Logger.WithField("object", object)

	if raw == nil {
		logger.Debug("Fetching dynamic object")

		var err error
		raw, err = ctrl.ObjectCache.Get(ctx, object)

		if err != nil {
			logger.WithError(err).Error("cannot fetch object value")
			return nil
		}

		if raw == nil {
			logger.Debug("object no longer exists")
			return nil
		}
	}

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

			ret := &util.ObjectRef{
				Cluster:              object.Cluster, // inherited from the same cluster
				GroupVersionResource: gvr,
				Namespace:            object.Namespace,
				Name:                 owner.Name,
				Uid:                  owner.UID,
			}
			logger.WithField("owner", ret).Debug("Resolved owner")

			return ret
		}
	}

	return nil
}
