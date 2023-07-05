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

package kelemetrix

import (
	"context"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-registry", manager.Ptr(NewRegistry()))
}

type Registry struct {
	TagProviderFactories *manager.List[TagProviderFactory]
	TagProviders         []TagProvider
	TagProviderNameIndex map[string]int

	Quantifiers         *manager.List[Quantifier]
	QuantifierNameIndex map[string]int
}

func NewRegistry() *Registry {
	return &Registry{
		TagProviders:         []TagProvider{},
		TagProviderNameIndex: make(map[string]int),
		QuantifierNameIndex:  make(map[string]int),
	}
}

func NewMockRegistry(
	tagProviders []TagProvider,
	quantifiers []Quantifier,
) *Registry {
	registry := NewRegistry()
	for _, tagProvider := range tagProviders {
		registry.addTagProvider(tagProvider)
	}
	registry.Quantifiers = &manager.List[Quantifier]{
		Impls: quantifiers,
	}
	for i, quantifier := range quantifiers {
		registry.QuantifierNameIndex[quantifier.Name()] = i
	}

	return registry
}

func (registry *Registry) Options() manager.Options { return &manager.NoOptions{} }

func (registry *Registry) Init() error {
	for _, factory := range registry.TagProviderFactories.Impls {
		for _, provider := range factory.GetTagProviders() {
			registry.addTagProvider(provider)
		}
	}

	for i, quantifier := range registry.Quantifiers.Impls {
		registry.QuantifierNameIndex[quantifier.Name()] = i
	}

	return nil
}

func (registry *Registry) Start(ctx context.Context) error { return nil }
func (registry *Registry) Close(ctx context.Context) error { return nil }

type TagProviderFactory interface {
	GetTagProviders() []TagProvider
}

type TagProvider interface {
	TagNames() []string

	ProvideValues(message *audit.Message, slice []string)
}

type MetricType uint8

const (
	MetricTypeCount = MetricType(iota)
	MetricTypeHistogram
	MetricTypeSummary
)

type Quantifier interface {
	Name() string
	Type() MetricType
	Quantify(message *audit.Message) (float64, bool)
}

func (registry *Registry) addTagProvider(provider TagProvider) {
	providerIndex := len(registry.TagProviders)
	registry.TagProviders = append(registry.TagProviders, provider)

	for _, name := range provider.TagNames() {
		registry.TagProviderNameIndex[name] = providerIndex
	}
}
