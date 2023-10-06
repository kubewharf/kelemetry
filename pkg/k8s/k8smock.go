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
	"flag"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	kelemetryversioned "github.com/kubewharf/kelemetry/pkg/crds/client/clientset/versioned"
	kelemetryfake "github.com/kubewharf/kelemetry/pkg/crds/client/clientset/versioned/fake"
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

	mutex           sync.Mutex
	singleFakeGuard bool
	dynamicClient   *dynamicfake.FakeDynamicClient
	k8sClient       *k8sfake.Clientset
	kelemetryClient *kelemetryfake.Clientset
}

func (client *MockClient) BindKlog(verbosity int32) {
	fs := flag.FlagSet{}
	klog.InitFlags(&fs)
	err := fs.Parse([]string{fmt.Sprintf("--v=%d", verbosity)})
	if err != nil {
		panic(err)
	}
}

func (client *MockClient) ClusterName() string { return client.Name }

func (client *MockClient) acquireSingleFake() {
	if client.singleFakeGuard {
		panic("only one type of client can be used from mock client")
	}
	client.singleFakeGuard = true
}

func (client *MockClient) DynamicClient() dynamic.Interface {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	if client.dynamicClient == nil {
		client.acquireSingleFake()
		client.dynamicClient = dynamicfake.NewSimpleDynamicClient(scheme.Scheme, client.Objects...)
	}

	return client.dynamicClient
}

func (client *MockClient) KubernetesClient() kubernetes.Interface {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	if client.k8sClient == nil {
		client.acquireSingleFake()
		client.k8sClient = k8sfake.NewSimpleClientset(client.Objects...)
	}
	return client.k8sClient
}

func (client *MockClient) KelemetryClient() kelemetryversioned.Interface {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	if client.kelemetryClient == nil {
		client.acquireSingleFake()
		client.kelemetryClient = kelemetryfake.NewSimpleClientset(client.Objects...)
	}
	return client.kelemetryClient
}

func (client *MockClient) InformerFactory() informers.SharedInformerFactory {
	panic("not yet implemented")
}

func (client *MockClient) NewInformerFactory(options ...informers.SharedInformerOption) informers.SharedInformerFactory {
	panic("not yet implemented")
}

func (client *MockClient) EventRecorder(name string) record.EventRecorder {
	return mockEventRecorder{}
}

type mockEventRecorder struct{}

func (mockEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {}

func (mockEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...any) {
}

func (mockEventRecorder) AnnotatedEventf(
	object runtime.Object,
	annotations map[string]string,
	eventtype string,
	reason string,
	messageFmt string,
	args ...any,
) {
}
