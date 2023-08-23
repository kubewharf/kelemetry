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

package tfstep

import (
	"github.com/jaegertracing/jaeger/model"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
	utilmarshal "github.com/kubewharf/kelemetry/pkg/util/marshal"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideListImpl(
		"tf-step/extract-nesting-visitor",
		manager.Ptr(&tfconfig.VisitorStep[ExtractNestingVisitor]{}),
		&manager.List[tfconfig.RegisteredStep]{},
	)
}

// Deletes spans matching MatchesPseudoType and brings their children one level up.
type ExtractNestingVisitor struct {
	// Filters the trace sources to delete.
	MatchesPseudoType utilmarshal.StringFilter `json:"matchesPseudoType"`
}

func (ExtractNestingVisitor) Kind() string { return "ExtractNestingVisitor" }

func (visitor ExtractNestingVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	if len(span.References) == 0 {
		// we cannot extract the root span
		return visitor
	}

	if pseudoType, ok := model.KeyValues(span.Tags).FindByKey(zconstants.PseudoType); ok {
		if visitor.MatchesPseudoType.Matches(pseudoType.AsString()) {
			childrenMap := tree.Children(span.SpanID)
			childrenCopy := make([]model.SpanID, 0, len(childrenMap))
			for childId := range childrenMap {
				childrenCopy = append(childrenCopy, childId)
			}

			for _, child := range childrenCopy {
				tree.Move(child, span.References[0].SpanID)
			}

			tree.Delete(span.SpanID)
		}
	}

	return visitor
}
func (visitor ExtractNestingVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {}
