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

package merge

import (
	"fmt"
	"math/rand"

	"github.com/jaegertracing/jaeger/model"
	"k8s.io/apimachinery/pkg/util/sets"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type Merger[R RawTrace[M], M any] struct{}

type RawTrace[M any] interface {
	GetSpans() *tftree.SpanTree
	GetMetadata() M
}

type objKey = utilobject.Key

func (merger Merger[R, M]) MergeTraces(rawTraces []R) ([]*MergeTree[M], error) {
	objects, err := merger.groupByKey(rawTraces)
	if err != nil {
		return nil, err
	}

	abLinks := abLinkMap{}

	for _, obj := range objects {
		obj.identifyLinks()

		for _, link := range obj.links {
			abLink := abLinkFromTargetLink(obj.key, link)
			abLinks.insert(abLink)
		}
	}

	var mergeTrees []*MergeTree[M]
	for _, keys := range merger.findConnectedComponents(objects, abLinks) {
		var members []*object[R, M]
		for _, key := range keys {
			members = append(members, objects[key])
		}

		mergeTree, err := newMergeTree(members, abLinks)
		if err != nil {
			return nil, err
		}

		mergeTrees = append(mergeTrees, mergeTree)
	}

	return mergeTrees, nil
}

func (merger Merger[R, M]) groupByKey(rawTraces []R) (map[objKey]*object[R, M], error) {
	objects := map[objKey]*object[R, M]{}

	for _, trace := range rawTraces {
		key, _ := zconstants.ObjectKeyFromSpan(trace.GetSpans().Root)

		if obj, hasPrev := objects[key]; hasPrev {
			if err := obj.merge(trace); err != nil {
				return nil, err
			}
		} else {
			obj, err := newObject[R, M](key, trace)
			if err != nil {
				return nil, err
			}

			objects[key] = obj
		}
	}

	return objects, nil
}

type object[R RawTrace[M], M any] struct {
	key      objKey
	metadata []M
	tree     *tftree.SpanTree

	links []targetLink
}

func newObject[R RawTrace[M], M any](key objKey, trace R) (*object[R, M], error) {
	clonedTree, err := trace.GetSpans().Clone()
	if err != nil {
		return nil, fmt.Errorf("clone spans: %w", err)
	}
	obj := &object[R, M]{
		key:      key,
		metadata: []M{trace.GetMetadata()},
		tree:     clonedTree,
	}
	return obj, nil
}

func (obj *object[R, M]) merge(trace R) error {
	obj.metadata = append(obj.metadata, trace.GetMetadata())

	mergeRoot(obj.tree.Root, trace.GetSpans().Root)

	copyVisitor := &copyTreeVisitor{to: obj.tree}
	trace.GetSpans().Visit(copyVisitor)
	if copyVisitor.err != nil {
		return copyVisitor.err
	}

	return nil
}

func mergeRoot(base *model.Span, tail *model.Span) {
	mergeRootInterval(base, tail)
	mergeRootTags(base, tail)
}

func mergeRootInterval(base *model.Span, tail *model.Span) {
	startTime := base.StartTime
	if tail.StartTime.Before(startTime) {
		startTime = tail.StartTime
	}

	endTime := base.StartTime.Add(base.Duration)
	tailEndTime := tail.StartTime.Add(tail.Duration)
	if tailEndTime.After(endTime) {
		endTime = tailEndTime
	}

	base.StartTime = startTime
	base.Duration = endTime.Sub(startTime)
}

func mergeRootTags(base *model.Span, tail *model.Span) {
	tagPos := map[string]int{}
	for pos, tag := range base.Tags {
		tagPos[tag.Key] = pos
	}

	for _, tag := range tail.Tags {
		if pos, hasTag := tagPos[tag.Key]; hasTag {
			if tail.StartTime.After(base.StartTime) {
				// the newer value wins
				base.Tags[pos] = tag
			}
		} else {
			base.Tags = append(base.Tags, tag)
		}
	}
}

func (obj *object[R, M]) identifyLinks() {
	for spanId := range obj.tree.Children(obj.tree.Root.SpanID) {
		span := obj.tree.Span(spanId)
		pseudoType, isPseudo := model.KeyValues(span.Tags).FindByKey(zconstants.PseudoType)
		if !(isPseudo && pseudoType.VStr == string(zconstants.PseudoTypeLink)) {
			continue
		}

		target, hasTarget := zconstants.LinkedKeyFromSpan(span)
		if !hasTarget {
			continue
		}

		linkRoleTag, hasLinkRole := model.KeyValues(span.Tags).FindByKey(zconstants.LinkRole)
		if !hasLinkRole {
			continue
		}
		linkRole := linkRoleTag.VStr

		linkClassTag, hasLinkClass := model.KeyValues(span.Tags).FindByKey(zconstants.LinkClass)
		linkClass := ""
		if hasLinkClass {
			linkClass = linkClassTag.VStr
		}

		obj.links = append(obj.links, targetLink{
			key:   target,
			role:  zconstants.LinkRoleValue(linkRole),
			class: linkClass,
		})
	}
}

type targetLink struct {
	key   objKey
	role  zconstants.LinkRoleValue
	class string
}

type copyTreeVisitor struct {
	to  *tftree.SpanTree
	err error
}

func (visitor *copyTreeVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	if span.SpanID != tree.Root.SpanID {
		spanCopy, err := tftree.CopySpan(span)
		if err != nil {
			visitor.err = err
			return nil
		}

		visitor.to.Add(spanCopy, span.ParentSpanID())
	}

	return visitor
}

func (visitor *copyTreeVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {}

type abLink struct {
	alpha, beta objKey

	alphaIsParent bool // this needs to be changed if there are link roles other than parent and child
	class         string
}

func (link abLink) isParent(key objKey) bool {
	if link.alphaIsParent {
		return link.alpha == key
	} else {
		return link.beta == key
	}
}

func abLinkFromTargetLink(subject objKey, link targetLink) abLink {
	if groupingKeyLess(subject, link.key) {
		return abLink{
			alpha:         subject,
			beta:          link.key,
			alphaIsParent: link.role == zconstants.LinkRoleChild,
			class:         link.class,
		}
	} else {
		return abLink{
			beta:          subject,
			alpha:         link.key,
			alphaIsParent: link.role != zconstants.LinkRoleChild,
			class:         link.class,
		}
	}
}

func groupingKeyLess(left, right objKey) bool {
	if left.Group != right.Group {
		return left.Group < right.Group
	}

	if left.Resource != right.Resource {
		return left.Resource < right.Resource
	}

	if left.Cluster != right.Cluster {
		return left.Cluster < right.Cluster
	}

	if left.Namespace != right.Namespace {
		return left.Namespace < right.Namespace
	}

	if left.Name != right.Name {
		return left.Name < right.Name
	}

	return false
}

type abLinkMap map[objKey]map[objKey]abLink

func (m abLinkMap) insert(link abLink) {
	m.insertDirected(link.alpha, link.beta, link)
	m.insertDirected(link.beta, link.alpha, link)
}

func (m abLinkMap) insertDirected(k1, k2 objKey, link abLink) {
	v1, hasK1 := m[k1]
	if !hasK1 {
		v1 = map[objKey]abLink{}
	}
	v1[k2] = link
}

func (m abLinkMap) detectRoot(seed objKey) (_root objKey, _hasCycle bool) {
	visited := sets.New[objKey]()
	return m.dfsRoot(visited, seed)
}

func (m abLinkMap) dfsRoot(visited sets.Set[objKey], key objKey) (_root objKey, _hasCycle bool) {
	if visited.Has(key) {
		return key, true
	}
	visited.Insert(key) // avoid infinite recursion

	for peer, link := range m[key] {
		if link.isParent(peer) {
			return m.dfsRoot(visited, peer)
		}
	}

	return key, false // key has no parent, so key is root
}

type componentTaint = int

type connectedComponent = []objKey

func (Merger[R, M]) findConnectedComponents(objects map[objKey]*object[R, M], abLinks abLinkMap) []connectedComponent {
	objectKeys := make(sets.Set[objKey], len(objects))
	for gk := range objects {
		objectKeys.Insert(gk)
	}

	var taintCounter componentTaint

	taints := map[objKey]componentTaint{}

	for {
		seed, hasMore := peekArbitraryFromSet(objectKeys)
		if !hasMore {
			break
		}

		taintCounter += 1
		dfsTaint(objectKeys, abLinks, taints, taintCounter, seed)
	}

	components := make([]connectedComponent, taintCounter)
	for key, taint := range taints {
		components[taint] = append(components[taint], key)
	}

	return components
}

func peekArbitraryFromSet[T comparable](set sets.Set[T]) (T, bool) {
	for value := range set {
		return value, true
	}

	return reflectutil.ZeroOf[T](), false
}

func dfsTaint(
	keys sets.Set[objKey],
	abLinks abLinkMap,
	taints map[objKey]componentTaint,
	taintId componentTaint,
	seed objKey,
) {
	taints[seed] = taintId
	delete(keys, seed) // delete before diving in to avoid recursing backwards

	for peer := range abLinks[seed] {
		if _, remaining := keys[peer]; !remaining {
			continue // this should be unreachable
		}

		dfsTaint(keys, abLinks, taints, taintId, peer)
	}
}

type MergeTree[M any] struct {
	Metadata []M

	Tree *tftree.SpanTree
}

func newMergeTree[R RawTrace[M], M any](
	members []*object[R, M],
	abLinks abLinkMap,
) (*MergeTree[M], error) {
	metadata := []M{}

	for _, member := range members {
		metadata = append(metadata, member.metadata...)
	}

	merged, err := mergeLinkedTraces(members, abLinks)
	if err != nil {
		return nil, err
	}

	return &MergeTree[M]{
		Metadata: metadata,
		Tree:     merged,
	}, nil
}

func mergeLinkedTraces[R RawTrace[M], M any](objects []*object[R, M], abLinks abLinkMap) (*tftree.SpanTree, error) {
	trees := make(map[objKey]*object[R, M], len(objects))
	for _, obj := range objects {
		trees[obj.key] = obj
	}

	rootKey, _ := abLinks.detectRoot(objects[0].key)

	tree := trees[rootKey].tree

	pendingObjects := []objKey{rootKey}
	for len(pendingObjects) > 0 {
		subj := pendingObjects[len(pendingObjects)-1]
		pendingObjects = pendingObjects[:len(pendingObjects)-1]

		for _, link := range trees[subj].links {
			if link.role != zconstants.LinkRoleChild {
				continue
			}

			parentSpan := trees[subj].tree.Root
			if link.class != "" {
				virtualSpan := createVirtualSpan(tree.Root.TraceID, parentSpan, link.class)
				tree.Add(virtualSpan, parentSpan.SpanID)
				parentSpan = virtualSpan
			}

			tree.AddTree(trees[link.key].tree, parentSpan.SpanID)
			pendingObjects = append(pendingObjects, link.key)
		}
	}

	return tree, nil
}

func createVirtualSpan(traceId model.TraceID, span *model.Span, class string) *model.Span {
	spanId := model.SpanID(rand.Uint64())

	return &model.Span{
		TraceID:       traceId,
		SpanID:        spanId,
		OperationName: class,
		Flags:         0,
		StartTime:     span.StartTime,
		Duration:      span.Duration,
		Tags: []model.KeyValue{
			{
				Key:   zconstants.PseudoType,
				VType: model.StringType,
				VStr:  string(zconstants.PseudoTypeLinkClass),
			},
		},
		Process: &model.Process{
			ServiceName: class,
		},
		ProcessID: "1",
	}
}
