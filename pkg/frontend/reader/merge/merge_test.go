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

package merge_test

import (
	"context"
	"testing"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/kelemetry/pkg/frontend/reader/merge"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func newTrace(id uint64, key utilobject.Key, startTime int64, endTime int64, links []merge.TargetLink) merge.TraceWithMetadata[uint64] {
	traceId := model.NewTraceID(id, id)
	objectSpan := &model.Span{
		TraceID:   traceId,
		SpanID:    model.SpanID(1),
		StartTime: time.Time{}.Add(time.Duration(startTime)),
		Duration:  time.Duration(endTime - startTime),
		Tags: append(
			mapToTags(zconstants.KeyToSpanTags(key)),
			model.String(zconstants.TraceSource, zconstants.TraceSourceObject),
			model.String(zconstants.PseudoType, string(zconstants.PseudoTypeObject)),
		),
	}
	spans := []*model.Span{objectSpan}

	for i, link := range links {
		tags := zconstants.KeyToSpanTags(key)
		zconstants.TagLinkedObject(tags, zconstants.LinkRef{
			Key:   link.Key,
			Role:  link.Role,
			Class: link.Class,
		})

		spans = append(spans, &model.Span{
			TraceID:   traceId,
			SpanID:    model.SpanID(100 + i),
			StartTime: objectSpan.StartTime,
			Duration:  objectSpan.Duration,
			Tags: append(
				mapToTags(tags),
				model.String(zconstants.TraceSource, zconstants.TraceSourceObject),
				model.String(zconstants.PseudoType, string(zconstants.PseudoTypeLink)),
			),
		})
	}

	tree := tftree.NewSpanTree(spans)

	return merge.TraceWithMetadata[uint64]{
		Tree:     tree,
		Metadata: id,
	}
}

func mapToTags(m map[string]string) (out []model.KeyValue) {
	for key, value := range m {
		out = append(out, model.String(key, value))
	}

	return out
}

// Assume traces[0] is the only result returned by the initial list.
func do(
	t *testing.T,
	traces []merge.TraceWithMetadata[uint64],
	clipTimeStart, clipTimeEnd int64,
) {
	assert := assert.New(t)

	merger := merge.Merger[uint64]{}
	assert.NoError(merger.AddTraces(traces))

	assert.NoError(merger.FollowLinks(
		context.Background(),
		tfconfig.ConstantLinkSelector(true),
		time.Time{}.Add(time.Duration(clipTimeStart)),
		time.Time{}.Add(time.Duration(clipTimeEnd)),
		func(ctx context.Context, key utilobject.Key, startTime, endTime time.Time, limit int) (out []merge.TraceWithMetadata[uint64], _ error) {
			for _, trace := range traces {
				traceKey, hasKey := zconstants.ObjectKeyFromSpan(trace.Tree.Root)
				assert.True(hasKey)
				if key == traceKey {
					out = append(out, trace)
				}
			}

			return out, nil
		},
		len(traces),
		int32(len(traces)),
		false,
	))

	result, err := merger.MergeTraces()
	assert.NoError(err)
	t.Log(result)
}

func TestFullTree(t *testing.T) {
}
