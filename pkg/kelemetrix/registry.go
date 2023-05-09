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
	"strings"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-registry", manager.Ptr(&Registry{}))
}

type Registry struct {
	manager.BaseComponent
	tagNames     []string
	tagProviders []indexedTagProvider
	quantifiers  []Quantifier
}

type indexedTagProvider struct {
	provider                   TagProvider
	startTagIndex, endTagIndex int
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
	TagSets() map[string]func(string) bool
	Quantify(message *audit.Message) (int64, bool)
}

func ParseTagSet(s string) func(string) bool {
	if s == "*" {
		return func(_ string) bool { return true }
	}

	tags := map[string]struct{}{}
	for _, tag := range strings.Split(s, "*") {
		tags[tag] = struct{}{}
	}
	return func(tag string) bool {
		_, ok := tags[tag]
		return ok
	}
}

func (registry *Registry) AddTagProvider(provider TagProvider) {
	names := provider.TagNames()

	indexed := indexedTagProvider{
		provider:      provider,
		startTagIndex: len(registry.tagNames),
		endTagIndex:   len(registry.tagNames) + len(names),
	}

	registry.tagNames = append(registry.tagNames, names...)
	registry.tagProviders = append(registry.tagProviders, indexed)
}

func (registry *Registry) AddQuantifier(quantifier Quantifier) {
	registry.quantifiers = append(registry.quantifiers, quantifier)
}
