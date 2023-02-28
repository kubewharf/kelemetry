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
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("jaeger-transform-config", newMux)
}

type Provider interface {
	Names() []string
	DefaultName() string
	DefaultId() Id
	GetByName(name string) *Config
	GetById(id Id) *Config
}

type Id uint32

type Config struct {
	// The config ID, used to generate the cache ID.
	Id Id
	// The config name, used in search page display.
	Name string
	// If true, only displays the spans below the matched span.
	// If false, displays the whole trace including parent and sibling spans.
	UseSubtree bool
	// The steps to transform the tree
	Steps []Step
}

type Step struct {
	Visitor tftree.TreeVisitor
}

type mux struct {
	*manager.Mux
}

func newMux() Provider {
	return &mux{
		Mux: manager.NewMux("jaeger-transform-config", false),
	}
}

func (mux *mux) Names() []string               { return mux.Impl().(Provider).Names() }
func (mux *mux) DefaultName() string           { return mux.Impl().(Provider).DefaultName() }
func (mux *mux) DefaultId() Id                 { return mux.Impl().(Provider).DefaultId() }
func (mux *mux) GetByName(name string) *Config { return mux.Impl().(Provider).GetByName(name) }
func (mux *mux) GetById(id Id) *Config         { return mux.Impl().(Provider).GetById(id) }
