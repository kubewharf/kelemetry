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
	manager.Global.Provide(
		"tf-step/prune-childless-visitor",
		manager.Ptr(&tfscheme.RegisterStep[*tfscheme.VisitorStep[PruneChildlessVisitor]]{Kind: "PruneChildlessVisitor"}),
	)
}

type PruneChildlessVisitor struct{}

func (visitor PruneChildlessVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	return visitor
}

// Prune in postorder traversal to recursively remove higher pseudospans without leaves.
func (visitor PruneChildlessVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {
	if _, hasTag := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel); hasTag {
		if len(tree.Children(span.SpanID)) == 0 && span.SpanID != tree.Root.SpanID {
			tree.Delete(span.SpanID)
		}
	}
}
