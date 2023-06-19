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
	"encoding/json"
	"fmt"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

type Step interface {
	Run(tree *tftree.SpanTree)
}

type RegisteredStep interface {
	Step

	Kind() string

	UnmarshalNewJSON(buf []byte) (Step, error)
}

type RegisteredVisitor interface {
	tftree.TreeVisitor

	Kind() string
}

type VisitorStep[V RegisteredVisitor] struct {
	manager.BaseComponent

	Visitor V `managerSkipFill:""`
}

func (step *VisitorStep[V]) Options() manager.Options { return &manager.AlwaysEnableOptions{} }

func (step *VisitorStep[V]) UnmarshalJSON(buf []byte) error {
	return json.Unmarshal(buf, &step.Visitor)
}

func (step *VisitorStep[V]) Run(tree *tftree.SpanTree) {
	tree.Visit(step.Visitor)
}

func (step *VisitorStep[V]) Kind() string { return step.Visitor.Kind() }

func (*VisitorStep[V]) UnmarshalNewJSON(buf []byte) (Step, error) {
	step := &VisitorStep[V]{}
	if err := step.UnmarshalJSON(buf); err != nil {
		return nil, err
	}
	return step, nil
}

type BatchStep struct {
	Steps []Step
}

func (step *BatchStep) Run(tree *tftree.SpanTree) {
	for _, child := range step.Steps {
		child.Run(tree)
	}
}

func ParseSteps(buf []byte, batches map[string][]Step, registeredSteps []RegisteredStep) ([]Step, error) {
	raw := []json.RawMessage{}

	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, fmt.Errorf("json parse error: %w", err)
	}

	steps := make([]Step, len(raw))
	for i, stepBuf := range raw {
		step, err := UnmarshalStep(batches, stepBuf, registeredSteps)
		if err != nil {
			return nil, err
		}

		steps[i] = step
	}

	return steps, nil
}

func UnmarshalStep(batches map[string][]Step, buf []byte, registeredSteps []RegisteredStep) (Step, error) {
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

	for _, step := range registeredSteps {
		if step.Kind() == hasKind.Kind {
			return step.UnmarshalNewJSON(buf)
		}
	}

	return nil, fmt.Errorf("unknown kind %q", hasKind.Kind)
}
