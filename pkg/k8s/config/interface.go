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

// Provide connection details to a Kubernetes cluster.
package k8sconfig

import (
	"time"

	"k8s.io/client-go/rest"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kube-config", manager.Ptr[Config](&mux{
		Mux: manager.NewMux("kube-config", false),
	}))
}

type Config interface {
	// TargetName returns the name of thetarget cluster.
	// ProvideTarget() is equivalent to Provide(TargetName()).
	TargetName() string

	// Provide returns the rest config for a named cluster.
	// Returns nil if the cluster is not available.
	// Mainly used when the main cluster contains references to other clusters.
	Provide(clusterName string) *Cluster
}

type Cluster struct {
	Config                *rest.Config
	DefaultRequestTimeout time.Duration
}

type mux struct {
	*manager.Mux
}

func (mux *mux) TargetName() string {
	return mux.Impl().(Config).TargetName()
}

func (mux *mux) Provide(clusterName string) *Cluster {
	return mux.Impl().(Config).Provide(clusterName)
}

type MockConfig struct {
	TargetClusterName string
	Clusters          map[string]*Cluster
}

func (c *MockConfig) TargetName() string { return c.TargetClusterName }

func (c *MockConfig) Provide(clusterName string) *Cluster { return c.Clusters[clusterName] }
