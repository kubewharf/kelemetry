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

package filter

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/discovery"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

const (
	configMapName            = "kelemetry-filter-config"
	configMapNamespace       = "default"
	configMapExcludeRegexKey = "exclude-regex"
)

func init() {
	manager.Global.Provide("filter", manager.Ptr[Filter](&filter{}))
}

type Filter interface {
	manager.Component

	TestGvk(cluster string, gvk schema.GroupVersionKind) bool
	TestGvr(gvr schema.GroupVersionResource) bool

	TestAuditEvent(event *auditv1.Event) bool
}

type options struct {
	excludedTypes      []string
	excludedUserAgents []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(&options.excludedTypes, "filter-exclude-types", []string{
		"events",
		"events.k8s.io/events",
		"coordination.k8s.io/leases",
	}, "objects with these GRs are not traced")
	fs.StringSliceVar(&options.excludedUserAgents, "filter-exclude-user-agent", []string{
		"/leader-election",
		"local-path-provisioner/",
	}, "requests from user agents with any of these substrings are not traced")
}

func (options *options) EnableFlag() *bool { return nil }

type filter struct {
	options   options
	Logger    logrus.FieldLogger
	Discovery discovery.DiscoveryCache
	Clients   k8s.Clients

	excludeTypeHashTable map[schema.GroupResource]struct{}
	configMapInformer    informers.SharedInformerFactory
	recorder             record.EventRecorder

	configMutex sync.RWMutex
	config      *config
}

type config struct {
	excludeRegex *regexp.Regexp
}

var _ = func() manager.Component { return &filter{} }

func (filter *filter) Options() manager.Options {
	return &filter.options
}

func (filter *filter) Init() error {
	filter.excludeTypeHashTable = make(map[schema.GroupResource]struct{}, len(filter.options.excludedTypes))

	for _, ty := range filter.options.excludedTypes {
		split := strings.Split(ty, "/")
		var parsed schema.GroupResource
		switch len(split) {
		case 1:
			parsed.Resource = split[0]
		case 2:
			parsed.Group = split[0]
			parsed.Resource = split[1]
		default:
			return fmt.Errorf("cannot parse %q as a GVR", ty)
		}

		filter.excludeTypeHashTable[parsed] = struct{}{}
	}

	filter.configMapInformer = filter.Clients.TargetCluster().NewInformerFactory(
		informers.WithNamespace(configMapNamespace),
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", configMapName).String()
		}),
	)
	_, err := filter.configMapInformer.Core().V1().ConfigMaps().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { filter.setConfig(obj.(*corev1.ConfigMap)) },
		UpdateFunc: func(oldObj, newObj any) { filter.setConfig(newObj.(*corev1.ConfigMap)) },
		DeleteFunc: func(obj any) { filter.setConfig(nil) },
	})
	if err != nil {
		// should not happen during startup
		return fmt.Errorf("cannot add ConfigMap event handler: %w", err)
	}

	filter.recorder = filter.Clients.TargetCluster().EventRecorder("kelemetry-filter")

	return nil
}

func (filter *filter) Start(ctx context.Context) error {
	filter.configMapInformer.Start(ctx.Done())
	return nil
}

func (filter *filter) Close(ctx context.Context) error { return nil }

func (filter *filter) setConfig(cm *corev1.ConfigMap) {
	var c *config
	if cm != nil {
		var err error
		c, err = parseConfigMap(cm.Data)
		if err != nil {
			filter.recorder.Eventf(cm, corev1.EventTypeWarning, "InvalidConfig", err.Error())
		}
	}

	filter.configMutex.Lock()
	defer filter.configMutex.Unlock()
	filter.config = c
}

func parseConfigMap(data map[string]string) (c *config, err error) {
	c = new(config)

	if excludeRegex, hasExcludeRegex := data[configMapExcludeRegexKey]; hasExcludeRegex {
		c.excludeRegex, err = regexp.Compile(excludeRegex)
		if err != nil {
			return nil, err
		}
	}

	return c, nil
}

func (filter *filter) getConfig() *config {
	filter.configMutex.RLock()
	defer filter.configMutex.RUnlock()
	return filter.config
}

func (filter *filter) TestGvk(cluster string, gvk schema.GroupVersionKind) bool {
	cdc, err := filter.Discovery.ForCluster(cluster)
	if err != nil {
		filter.Logger.
			WithError(err).
			WithField("cluster", cluster).
			WithField("gvk", gvk).
			Error("assuming positive filter result in unsupported cluster")
		return true
	}

	gvr, hasCache := cdc.LookupResource(gvk)
	if !hasCache {
		filter.Logger.WithField("gvk", gvk).Warn("Received object with unknown GVK")
		return true
	}

	return filter.TestGvr(gvr)
}

func (filter *filter) TestGvr(gvr schema.GroupVersionResource) bool {
	ty := gvr.GroupResource()
	if _, exists := filter.excludeTypeHashTable[ty]; exists {
		return false
	}

	return true
}

func (filter *filter) TestAuditEvent(event *auditv1.Event) bool {
	if event.ObjectRef == nil {
		return false
	}

	if !filter.TestGvr(schema.GroupVersionResource{
		Group:    event.ObjectRef.APIGroup,
		Version:  event.ObjectRef.APIVersion,
		Resource: event.ObjectRef.Resource,
	}) {
		return false
	}

	if config := filter.getConfig(); config != nil {
		if config.excludeRegex != nil && config.excludeRegex.MatchString(fmt.Sprintf(
			"%s/%s/%s/%s/%s",
			event.ObjectRef.APIGroup,
			event.ObjectRef.APIVersion,
			event.ObjectRef.Resource,
			event.ObjectRef.Namespace,
			event.ObjectRef.Name,
		)) {
			return false
		}
	}

	for _, banned := range filter.options.excludedUserAgents {
		if strings.Contains(event.UserAgent, banned) {
			return false
		}
	}

	return true
}
