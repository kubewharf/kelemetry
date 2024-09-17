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
	"sort"
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
		SpanID:    model.SpanID(id | (1 << 16)),
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
			TraceID: traceId,
			// #nosec G115 -- i is a small integer.
			SpanID:    model.SpanID(id | (3 << 16) | (uint64(i) << 20)),
			StartTime: objectSpan.StartTime,
			Duration:  objectSpan.Duration,
			Tags: append(
				mapToTags(tags),
				model.String(zconstants.TraceSource, zconstants.TraceSourceObject),
				model.String(zconstants.PseudoType, string(zconstants.PseudoTypeLink)),
			),
			References: []model.SpanRef{model.NewChildOfRef(traceId, objectSpan.SpanID)},
		})
	}

	spans = append(spans, &model.Span{
		TraceID:   traceId,
		SpanID:    model.SpanID(id | (2 << 16)),
		StartTime: time.Time{}.Add(time.Duration(startTime+endTime) / 2),
		Duration:  time.Duration(endTime-startTime) / 4,
		Tags: append(
			mapToTags(zconstants.KeyToSpanTags(key)),
			model.String(zconstants.TraceSource, zconstants.TraceSourceEvent),
			model.String(zconstants.NotPseudo, zconstants.NotPseudo),
		),
		References: []model.SpanRef{model.NewChildOfRef(traceId, objectSpan.SpanID)},
	})

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

type traceList []merge.TraceWithMetadata[uint64]

func (list traceList) append(idLow uint32, key utilobject.Key, links []merge.TargetLink) traceList {
	for time := 0; time < 4; time++ {
		// #nosec G115 -- time is a small integer.
		id := uint64(idLow) | (uint64(time) << 8)
		list = append(list, newTrace(
			id, key,
			int64(time*10), int64((time+1)*10),
			links,
		))
	}
	return list
}

// IDs:
// - rs = 0x10, 0x11
// - dp = 0x20
// - pod = 0x30 | rs | (replica*2)
// - node = 0x40
func sampleTraces(withPods bool, withNode bool) traceList {
	rsKeys := []utilobject.Key{
		{
			Cluster:   "test",
			Group:     "apps",
			Resource:  "replicasets",
			Namespace: "default",
			Name:      "dp-spec1",
		},
		{
			Cluster:   "test",
			Group:     "apps",
			Resource:  "replicasets",
			Namespace: "default",
			Name:      "dp-spec2",
		},
	}
	dpKey := utilobject.Key{
		Cluster:   "test",
		Group:     "apps",
		Resource:  "deployments",
		Namespace: "default",
		Name:      "dp",
	}
	podKeys := [][]utilobject.Key{
		{
			{
				Cluster:   "test",
				Group:     "",
				Resource:  "pods",
				Namespace: "default",
				Name:      "dp-spec1-replica1",
			},
			{
				Cluster:   "test",
				Group:     "",
				Resource:  "pods",
				Namespace: "default",
				Name:      "dp-spec1-replica2",
			},
		},
		{
			{
				Cluster:   "test",
				Group:     "",
				Resource:  "pods",
				Namespace: "default",
				Name:      "dp-spec2-replica1",
			},
			{
				Cluster:   "test",
				Group:     "",
				Resource:  "pods",
				Namespace: "default",
				Name:      "dp-spec2-replica2",
			},
		},
	}
	nodeKey := utilobject.Key{
		Cluster:   "test",
		Group:     "",
		Resource:  "nodes",
		Namespace: "",
		Name:      "node",
	}

	list := traceList{}
	for spec := uint32(0); spec < 2; spec++ {
		rsLinks := []merge.TargetLink{
			{Key: dpKey, Role: zconstants.LinkRoleParent, Class: "children"},
		}
		if withPods {
			rsLinks = append(rsLinks,
				merge.TargetLink{Key: podKeys[spec][0], Role: zconstants.LinkRoleChild, Class: "children"},
				merge.TargetLink{Key: podKeys[spec][1], Role: zconstants.LinkRoleChild, Class: "children"},
			)
		}
		list = list.append(0x10|spec, rsKeys[spec], rsLinks)
	}
	list = list.append(0x20, dpKey, []merge.TargetLink{
		{Key: rsKeys[0], Role: zconstants.LinkRoleChild, Class: "children"},
		{Key: rsKeys[1], Role: zconstants.LinkRoleChild, Class: "children"},
	})

	nodeLinks := []merge.TargetLink{}
	if withPods {
		for spec := uint32(0); spec < 2; spec++ {
			for replica := uint32(0); replica < 2; replica++ {
				podLinks := []merge.TargetLink{
					{Key: rsKeys[spec], Role: zconstants.LinkRoleParent, Class: "children"},
				}
				if withNode {
					podLinks = append(podLinks, merge.TargetLink{Key: nodeKey, Role: zconstants.LinkRoleChild, Class: "node"})
				}
				list = list.append(0x30|spec|(replica<<1), podKeys[spec][replica], podLinks)
				nodeLinks = append(nodeLinks, merge.TargetLink{Key: podKeys[spec][replica], Role: zconstants.LinkRoleParent, Class: "node"})
			}
		}
	}
	if withNode {
		list = list.append(0x40, nodeKey, nodeLinks)
	}
	return list
}

func do(
	t *testing.T,
	clipTimeStart, clipTimeEnd int64,
	traces traceList,
	activePrefixLength int,
	linkSelector tfconfig.LinkSelector,
	expectGroupSizes []int,
	expectObjectCounts []int,
) {
	t.Helper()

	assert := assert.New(t)

	active := []merge.TraceWithMetadata[uint64]{}
	for _, trace := range traces[:activePrefixLength] {
		traceTime := int64(trace.Tree.Root.StartTime.Sub(time.Time{}))
		if clipTimeStart <= traceTime && traceTime < clipTimeEnd {
			active = append(active, trace)
		}
	}

	merger := merge.Merger[uint64]{}
	_, err := merger.AddTraces(active)
	assert.NoError(err)

	assert.NoError(merger.FollowLinks(
		context.Background(),
		linkSelector,
		time.Time{}.Add(time.Duration(clipTimeStart)),
		time.Time{}.Add(time.Duration(clipTimeEnd)),
		func(
			ctx context.Context,
			key utilobject.Key,
			startTime, endTime time.Time,
			limit int,
		) (out []merge.TraceWithMetadata[uint64], _ error) {
			for _, trace := range traces {
				traceKey := zconstants.ObjectKeyFromSpan(trace.Tree.Root)
				traceTime := int64(trace.Tree.Root.StartTime.Sub(time.Time{}))
				if key == traceKey && clipTimeStart <= traceTime && traceTime < clipTimeEnd {
					out = append(out, trace)
				}
			}

			return out, nil
		},
		len(traces),
		// #nosec G115 -- test cases have reasonably small sizes.
		int32(len(traces)),
		false,
	))

	result, err := merger.MergeTraces()
	assert.NoError(err)
	assert.Len(result, len(expectGroupSizes))

	sort.Ints(expectGroupSizes)
	actualGroupSizes := make([]int, len(result))
	for i, group := range result {
		actualGroupSizes[i] = len(group.Metadata)
	}
	sort.Ints(actualGroupSizes)
	assert.Equal(expectGroupSizes, actualGroupSizes)

	actualObjectCounts := []int{}
	for _, group := range result {
		objectCount := 0
		for _, span := range group.Tree.GetSpans() {
			pseudoTag, isPseudo := model.KeyValues(span.Tags).FindByKey(zconstants.PseudoType)
			if isPseudo && pseudoTag.VStr == string(zconstants.PseudoTypeObject) {
				objectCount += 1
			}
		}

		actualObjectCounts = append(actualObjectCounts, objectCount)
	}
	sort.Ints(actualObjectCounts)
	assert.Equal(expectObjectCounts, actualObjectCounts)
}

func TestFullTree(t *testing.T) {
	do(t, 10, 30, sampleTraces(true, true), 4, tfconfig.ConstantLinkSelector(true), []int{2 * (1 + 2 + 4 + 1)}, []int{1 + 2 + 4 + 1})
}

func TestFilteredTree(t *testing.T) {
	do(t, 10, 30, sampleTraces(true, true), 4, rsPodLinksOnly{}, []int{2 * (1 + 2)}, []int{1 + 2})
}

type rsPodLinksOnly struct{}

func (rsPodLinksOnly) Admit(parent, child utilobject.Key, parentIsSource bool, class string) tfconfig.LinkSelector {
	if parent.Resource == "replicasets" && child.Resource == "pods" {
		return rsPodLinksOnly{}
	} else {
		return nil
	}
}
