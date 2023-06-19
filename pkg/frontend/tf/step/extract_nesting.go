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

	tfscheme "github.com/kubewharf/kelemetry/pkg/frontend/tf/scheme"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideListImpl(
		"tf-step/extract-nesting-visitor",
		manager.Ptr(&tfscheme.VisitorStep[ExtractNestingVisitor]{}),
		&manager.List[tfscheme.RegisteredStep]{},
	)
}

// Deletes spans matching MatchesNestLevel and brings their children one level up.
type ExtractNestingVisitor struct {
	// NestLevels returns true if the span should be deleted.
	// It is only called on spans with the tag zconstants.Nesting
	MatchesNestLevel StringFilter `json:"matchesNestLevel"`
}

func (ExtractNestingVisitor) Kind() string { return "ExtractNestingVisitor" }

func (visitor ExtractNestingVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	if len(span.References) == 0 {
		// we cannot extract the root span
		return visitor
	}

	if nestLevel, ok := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel); ok {
		if visitor.MatchesNestLevel.Test(nestLevel.AsString()) {
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
