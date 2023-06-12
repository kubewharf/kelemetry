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

	"github.com/spf13/pflag"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("frontend-tf-scheme", manager.Ptr[Scheme](&scheme{kindMap: map[string]*StepDef{}}))
}

type Scheme interface {
	Register(stepDef *StepDef)

	Unmarshal(batches map[string][]Step, buf []byte) (Step, error)
}

type StepDef struct {
	kind          string
	unmarshalJson func(buf []byte) (Step, error)
}

func NewStepDef[T Step](kind string) *StepDef {
	return &StepDef{
		kind: kind,
		unmarshalJson: func(buf []byte) (Step, error) {
			var step T
			if err := json.Unmarshal(buf, &step); err != nil {
				return nil, fmt.Errorf("cannot parse JSON object as %s: %w", kind, err)
			}

			return step, nil
		},
	}
}

type scheme struct {
	manager.BaseComponent

	kindMap map[string]*StepDef
}

func (scheme *scheme) Register(stepDef *StepDef) {
	scheme.kindMap[stepDef.kind] = stepDef
}

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

type RegisterStep[T Step] struct {
	Scheme Scheme
	Kind   string
}

func (s *RegisterStep[T]) Options() manager.Options { return s }

func (s *RegisterStep[T]) Setup(fs *pflag.FlagSet) {}

func (s *RegisterStep[T]) EnableFlag() *bool { return pointer.Bool(true) }

func (s *RegisterStep[T]) Init() error {
	s.Scheme.Register(NewStepDef[T](s.Kind))
	return nil
}

func (*RegisterStep[T]) Start(context.Context) error { return nil }

func (*RegisterStep[T]) Close(context.Context) error { return nil }
