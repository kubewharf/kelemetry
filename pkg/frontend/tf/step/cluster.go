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

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
)

type ClusterNameVisitor struct {
	parentCluster string
}

func (visitor ClusterNameVisitor) Enter(tree tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	var cluster string

	for _, tag := range span.Tags {
		if tag.Key == "cluster" {
			cluster = tag.VStr
		}
	}

	if span.SpanID != tree.Root.SpanID && cluster != visitor.parentCluster {
		span.Process.ServiceName = cluster
	}

	return ClusterNameVisitor{parentCluster: cluster}
}

func (visitor ClusterNameVisitor) Exit(tree tftree.SpanTree, span *model.Span) {}
