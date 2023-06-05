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

package config

import (
	"context"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-config", manager.Ptr[Provider](&comp{}))
}

type Provider interface {
	Get() *Config
}

type MockProvider struct {
	Config *Config
}

func (provider *MockProvider) Get() *Config { return provider.Config }

type Config struct {
	Metrics []Metric
}

type Metric struct {
	Name            string
	Quantifier      string
	Tags            []string
	TagFilters      []TagFilter
	QuantityFilters []QuantityFilter
}

type TagFilter struct {
	Tag     string
	OneOf   []string
	IsRegex bool
	Negate  bool
}

type QuantityFilter struct {
	Quantity  string
	Operator  QuantityOperator
	Threshold float64
}

type QuantityOperator string

const (
	QuantityOperatorLess      QuantityOperator = "<"
	QuantityOperatorGreater   QuantityOperator = ">"
	QuantityOperatorLessEq    QuantityOperator = "<="
	QuantityOperatorGreaterEq QuantityOperator = ">="
)

type compOptions struct {
	path string
}

func (options *compOptions) Setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.path, "kelemetrix-config-path", "hack/kelemetrix.toml", "path to kelemetrix config")
}

func (options *compOptions) EnableFlag() *bool { return nil }

type comp struct {
	options compOptions

	value Config
}

func (config *comp) Options() manager.Options { return &config.options }

func (config *comp) Init() error {
	file, err := os.Open(config.options.path)
	if err != nil {
		return fmt.Errorf("cannot open config file %s: %w", config.options.path, err)
	}

	if err := toml.NewDecoder(file).DisallowUnknownFields().Decode(&config.value); err != nil {
		return fmt.Errorf("error decoding config file %s: %w", config.options.path, err)
	}

	return nil
}

func (config *comp) Start(ctx context.Context) error { return nil }
func (config *comp) Close(ctx context.Context) error { return nil }

func (config *comp) Get() *Config {
	return &config.value
}
