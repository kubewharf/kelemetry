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

	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideMuxImpl("kube-config/mapoption", manager.Ptr(&Provider{}), k8sconfig.Config.Provide)
}

type options struct {
	targetClusterName string
	master            map[string]string
	kubeconfig        map[string]string
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
}

func (options *options) EnableFlag() *bool { return nil }

type Provider struct {
	manager.MuxImplBase
	options options

	configs      map[string]*rest.Config
	targetConfig *rest.Config
}

var _ k8sconfig.Config = &Provider{}

func (*Provider) MuxImplName() (name string, isDefault bool) { return "mapoption", true }

func (provider *Provider) Options() manager.Options { return &provider.options }

func (provider *Provider) Init(ctx context.Context) error {
	names := map[string]struct{}{}
	for name := range provider.options.master {
		names[name] = struct{}{}
	}
	for name := range provider.options.kubeconfig {
		names[name] = struct{}{}
	}

	provider.configs = make(map[string]*rest.Config, len(names))
	for name := range names {
		config, err := clientcmd.BuildConfigFromFlags(provider.options.master[name], provider.options.kubeconfig[name])
		if err != nil {
			return fmt.Errorf("cannot construct connection config for %q: %w", name, err)
		}

		config = rest.AddUserAgent(config, "kelemetry")

		provider.configs[name] = config
	}

	targetConfig, hasTarget := provider.configs[provider.options.targetClusterName]
	if !hasTarget {
		var err error
		if targetConfig, err = rest.InClusterConfig(); err != nil {
			return fmt.Errorf(
				"target cluster %q does not exist, fallback to in-cluster config but fail: %w",
				provider.options.targetClusterName,
				err,
			)
		}

		provider.configs[provider.options.targetClusterName] = targetConfig
	}

	provider.targetConfig = targetConfig

	return nil
}

func (provider *Provider) Start(stopCh <-chan struct{}) error { return nil }

func (provider *Provider) Close() error { return nil }

func (provider *Provider) ProvideTarget() *rest.Config { return provider.targetConfig }

func (provider *Provider) TargetName() string { return provider.options.targetClusterName }

func (provider *Provider) Provide(clusterName string) *rest.Config {
	return provider.configs[clusterName]
}
