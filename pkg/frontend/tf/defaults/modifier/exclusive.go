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
	manager.Global.ProvideListImpl(
		"tf-modifier/exclusive",
		manager.Ptr(&ExclusiveModifierFactory{}),
		&manager.List[tfconfig.ModifierFactory]{},
	)
}

type ExclusiveModifierOptions struct {
	enable bool
}

func (options *ExclusiveModifierOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "jaeger-tf-exclusive-modifier-enable", true, "enable exclusive modifier and list it in frontend")
}

func (options *ExclusiveModifierOptions) EnableFlag() *bool { return &options.enable }

type ExclusiveModifierFactory struct {
	options ExclusiveModifierOptions
}

var _ manager.Component = &ExclusiveModifierFactory{}

func (m *ExclusiveModifierFactory) Options() manager.Options        { return &m.options }
func (m *ExclusiveModifierFactory) Init() error                     { return nil }
func (m *ExclusiveModifierFactory) Start(ctx context.Context) error { return nil }
func (m *ExclusiveModifierFactory) Close(ctx context.Context) error { return nil }

func (*ExclusiveModifierFactory) ListIndex() string    { return "exclusive" }
func (*ExclusiveModifierFactory) ModifierName() string { return "exclusive" }

func (*ExclusiveModifierFactory) Build(jsonBuf []byte) (tfconfig.Modifier, error) {
	return &ExclusiveModifier{}, nil
}

type ExclusiveModifier struct{}

func (*ExclusiveModifier) Modify(config *tfconfig.Config) {
	config.UseSubtree = true
}
