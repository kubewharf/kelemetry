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

package mapoption

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

const K8sDefaultRequestTimeout = time.Minute

func init() {
	manager.Global.ProvideMuxImpl("kube-config/mapoption", manager.Ptr(&Provider{}), k8sconfig.Config.Provide)
}

type options struct {
	targetClusterName string
	master            map[string]string
	kubeconfig        map[string]string
	requestTimeout    map[string]string
	useOldRvClusters  []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringVar(
		&options.targetClusterName,
		"kube-target-cluster",
		"",
		"name of the target cluster; must be a key in --kube-apiserver and/or --kubeconfig",
	)
	fs.StringToStringVar(&options.master, "kube-apiservers", map[string]string{}, "map of kube apiserver addresses")
	fs.StringToStringVar(&options.kubeconfig, "kube-config-paths", map[string]string{}, "map of kube config paths")
	fs.StringToStringVar(
		&options.requestTimeout,
		"kube-request-timeout",
		map[string]string{},
		"map of server-side request timeout, used in metrics",
	)
	fs.StringSliceVar(
		&options.useOldRvClusters,
		"kube-use-old-resource-version-clusters",
		[]string{},
		"list of clusters that do not have new resource version in audit",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type Provider struct {
	manager.MuxImplBase
	options options

	configs map[string]*k8sconfig.Cluster
}

var _ k8sconfig.Config = &Provider{}

func (*Provider) MuxImplName() (name string, isDefault bool) { return "mapoption", true }

func (provider *Provider) Options() manager.Options { return &provider.options }

func (provider *Provider) Init() error {
	names := map[string]struct{}{}
	for name := range provider.options.master {
		names[name] = struct{}{}
	}
	for name := range provider.options.kubeconfig {
		names[name] = struct{}{}
	}

	provider.configs = make(map[string]*k8sconfig.Cluster, len(names))
	for name := range names {
		config, err := clientcmd.BuildConfigFromFlags(provider.options.master[name], provider.options.kubeconfig[name])
		if err != nil {
			return fmt.Errorf("cannot construct connection config for %q: %w", name, err)
		}

		config = rest.AddUserAgent(config, "kelemetry")

		defaultRequestTimeout := K8sDefaultRequestTimeout
		if value, exists := provider.options.requestTimeout[name]; exists {
			defaultRequestTimeout, err = time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid duration %q for cluster %q: %w", value, name, err)
			}
		}

		useOldRv := false
		for _, oldRvCluster := range provider.options.useOldRvClusters {
			if oldRvCluster == name {
				useOldRv = true
				break
			}
		}

		clusterConfig := &k8sconfig.Cluster{
			Config:                config,
			DefaultRequestTimeout: defaultRequestTimeout,
			UseOldResourceVersion: useOldRv,
		}

		provider.configs[name] = clusterConfig
	}

	_, hasTarget := provider.configs[provider.options.targetClusterName]
	if !hasTarget {
		inCluster, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf(
				"target cluster %q does not exist, fallback to in-cluster config but fail: %w",
				provider.options.targetClusterName,
				err,
			)
		}

		defaultRequestTimeout := K8sDefaultRequestTimeout
		if value, exists := provider.options.requestTimeout[provider.options.targetClusterName]; exists {
			defaultRequestTimeout, err = time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid duration %q for cluster %q: %w", value, provider.options.targetClusterName, err)
			}
		}

		provider.configs[provider.options.targetClusterName] = &k8sconfig.Cluster{
			Config:                inCluster,
			DefaultRequestTimeout: defaultRequestTimeout,
		}
	}

	return nil
}

func (provider *Provider) Start(ctx context.Context) error { return nil }

func (provider *Provider) Close(ctx context.Context) error { return nil }

func (provider *Provider) TargetName() string { return provider.options.targetClusterName }

func (provider *Provider) Provide(clusterName string) *k8sconfig.Cluster {
	return provider.configs[clusterName]
}
