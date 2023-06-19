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

package tfmodifier

import (
	"context"

	"github.com/spf13/pflag"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideListImpl("tf-modifier/exclusive", manager.Ptr(&ExclusiveModifier{}), &manager.List[tfconfig.Modifier]{})
}

type ExclusiveModifierOptions struct {
	enable bool
}

func (options *ExclusiveModifierOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "jaeger-tf-exclusive-modifier-enable", true, "enable exclusive modifier and list it in frontend")
}

func (options *ExclusiveModifierOptions) EnableFlag() *bool { return &options.enable }

type ExclusiveModifier struct {
	options ExclusiveModifierOptions
}

var _ manager.Component = &ExclusiveModifier{}

func (m *ExclusiveModifier) Options() manager.Options        { return &m.options }
func (m *ExclusiveModifier) Init() error                     { return nil }
func (m *ExclusiveModifier) Start(ctx context.Context) error { return nil }
func (m *ExclusiveModifier) Close(ctx context.Context) error { return nil }

func (*ExclusiveModifier) ModifierName() string { return "exclusive" }

func (*ExclusiveModifier) Modify(config *tfconfig.Config) {
	config.UseSubtree = true
}
