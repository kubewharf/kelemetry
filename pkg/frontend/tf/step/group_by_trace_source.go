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
	"math/rand"

	"github.com/jaegertracing/jaeger/model"

	tfscheme "github.com/kubewharf/kelemetry/pkg/frontend/tf/scheme"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide(
		"tf-step/group-by-trace-source-visitor",
		manager.Ptr(&tfscheme.RegisterStep[*tfscheme.VisitorStep[GroupByTraceSourceVisitor]]{Kind: "GroupByTraceSourceVisitor"}),
	)
}

const pseudoSpanNestLevel = "groupByTraceSource"

// Splits span logs into pseudospans grouped by traceSource.
type GroupByTraceSourceVisitor struct {
	ShouldBeGrouped StringFilter `json:"shouldBeGrouped"`
}

func (visitor GroupByTraceSourceVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	nestLevel, hasNestLevel := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel)
	if hasNestLevel && nestLevel.AsString() == pseudoSpanNestLevel {
		// already grouped, don't recurse
		return visitor
	}

	remainingLogs := []model.Log{}

	index := map[string][]model.Log{}
	for _, log := range span.Logs {
		traceSource, hasTraceSource := model.KeyValues(log.Fields).FindByKey(zconstants.TraceSource)
		if hasTraceSource && visitor.ShouldBeGrouped.Test(traceSource.AsString()) {
			index[traceSource.AsString()] = append(index[traceSource.AsString()], log)
		} else {
			remainingLogs = append(remainingLogs, log)
		}
	}

	span.Logs = remainingLogs

	for traceSource, logs := range index {
		newSpanId := model.SpanID(rand.Uint64())
		newSpan := &model.Span{
			TraceID:       span.TraceID,
			SpanID:        newSpanId,
			OperationName: traceSource,
			Flags:         0,
			StartTime:     span.StartTime,
			Duration:      span.Duration,
			Tags: []model.KeyValue{
				{
					Key:   zconstants.NestLevel,
					VType: model.StringType,
					VStr:  pseudoSpanNestLevel,
				},
			},
			Logs: logs,
			Process: &model.Process{
				ServiceName: traceSource,
			},
			ProcessID: "1",
		}
		tree.Add(newSpan, span.SpanID)
	}

	return visitor
}

func (visitor GroupByTraceSourceVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {}
