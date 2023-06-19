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
	"fmt"
	"strings"

	"github.com/jaegertracing/jaeger/model"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideListImpl(
		"tf-step/service-operation-replace-visitor",
		manager.Ptr(&tfconfig.VisitorStep[ServiceOperationReplaceVisitor]{}),
		&manager.List[tfconfig.RegisteredStep]{},
	)
	manager.Global.ProvideListImpl(
		"tf-step/cluster-name-visitor",
		manager.Ptr(&tfconfig.VisitorStep[ClusterNameVisitor]{}),
		&manager.List[tfconfig.RegisteredStep]{},
	)
}

type ReplaceDest string

const (
	ReplaceDestService   ReplaceDest = "service"
	ReplaceDestOperation ReplaceDest = "operation"
)

type ServiceOperationReplaceVisitor struct {
	TraceSource string      `json:"traceSource"`
	Dest        ReplaceDest `json:"dest"`
	Source      []string    `json:"source"`
}

func (ServiceOperationReplaceVisitor) Kind() string { return "ServiceOperationReplaceVisitor" }

func (visitor ServiceOperationReplaceVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	tags := model.KeyValues(span.Tags)

	if tag, hasTag := tags.FindByKey(zconstants.TraceSource); !hasTag || tag.VStr != visitor.TraceSource {
		return visitor
	}

	var sources []string
	for _, source := range visitor.Source {
		if tag, hasTag := tags.FindByKey(source); hasTag {
			tagStr := tag.AsStringLossy()
			if tagStr != "" && !(source == "namespace" && tagStr == "default") {
				sources = append(sources, tagStr)
			}
		}
	}

	str := strings.Join(sources, " ")
	switch visitor.Dest {
	case ReplaceDestService:
		span.Process.ServiceName = str
	case ReplaceDestOperation:
		span.OperationName = str
	}

	return visitor
}

func (visitor ServiceOperationReplaceVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {}

type ClusterNameVisitor struct {
	parentCluster string
}

func (ClusterNameVisitor) Kind() string { return "ClusterNameVisitor" }

func (visitor ClusterNameVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	tags := model.KeyValues(span.Tags)

	cluster, hasCluster := tags.FindByKey("cluster")
	if hasCluster {
		cluster := cluster.VStr
		if cluster != visitor.parentCluster {
			span.Process.ServiceName = fmt.Sprintf("%s %s", cluster, span.Process.ServiceName)
		}

		return ClusterNameVisitor{parentCluster: cluster}
	}

	return visitor
}

func (visitor ClusterNameVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {}
