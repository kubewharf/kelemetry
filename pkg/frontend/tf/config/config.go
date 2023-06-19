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

package tfconfig

import (
	"strconv"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("jaeger-transform-config", manager.Ptr[Provider](&mux{
		Mux: manager.NewMux("jaeger-transform-config", false),
	}))
}

type Provider interface {
	Names() []string
	DefaultName() string
	DefaultId() Id
	GetByName(name string) *Config
	GetById(id Id) *Config
}

type Id uint32

func (id *Id) UnmarshalText(text []byte) error {
	i, err := strconv.ParseUint(string(text), 16, 32)
	if err != nil {
		return err
	}
	*id = Id(uint32(i))
	return nil
}

type Config struct {
	// The config ID, used to generate the cache ID.
	Id Id
	// The config name, used in search page display.
	Name string
	// If true, only displays the spans below the matched span.
	// If false, displays the whole trace including parent and sibling spans.
	UseSubtree bool
	Extensions []extension.Provider
	// The steps to transform the tree
	Steps []Step
}

func (config *Config) Clone() *Config {
	steps := make([]Step, len(config.Steps))
	copy(steps, config.Steps) // no need to deep clone each step

	return &Config{
		Id:         config.Id,
		Name:       config.Name,
		UseSubtree: config.UseSubtree,
		Steps:      steps,
	}
}

type mux struct {
	*manager.Mux
}

func (mux *mux) Names() []string               { return mux.Impl().(Provider).Names() }
func (mux *mux) DefaultName() string           { return mux.Impl().(Provider).DefaultName() }
func (mux *mux) DefaultId() Id                 { return mux.Impl().(Provider).DefaultId() }
func (mux *mux) GetByName(name string) *Config { return mux.Impl().(Provider).GetByName(name) }
func (mux *mux) GetById(id Id) *Config         { return mux.Impl().(Provider).GetById(id) }
