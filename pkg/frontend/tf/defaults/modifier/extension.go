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
	"encoding/json"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideListImpl(
		"tf-modifier/extension",
		manager.Ptr(&ExtensionModifierFactory{}),
		&manager.List[tfconfig.ModifierFactory]{},
	)
}

type ExtensionModifierOptions struct {
	enable bool
}

func (options *ExtensionModifierOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "jaeger-tf-extension-modifier-enable", true, "enable extension modifier")
}

func (options *ExtensionModifierOptions) EnableFlag() *bool { return &options.enable }

type ExtensionModifierFactory struct {
	options      ExtensionModifierOptions
	ProviderList *manager.List[extension.ProviderFactory]
}

var _ manager.Component = &ExtensionModifierFactory{}

func (m *ExtensionModifierFactory) Options() manager.Options        { return &m.options }
func (m *ExtensionModifierFactory) Init() error                     { return nil }
func (m *ExtensionModifierFactory) Start(ctx context.Context) error { return nil }
func (m *ExtensionModifierFactory) Close(ctx context.Context) error { return nil }

func (*ExtensionModifierFactory) ListIndex() string    { return "extension" }
func (*ExtensionModifierFactory) ModifierName() string { return "extension" }

func (m *ExtensionModifierFactory) Build(jsonBuf []byte) (tfconfig.Modifier, error) {
	var hasKind struct {
		Kind string `json:"kind"`
	}

	if err := json.Unmarshal(jsonBuf, &hasKind); err != nil {
		return nil, fmt.Errorf("no extension kind specified: %w", err)
	}

	matchedFactory, hasMatchedFactory := m.ProviderList.Indexed[hasKind.Kind]
	if !hasMatchedFactory {
		return nil, fmt.Errorf("no extension provider with kind %q", hasKind.Kind)
	}

	provider, err := matchedFactory.Configure(jsonBuf)
	if err != nil {
		return nil, fmt.Errorf("parse extension provider config error: %w", err)
	}

	return &ExtensionModifier{provider: provider}, nil
}

type ExtensionModifier struct {
	provider extension.Provider
}

func (m *ExtensionModifier) Modify(config *tfconfig.Config) {
	config.Extensions = append(config.Extensions, m.provider)
}
