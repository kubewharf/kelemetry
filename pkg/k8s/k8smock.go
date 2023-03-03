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

package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
)

type MockClients struct {
	TargetClusterName string
	Clients           map[string]*MockClient
}

func (clients *MockClients) TargetCluster() Client {
	return clients.Clients[clients.TargetClusterName]
}

func (clients *MockClients) Cluster(clusterName string) (Client, error) {
	if client, exists := clients.Clients[clusterName]; exists {
		return client, nil
	} else {
		return nil, fmt.Errorf("cluster is not supported")
	}
}

type MockClient struct {
	Name    string
	Objects []runtime.Object
}

func (client *MockClient) ClusterName() string { return client.Name }

func (client *MockClient) DynamicClient() dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClient(scheme.Scheme, client.Objects...)
}

func (client *MockClient) KubernetesClient() kubernetes.Interface { panic("not yet implemented") }

func (client *MockClient) InformerFactory() informers.SharedInformerFactory {
	panic("not yet implemented")
}

func (client *MockClient) NewInformerFactory(options ...informers.SharedInformerOption) informers.SharedInformerFactory {
	panic("not yet implemented")
}

func (client *MockClient) EventRecorder(name string) record.EventRecorder {
	panic("not yet implemented")
}
