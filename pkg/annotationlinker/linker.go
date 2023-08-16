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

package annotationlinker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideListImpl("annotation-linker", manager.Ptr(&controller{}), &manager.List[linker.Linker]{})
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "annotation-linker-enable", false, "enable annotation linker")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type controller struct {
	options        options
	Logger         logrus.FieldLogger
	Clients        k8s.Clients
	DiscoveryCache discovery.DiscoveryCache
	ObjectCache    *objectcache.ObjectCache
}

var _ manager.Component = &controller{}

func (ctrl *controller) Options() manager.Options        { return &ctrl.options }
func (ctrl *controller) Init() error                     { return nil }
func (ctrl *controller) Start(ctx context.Context) error { return nil }
func (ctrl *controller) Close(ctx context.Context) error { return nil }

func (ctrl *controller) Lookup(ctx context.Context, object utilobject.Rich) ([]linker.LinkerResult, error) {
	raw := object.Raw

	logger := ctrl.Logger.WithFields(object.AsFields("object"))

	if raw == nil {
		logger.Debug("Fetching dynamic object")

		var err error
		raw, err = ctrl.ObjectCache.Get(ctx, object.VersionedKey)

		if err != nil {
			return nil, metrics.LabelError(fmt.Errorf("cannot fetch object value: %w", err), "FetchCache")
		}

		if raw == nil {
			logger.Debug("object no longer exists")
			return nil, nil
		}
	}

	if ann, ok := raw.GetAnnotations()[LinkAnnotation]; ok {
		ref := &ParentLink{}
		err := json.Unmarshal([]byte(ann), ref)
		if err != nil {
			return nil, metrics.LabelError(fmt.Errorf("cannot parse ParentLink annotation: %w", err), "ParseAnnotation")
		}

		if ref.Cluster == "" {
			ref.Cluster = object.Cluster
		}

		objectRef := ref.ToRich()
		logger.WithFields(objectRef.AsFields("parent")).Debug("Resolved parent")

		return []linker.LinkerResult{{
			Object:  objectRef,
			Role:    zconstants.LinkRoleParent,
			DedupId: "annotation",
		}}, nil
	}

	return nil, nil
}
