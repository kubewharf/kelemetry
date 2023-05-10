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
	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-registry", manager.Func(func() *Registry {
		return &Registry{
			TagProviderNameIndex: make(map[string]int),
			QuantifierNameIndex:  make(map[string]int),
		}
	}))
}

type Registry struct {
	manager.BaseComponent
	TagProviders         []TagProvider
	TagProviderNameIndex map[string]int
	Quantifiers          []Quantifier
	QuantifierNameIndex  map[string]int
}

type IndexedTagProvider struct {
	Provider                   TagProvider
	StartTagIndex, EndTagIndex int
}

type TagProvider interface {
	TagNames() []string

	ProvideValues(message *audit.Message, slice []string)
}

type MetricType uint8

const (
	MetricTypeCount = MetricType(iota)
	MetricTypeGauge
	MetricTypeHistogram
)

type Quantifier interface {
	Name() string
	Type() MetricType
	Quantify(message *audit.Message) (int64, bool)
}

func (registry *Registry) AddTagProvider(provider TagProvider) {
	providerIndex := len(registry.TagProviders)
	registry.TagProviders = append(registry.TagProviders, provider)

	for _, name := range provider.TagNames() {
		registry.TagProviderNameIndex[name] = providerIndex
	}
}

func (registry *Registry) AddQuantifier(quantifier Quantifier) {
	quantifierIndex := len(registry.Quantifiers)
	registry.Quantifiers = append(registry.Quantifiers, quantifier)
	registry.QuantifierNameIndex[quantifier.Name()] = quantifierIndex
}
