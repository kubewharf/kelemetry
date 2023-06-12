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
	"encoding/json"
	"fmt"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
)

type Step interface {
	Run(tree *tftree.SpanTree)
}

type VisitorStep[V tftree.TreeVisitor] struct {
	Visitor V
}

func (step *VisitorStep[V]) UnmarshalJSON(buf []byte) error {
	return json.Unmarshal(buf, &step.Visitor)
}

func (step *VisitorStep[V]) Run(tree *tftree.SpanTree) {
	tree.Visit(step.Visitor)
}

type BatchStep struct {
	Steps []Step
}

func (step *BatchStep) Run(tree *tftree.SpanTree) {
	for _, child := range step.Steps {
		child.Run(tree)
	}
}

func ParseSteps(buf []byte, scheme Scheme, batches map[string][]Step) ([]Step, error) {
	raw := []json.RawMessage{}

	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, fmt.Errorf("json parse error: %w", err)
	}

	steps := make([]Step, len(raw))
	for i, stepBuf := range raw {
		step, err := scheme.Unmarshal(batches, stepBuf)
		if err != nil {
			return nil, err
		}

		steps[i] = step
	}

	return steps, nil
}
