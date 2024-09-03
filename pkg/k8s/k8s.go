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
	"context"
	"flag"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	kelemetryversioned "github.com/kubewharf/kelemetry/pkg/crds/client/clientset/versioned"
	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kube", manager.Ptr[Clients](&clusterClients{
		clients: map[string]*client{},
	}))
}

type options struct {
	targetRestQps   float32
	targetRestBurst int
	otherRestQps    float32
	otherRestBurst  int
}

func (options *options) Setup(fs *pflag.FlagSet) {
	goFs := &flag.FlagSet{}
	klog.InitFlags(goFs)
	goFs.VisitAll(func(f *flag.Flag) {
		f.Name = fmt.Sprintf("klog-%s", strings.ReplaceAll(f.Name, "_", "-"))
	})
	fs.AddGoFlagSet(goFs)

	fs.Float32Var(&options.targetRestQps, "kube-target-rest-qps", 10.0, "k8s rest client qps for the target cluster")
	fs.IntVar(&options.targetRestBurst, "kube-target-rest-burst", 10, "k8s rest client burst for the target cluster")
	fs.Float32Var(&options.otherRestQps, "kube-other-rest-qps", 10.0, "k8s rest client qps for non-target clusters")
	fs.IntVar(&options.otherRestBurst, "kube-other-rest-burst", 10, "k8s rest client burst for non-target clusters")
}

func (options *options) EnableFlag() *bool { return nil }

type Clients interface {
	TargetCluster() Client
	Cluster(name string) (Client, error)
}

type Client interface {
	ClusterName() string
	DynamicClient() dynamic.Interface
	KubernetesClient() kubernetes.Interface
	KelemetryClient() kelemetryversioned.Interface
	InformerFactory() informers.SharedInformerFactory
	NewInformerFactory(options ...informers.SharedInformerOption) informers.SharedInformerFactory
	EventRecorder(name string) record.EventRecorder
}

type clusterClients struct {
	options options
	Logger  logrus.FieldLogger
	Config  k8sconfig.Config

	clientsLock  sync.RWMutex
	clients      map[string]*client
	targetClient Client

	started int32
}

type client struct {
	name             string
	restConfig       *rest.Config
	dynamicClient    dynamic.Interface
	kubernetesClient *kubernetes.Clientset
	kelemetryClient  kelemetryversioned.Interface
	informerFactory  informers.SharedInformerFactory
	eventBroadcaster record.EventBroadcaster
}

func (clients *clusterClients) Options() manager.Options {
	return &clients.options
}

func (clients *clusterClients) Init() error {
	klog.SetLogger(logWrapper(clients.Logger))

	var err error
	clients.targetClient, err = clients.Cluster(clients.Config.TargetName())
	if err != nil {
		return err
	}

	return nil
}

func (clients *clusterClients) Start(ctx context.Context) error {
	clients.clientsLock.RLock()
	defer clients.clientsLock.RUnlock()

	for _, client := range clients.clients {
		client.informerFactory.Start(ctx.Done())
		client.informerFactory.WaitForCacheSync(ctx.Done())
	}

	atomic.StoreInt32(&clients.started, 1)

	return nil
}

func (clients *clusterClients) Close(ctx context.Context) error {
	return nil
}

func (clients *clusterClients) tryCluster(name string) (Client, bool) {
	clients.clientsLock.RLock()
	defer clients.clientsLock.RUnlock()

	client, exists := clients.clients[name]
	return client, exists
}

func (clients *clusterClients) Cluster(name string) (Client, error) {
	if client, exists := clients.tryCluster(name); exists {
		return client, nil
	}

	clients.clientsLock.Lock()
	defer clients.clientsLock.Unlock()

	client, exists := clients.clients[name]
	if exists {
		return client, nil
	}

	client, err := clients.newClient(name)
	if err != nil {
		return nil, err
	}
	clients.clients[name] = client
	return client, nil
}

func (clients *clusterClients) newClient(name string) (*client, error) {
	cluster := clients.Config.Provide(name)
	if cluster == nil {
		return nil, fmt.Errorf("cluster %q is not available", name)
	}

	config := rest.CopyConfig(cluster.Config)
	config.QPS = clients.options.otherRestQps
	config.Burst = clients.options.otherRestBurst

	if name == clients.Config.TargetName() {
		config.QPS = clients.options.targetRestQps
		config.Burst = clients.options.targetRestBurst
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("dynamic client creation failed: %w", err)
	}

	kubernetesClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client creation failed: %w", err)
	}

	kelemetryClient, err := kelemetryversioned.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kelemetry client creation failed: %w", err)
	}

	var informerFactory informers.SharedInformerFactory
	if atomic.LoadInt32(&clients.started) == 0 {
		// we do not support using informers on clusters created after start
		informerFactory = informers.NewSharedInformerFactory(kubernetesClient, 0)
	}

	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubernetesClient.CoreV1().Events("")})

	return &client{
		name:             name,
		restConfig:       config,
		dynamicClient:    dynamicClient,
		kubernetesClient: kubernetesClient,
		kelemetryClient:  kelemetryClient,
		informerFactory:  informerFactory,
		eventBroadcaster: broadcaster,
	}, nil
}

func (clients *clusterClients) TargetCluster() Client {
	return clients.targetClient
}

func (client *client) ClusterName() string {
	return client.name
}

func (client *client) DynamicClient() dynamic.Interface {
	return client.dynamicClient
}

func (client *client) KubernetesClient() kubernetes.Interface {
	return client.kubernetesClient
}

func (client *client) KelemetryClient() kelemetryversioned.Interface {
	return client.kelemetryClient
}

func (client *client) InformerFactory() informers.SharedInformerFactory {
	if client.informerFactory == nil {
		panic("InformerFactory is only available for clusters requested at Init phase")
	}

	return client.informerFactory
}

func (client *client) NewInformerFactory(options ...informers.SharedInformerOption) informers.SharedInformerFactory {
	return informers.NewSharedInformerFactoryWithOptions(client.kubernetesClient, 0, options...)
}

func (client *client) EventRecorder(name string) record.EventRecorder {
	return client.eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: name})
}

type logrusSink struct{ delegate logrus.FieldLogger }

func (s logrusSink) Init(info logr.RuntimeInfo) {}

// #nosec G115 -- log levels are all small values.
func (s logrusSink) Enabled(level int) bool { return klog.V(klog.Level(level)).Enabled() }

func kvToLogr(keysAndValues []any) logrus.Fields {
	m := logrus.Fields{}
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		m[keysAndValues[i].(string)] = keysAndValues[i+1]
	}
	return m
}

func (s logrusSink) Info(level int, msg string, keysAndValues ...any) {
	delegate := s.delegate.WithFields(kvToLogr(keysAndValues))
	if level > 2 {
		delegate.Debug(msg)
	} else {
		delegate.Info(msg)
	}
}

func (s logrusSink) Error(err error, msg string, keysAndValues ...any) {
	s.delegate.WithFields(kvToLogr(keysAndValues)).Info(msg)
}

func (s logrusSink) WithValues(keysAndValues ...any) logr.LogSink {
	return logrusSink{delegate: s.delegate.WithFields(kvToLogr(keysAndValues))}
}

func (s logrusSink) WithName(name string) logr.LogSink {
	return logrusSink{delegate: s.delegate.WithField("name", name)}
}

func logWrapper(delegate logrus.FieldLogger) logr.Logger {
	return logr.New(logrusSink{delegate: delegate})
}
