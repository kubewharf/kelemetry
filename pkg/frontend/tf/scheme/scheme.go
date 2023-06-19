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

package tfscheme

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("frontend-tf-scheme", manager.Ptr[Scheme](&scheme{kindMap: map[string]*StepDef{}}))
}

type Scheme interface {
	Unmarshal(batches map[string][]Step, buf []byte) (Step, error)
}

type StepDef struct {
	unmarshalJson func(buf []byte) (Step, error)
}

type scheme struct {
	Steps *manager.List[RegisteredStep]

	kindMap map[string]*StepDef
}

func (scheme *scheme) Options() manager.Options { return &manager.NoOptions{} }

func (scheme *scheme) Init() error {
	for _, step := range scheme.Steps.Impls {
		scheme.kindMap[step.Kind()] = &StepDef{
			unmarshalJson: step.UnmarshalNewJSON,
		}
	}

	return nil
}

func (scheme *scheme) Start(ctx context.Context) error { return nil }
func (scheme *scheme) Close(ctx context.Context) error { return nil }

func (scheme *scheme) Unmarshal(batches map[string][]Step, buf []byte) (Step, error) {
	var hasKind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(buf, &hasKind); err != nil {
		return nil, fmt.Errorf("JSON object must have \"kind\" field: %w", err)
	}

	if hasKind.Kind == "Batch" {
		var hasBatchName struct {
			BatchName string `json:"batchName"`
		}
		if err := json.Unmarshal(buf, &hasBatchName); err != nil {
			return nil, fmt.Errorf("JSON object must have \"batchName\" field: %w", err)
		}

		batch, exists := batches[hasBatchName.BatchName]
		if !exists {
			return nil, fmt.Errorf("unknown batch %q", hasBatchName.BatchName)
		}

		return &BatchStep{Steps: batch}, nil
	}

	def, exists := scheme.kindMap[hasKind.Kind]
	if !exists {
		return nil, fmt.Errorf("unknown kind %q", hasKind.Kind)
	}

	return def.unmarshalJson(buf)
}
