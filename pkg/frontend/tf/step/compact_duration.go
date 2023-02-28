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
	"time"

	"github.com/jaegertracing/jaeger/model"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

// Splits span logs into pseudospans grouped by traceSource.
type CompactDurationVisitor struct{}

func (visitor CompactDurationVisitor) Enter(tree tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	return visitor
}

func (visitor CompactDurationVisitor) Exit(tree tftree.SpanTree, span *model.Span) {
	// use exit hook to use compact results of children

	if tree.Root == span {
		// keep the root in full duration for display
		return
	}

	if _, hasNestLevel := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel); !hasNestLevel {
		return
	}

	var r timeRange

	for childId := range tree.Children(span.SpanID) {
		child := tree.Span(childId)

		childStart := child.StartTime
		childEnd := childStart.Add(child.Duration)

		r.union(childStart, childEnd)
	}

	for _, log := range span.Logs {
		r.union(log.Timestamp, log.Timestamp.Add(zconstants.DummyDuration))
	}

	if r.hasStart && r.hasEnd {
		span.StartTime = r.start
		span.Duration = r.end.Sub(r.start)
	}
}

type timeRange struct {
	start, end       time.Time
	hasStart, hasEnd bool
}

func (r *timeRange) union(start time.Time, end time.Time) {
	if !r.hasStart || r.start.After(start) {
		r.hasStart = true
		r.start = start
	}

	if !r.hasEnd || r.end.Before(end) {
		r.hasEnd = true
		r.end = end
	}
}
