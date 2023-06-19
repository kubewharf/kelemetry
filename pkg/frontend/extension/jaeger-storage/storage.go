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

package extensionjaeger

import (
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/pkg/metrics"
	jaegerstorage "github.com/jaegertracing/jaeger/plugin/storage"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
	utiljaeger "github.com/kubewharf/kelemetry/pkg/util/jaeger"
)

func init() {
	manager.Global.ProvideListImpl("frontend-extension/jaeger-storage", manager.Ptr(&Storage{}), &manager.List[extension.ProviderFactory]{})
}

type Storage struct {
	manager.BaseComponent
}

func (*Storage) Kind() string { return "JaegerStorage" }

func getViper() (*viper.Viper, error) {
	factoryConfig := jaegerstorage.FactoryConfig{
		SpanWriterTypes:         utiljaeger.SpanStorageTypesToAddFlag,
		SpanReaderType:          "elasticsearch",
		DependenciesStorageType: "elasticsearch",
	}
	storageFactory, err := jaegerstorage.NewFactory(factoryConfig)
	if err != nil {
		return nil, fmt.Errorf("Cannot initialize storage factory: %v", err)
	}

	viper := viper.New()

	goFs := &flag.FlagSet{}
	storageFactory.AddFlags(goFs)
	pfs := &pflag.FlagSet{}
	pfs.AddGoFlagSet(goFs)
	if err := viper.BindPFlags(pfs); err != nil {
		return nil, fmt.Errorf("cannot bind extension storage factory flags: %w", err)
	}

	return viper, nil
}

func (*Storage) Configure(jsonBuf []byte) (extension.Provider, error) {
	providerArgs := &ProviderArgs{}
	if err := json.Unmarshal(jsonBuf, providerArgs); err != nil {
		return nil, fmt.Errorf("cannot parse jaeger-storage config: %w", err)
	}

	viper, err := getViper()
	if err != nil {
		return nil, err
	}

	for argKey, argValue := range providerArgs.StorageArgs {
		viper.Set(argKey, argValue)
	}

	storageType := viper.GetString("span-storage.type")
	if len(storageType) == 0 {
		return nil, fmt.Errorf("span-storage.type must be specified for jaeger-storage extension")
	}

	factoryConfig := jaegerstorage.FactoryConfig{
		SpanReaderType:          storageType,
		SpanWriterTypes:         []string{storageType},
		DependenciesStorageType: storageType,
	}

	storageFactory, err := jaegerstorage.NewFactory(factoryConfig)
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate extension storage factory: %w", err)
	}

	zapLogger := zap.NewExample()

	storageFactory.InitFromViper(viper, zapLogger)
	if err := storageFactory.Initialize(metrics.NullFactory, zapLogger); err != nil {
		return nil, fmt.Errorf("cannot initialize extension storage: %w", err)
	}

	reader, err := storageFactory.CreateSpanReader()
	if err != nil {
		return nil, fmt.Errorf("cannot create span reader for extension storage: %w", err)
	}

	return &Provider{
		Reader: reader,

		Service:     providerArgs.Service,
		Operation:   providerArgs.Operation,
		TagTemplate: providerArgs.TagTemplate,
	}, nil
}

type ProviderArgs struct {
	StorageArgs map[string]string `json:"storageArgs"`

	Service     string            `json:"service"`
	Operation   string            `json:"operation"`
	TagTemplate map[string]string `json:"tagTemplate"`
}

type Provider struct {
	Reader spanstore.Reader

	Service     string
	Operation   string
	TagTemplate map[string]string
}

func (provider *Provider) Fetch(
	object util.ObjectRef,
	tags model.KeyValues,
	start, end time.Duration,
) (extension.FetchResult, error) {
	panic("todo")
}

func (provider *Provider) LoadCache(identifier any) (extension.FetchResult, error) {
	panic("todo")
}
