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

package tfconfigfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideMuxImpl("jaeger-transform-config/file", manager.Ptr(&FileProvider{
		configs:        make(map[tfconfig.Id]registeredConfig),
		nameToConfigId: make(map[string]tfconfig.Id),
	}), tfconfig.Provider.DefaultId)
}

type options struct {
	file string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.file, "jaeger-transform-config-file", "hack/tfconfig.yaml", "path to tfconfig file")
}

func (options *options) EnableFlag() *bool { return nil }

type FileProvider struct {
	manager.MuxImplBase

	RegisteredSteps     *manager.List[tfconfig.RegisteredStep]
	RegisteredModifiers *manager.List[tfconfig.ModifierFactory]

	options        options
	names          []string
	configs        map[tfconfig.Id]registeredConfig
	nameToConfigId map[string]tfconfig.Id
	defaultConfig  tfconfig.Id
}

type registeredConfig struct {
	modifierClasses sets.Set[string]
	config          *tfconfig.Config
}

var (
	_ manager.Component = &FileProvider{}
	_ tfconfig.Provider = &FileProvider{}
)

func (p *FileProvider) MuxImplName() (name string, isDefault bool) { return "default", true }

func (p *FileProvider) Options() manager.Options { return &p.options }

func (p *FileProvider) Init() error {
	file, err := os.Open(p.options.file)
	if err != nil {
		return fmt.Errorf("cannot open tfconfig file: %w", err)
	}
	defer file.Close()

	yamlBytes, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read tfconfig error: %w", err)
	}

	jsonBytes, err := yaml.ToJSON(yamlBytes)
	if err != nil {
		return fmt.Errorf("parse tfconfig YAML error: %w", err)
	}

	if err := p.loadJsonBytes(jsonBytes); err != nil {
		return err
	}

	return nil
}

func (p *FileProvider) loadJsonBytes(jsonBytes []byte) error {
	type Batch struct {
		Name  string          `json:"name"`
		Steps json.RawMessage `json:"steps"`
	}

	type modifierConfig struct {
		Priority     int64           `json:"priority"`
		DisplayName  string          `json:"displayName"`
		ModifierName string          `json:"modifierName"`
		Args         json.RawMessage `json:"args"`
	}

	var file struct {
		Modifiers map[tfconfig.Id]modifierConfig `json:"modifiers"`
		Batches   []Batch                        `json:"batches"`
		Configs   []struct {
			Id         tfconfig.Id     `json:"id"`
			Name       string          `json:"name"`
			UseSubtree bool            `json:"useSubtree"`
			Steps      json.RawMessage `json:"steps"`
		}
	}
	if err := json.Unmarshal(jsonBytes, &file); err != nil {
		return fmt.Errorf("parse tfconfig error: %w", err)
	}

	type knownModifier struct {
		class    string
		priority int64
		fn       func(config *tfconfig.Config)
	}

	modifiers := make([]knownModifier, 0, len(file.Modifiers))

	for bitmask, modifierConfig := range file.Modifiers {
		bitmask := bitmask
		modifierConfig := modifierConfig

		displayName := modifierConfig.DisplayName

		factory, hasFactory := p.RegisteredModifiers.Indexed[modifierConfig.ModifierName]
		if !hasFactory {
			return fmt.Errorf("parse tfconfig modifier error: unknown modifier name %q", modifierConfig.ModifierName)
		}

		modifier, err := factory.Build([]byte(modifierConfig.Args))
		if err != nil {
			return fmt.Errorf("parse tfconfig modifier error: invalid modifier args: %w", err)
		}

		modifiers = append(modifiers, knownModifier{
			class:    modifier.ModifierClass(),
			priority: modifierConfig.Priority,
			fn: func(config *tfconfig.Config) {
				config.Id |= bitmask
				config.Name += fmt.Sprintf(" [%s]", displayName)
				modifier.Modify(config)
			},
		})
	}

	// Sort modifiers by the ascending order of priorities
	sort.Slice(modifiers, func(i, j int) bool { return modifiers[i].priority < modifiers[j].priority })

	batches := map[string][]tfconfig.Step{}
	for _, batch := range file.Batches {
		steps, err := tfconfig.ParseSteps(batch.Steps, batches, p.RegisteredSteps.Indexed)
		if err != nil {
			return fmt.Errorf("parse tfconfig batch error: %w", err)
		}

		batches[batch.Name] = steps
	}

	for _, raw := range file.Configs {
		steps, err := tfconfig.ParseSteps(raw.Steps, batches, p.RegisteredSteps.Indexed)
		if err != nil {
			return fmt.Errorf("parse tfconfig step error: %w", err)
		}

		config := &tfconfig.Config{
			Id:         raw.Id,
			Name:       raw.Name,
			UseSubtree: raw.UseSubtree,
			Steps:      steps,
		}

		p.register(registeredConfig{config: config, modifierClasses: sets.New[string]()})
	}

	for _, modifier := range modifiers {
		var newEntries []registeredConfig

		for _, config := range p.configs {
			newConfig := config.config.Clone()
			modifier.fn(newConfig)

			if config.modifierClasses.Has(modifier.class) {
				// incompatible combination
				continue
			}

			classes := config.modifierClasses.Clone()
			classes.Insert(modifier.class)

			newEntries = append(newEntries, registeredConfig{
				config:          newConfig,
				modifierClasses: classes,
			})
		}

		for _, newEntry := range newEntries {
			p.register(newEntry)
		}
	}

	return nil
}

func (p *FileProvider) register(regConfig registeredConfig) {
	config := regConfig.config

	p.names = append(p.names, config.Name)
	p.configs[config.Id] = regConfig
	p.nameToConfigId[config.Name] = config.Id
}

func (p *FileProvider) Start(ctx context.Context) error { return nil }
func (p *FileProvider) Close(ctx context.Context) error { return nil }

func (p *FileProvider) Names() []string {
	return p.names
}

func (p *FileProvider) DefaultName() string { return p.configs[p.defaultConfig].config.Name }

func (p *FileProvider) DefaultId() tfconfig.Id { return p.defaultConfig }

func (p *FileProvider) GetByName(name string) *tfconfig.Config {
	id, exists := p.nameToConfigId[name]
	if !exists {
		return nil
	}
	return p.configs[id].config
}

func (p *FileProvider) GetById(id tfconfig.Id) *tfconfig.Config { return p.configs[id].config }
