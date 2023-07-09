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

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	kelemetryv1a1 "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1"
	kelemetryv1a1util "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1/util"
	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.ProvideListImpl("rule-linker", manager.Ptr(&Controller{}), &manager.List[linker.Linker]{})
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "rule-linker-enable", false, "enable rule linker")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type Controller struct {
	options        options
	Logger         logrus.FieldLogger
	Clients        k8s.Clients
	DiscoveryCache discovery.DiscoveryCache
	ObjectCache    *objectcache.ObjectCache

	RuleInformer *kelemetryv1a1util.LinkRuleInformer
}

var _ manager.Component = &Controller{}

func (ctrl *Controller) Options() manager.Options        { return &ctrl.options }
func (ctrl *Controller) Init() error                     { return nil }
func (ctrl *Controller) Start(ctx context.Context) error { return nil }
func (ctrl *Controller) Close(ctx context.Context) error { return nil }

func (ctrl *Controller) Lookup(ctx context.Context, object util.ObjectRef) (*util.ObjectRef, error) {
	logger := ctrl.Logger.WithFields(object.AsFields("object"))

	childRaw := object.Raw
	if childRaw == nil {
		logger.Debug("Fetching dynamic object")

		var err error
		childRaw, err = ctrl.ObjectCache.Get(ctx, object)
		if err != nil {
			return nil, fmt.Errorf("cannot fetch object value: %w", err)
		}
		if childRaw == nil {
			return nil, fmt.Errorf("object does not exist")
		}
	}

	store, err := ctrl.RuleInformer.Cluster(object.Cluster)
	if err != nil {
		return nil, fmt.Errorf("cannot get cluster LinkRule informer: %w", err)
	}

	var matchedRule *kelemetryv1a1.LinkRule
	for _, rule := range store.List() {
		matched, err := kelemetryv1a1util.TestObject(object, childRaw, &rule.Filter)
		if err != nil {
			logger.WithField("rule", rule.Name).WithError(err).Error("error testing object for rule match")
		} else if matched {
			matchedRule = rule
			break
		}
	}

	if matchedRule == nil {
		return nil, nil
	}

	parentRef, err := kelemetryv1a1util.GenerateParent(&matchedRule.Parent, object, childRaw)
	if err != nil {
		return nil, fmt.Errorf("cannot infer parent object: %w", err)
	}

	logger.WithField("matchedRule", matchedRule.Name).WithFields(parentRef.AsFields("parent")).Debug("Testing if parent exists")
	parentRaw, err := ctrl.ObjectCache.Get(ctx, parentRef)
	if err != nil {
		return nil, fmt.Errorf("cannot get parent specified by rule: %w", err)
	}

	parentRef.Uid = parentRaw.GetUID()
	parentRef.Raw = parentRaw

	return &parentRef, nil
}
