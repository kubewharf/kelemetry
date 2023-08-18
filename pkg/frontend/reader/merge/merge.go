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
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"k8s.io/apimachinery/pkg/util/sets"

	jaegerbackend "github.com/kubewharf/kelemetry/pkg/frontend/backend"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/semaphore"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type objKey = utilobject.Key

type Merger[M any] struct {
	objects map[objKey]*object[M]
}

type TraceWithMetadata[M any] struct {
	Tree     *tftree.SpanTree
	Metadata M
}

type RawTree struct {
	Tree *tftree.SpanTree
}

func (tr RawTree) GetSpans() *tftree.SpanTree { return tr.Tree }
func (tr RawTree) GetMetadata() struct{}      { return struct{}{} }
func (tr RawTree) FromThumbnail(self *RawTree, tt *jaegerbackend.TraceThumbnail) {
	self.Tree = tt.Spans
}

func (merger *Merger[M]) AddTraces(trees []TraceWithMetadata[M]) (_affected sets.Set[objKey], _err error) {
	if merger.objects == nil {
		merger.objects = make(map[objKey]*object[M])
	}

	affected := sets.New[objKey]()
	for _, trace := range trees {
		key := zconstants.ObjectKeyFromSpan(trace.Tree.Root)
		affected.Insert(key)

		if obj, hasPrev := merger.objects[key]; hasPrev {
			if err := obj.merge(trace.Tree, trace.Metadata); err != nil {
				return nil, err
			}
		} else {
			obj, err := newObject[M](key, trace.Tree, trace.Metadata)
			if err != nil {
				return nil, err
			}

			merger.objects[key] = obj
		}
	}

	for key := range affected {
		merger.objects[key].identifyLinks()
	}

	return affected, nil
}

type followLinkPool[M any] struct {
	sem                *semaphore.Semaphore
	knownKeys          sets.Set[objKey]
	lister             ListFunc[M]
	startTime, endTime time.Time
	merger             *Merger[M]
}

func (fl *followLinkPool[M]) scheduleFrom(obj *object[M], followLimit *atomic.Int32, linkSelector tfconfig.LinkSelector) {
	admittedLinks := []TargetLink{}

	for _, link := range obj.links {
		if _, known := fl.knownKeys[link.Key]; known {
			admittedLinks = append(admittedLinks, link)
			continue
		}
		if followLimit.Add(-1) < 0 {
			continue
		}

		parentKey, childKey, parentIsSource := obj.key, link.Key, true
		if link.Role == zconstants.LinkRoleParent {
			parentKey, childKey, parentIsSource = link.Key, obj.key, false
		}

		subSelector := linkSelector.Admit(parentKey, childKey, parentIsSource, link.Class)
		if subSelector != nil {
			admittedLinks = append(admittedLinks, link)
			fl.knownKeys.Insert(link.Key)
			fl.schedule(link.Key, subSelector, followLimit, int32(fl.endTime.Sub(fl.startTime)/(time.Minute*30)))
		}
	}

	obj.links = admittedLinks
}

func (fl *followLinkPool[M]) schedule(key objKey, linkSelector tfconfig.LinkSelector, followLimit *atomic.Int32, traceLimit int32) {
	fl.sem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
		thumbnails, err := fl.lister(ctx, key, fl.startTime, fl.endTime, int(traceLimit))
		if err != nil {
			return nil, fmt.Errorf("fetching linked traces: %w", err)
		}

		return func() error {
			affected, err := fl.merger.AddTraces(thumbnails)
			if err != nil {
				return err
			}

			for key := range affected {
				fl.scheduleFrom(fl.merger.objects[key], followLimit, linkSelector)
			}

			return nil
		}, nil
	})
}

type ListFunc[M any] func(ctx context.Context, key objKey, startTime time.Time, endTime time.Time, limit int) ([]TraceWithMetadata[M], error)

func (merger *Merger[M]) FollowLinks(
	ctx context.Context,
	linkSelector tfconfig.LinkSelector,
	startTime, endTime time.Time,
	lister ListFunc[M],
	concurrency int,
	limit int32,
	limitIsGlobal bool,
) error {
	fl := &followLinkPool[M]{
		sem:       semaphore.New(concurrency),
		knownKeys: sets.New[objKey](),
		lister:    lister,
		startTime: startTime,
		endTime:   endTime,
		merger:    merger,
	}

	for _, obj := range merger.objects {
		fl.knownKeys.Insert(obj.key)
	}

	globalLimit := new(atomic.Int32)
	globalLimit.Store(limit)

	for _, obj := range merger.objects {
		var remainingLimit *atomic.Int32
		if limitIsGlobal {
			remainingLimit = globalLimit
		} else {
			remainingLimit = new(atomic.Int32)
			remainingLimit.Store(limit)
		}

		fl.scheduleFrom(obj, remainingLimit, linkSelector)
	}

	if err := fl.sem.Run(ctx); err != nil {
		return err
	}

	return nil
}

func (merger *Merger[M]) MergeTraces() ([]*MergeTree[M], error) {
	abLinks := abLinkMap{}

	for _, obj := range merger.objects {
		for _, link := range obj.links {
			abLink := abLinkFromTargetLink(obj.key, link)
			abLinks.insert(abLink)
		}
	}

	var mergeTrees []*MergeTree[M]
	for _, keys := range merger.findConnectedComponents(merger.objects, abLinks) {
		var members []*object[M]
		for _, key := range keys {
			members = append(members, merger.objects[key])
		}

		mergeTree, err := newMergeTree(members, abLinks)
		if err != nil {
			return nil, err
		}

		mergeTrees = append(mergeTrees, mergeTree)
	}

	return mergeTrees, nil
}

type object[M any] struct {
	key      objKey
	metadata []M
	tree     *tftree.SpanTree

	links []TargetLink
}

func newObject[M any](key objKey, trace *tftree.SpanTree, metadata M) (*object[M], error) {
	clonedTree, err := trace.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone spans: %w", err)
	}
	obj := &object[M]{
		key:      key,
		metadata: []M{metadata},
		tree:     clonedTree,
	}
	return obj, nil
}

func (obj *object[M]) merge(trace *tftree.SpanTree, metadata M) error {
	obj.metadata = append(obj.metadata, metadata)

	mergeRoot(obj.tree.Root, trace.Root)

	copyVisitor := &copyTreeVisitor{to: obj.tree}
	trace.Visit(copyVisitor)
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

func (obj *object[M]) identifyLinks() {
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

		obj.links = append(obj.links, TargetLink{
			Key:   target,
			Role:  zconstants.LinkRoleValue(linkRole),
			Class: linkClass,
		})
	}
}

type TargetLink struct {
	Key   objKey
	Role  zconstants.LinkRoleValue
	Class string
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

func abLinkFromTargetLink(subject objKey, link TargetLink) abLink {
	if groupingKeyLess(subject, link.Key) {
		return abLink{
			alpha:         subject,
			beta:          link.Key,
			alphaIsParent: link.Role == zconstants.LinkRoleChild,
			class:         link.Class,
		}
	} else {
		return abLink{
			beta:          subject,
			alpha:         link.Key,
			alphaIsParent: link.Role != zconstants.LinkRoleChild,
			class:         link.Class,
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
		m[k1] = v1
	}
	v1[k2] = link
}

func (m abLinkMap) detectRoot(seed objKey, vertexFilter func(objKey) bool) (_root objKey, _hasCycle bool) {
	visited := sets.New[objKey]()
	return m.dfsRoot(visited, seed, vertexFilter)
}

func (m abLinkMap) dfsRoot(visited sets.Set[objKey], key objKey, vertexFilter func(objKey) bool) (_root objKey, _hasCycle bool) {
	if visited.Has(key) {
		return key, true
	}
	visited.Insert(key) // avoid infinite recursion

	for peer, link := range m[key] {
		if !vertexFilter(peer) {
			continue
		}

		if link.isParent(peer) {
			return m.dfsRoot(visited, peer, vertexFilter)
		}
	}

	return key, false // key has no parent, so key is root
}

type componentTaint = int

type connectedComponent = []objKey

func (*Merger[M]) findConnectedComponents(objects map[objKey]*object[M], abLinks abLinkMap) []connectedComponent {
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

		dfsTaint(objectKeys, abLinks, taints, taintCounter, seed)
		taintCounter += 1
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

func newMergeTree[M any](
	members []*object[M],
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

func mergeLinkedTraces[M any](objects []*object[M], abLinks abLinkMap) (*tftree.SpanTree, error) {
	trees := make(map[objKey]*object[M], len(objects))
	for _, obj := range objects {
		trees[obj.key] = obj
	}

	rootKey, _ := abLinks.detectRoot(objects[0].key, func(key objKey) bool {
		_, hasTree := trees[key]
		return hasTree
	})

	tree := trees[rootKey].tree
	treeObjects := sets.New(rootKey)

	pendingObjects := []objKey{rootKey}
	for len(pendingObjects) > 0 {
		subj := pendingObjects[len(pendingObjects)-1]
		pendingObjects = pendingObjects[:len(pendingObjects)-1]

		for _, link := range trees[subj].links {
			if link.Role != zconstants.LinkRoleChild {
				continue
			}

			parentSpan := trees[subj].tree.Root
			if link.Class != "" {
				virtualSpan := createVirtualSpan(tree.Root.TraceID, parentSpan, "", link.Class)
				tree.Add(virtualSpan, parentSpan.SpanID)
				parentSpan = virtualSpan
			}

			if treeObjects.Has(link.Key) {
				parentSpan.Warnings = append(parentSpan.Warnings, fmt.Sprintf("repeated object %v omitted", link.Key))
				// duplicate
				continue
			}

			subtree, hasSubtree := trees[link.Key]
			if !hasSubtree {
				// this link was not fetched, e.g. because of fetch limit or link selector
				continue
			}

			tree.AddTree(subtree.tree, parentSpan.SpanID)
			treeObjects.Insert(link.Key)
			pendingObjects = append(pendingObjects, link.Key)
		}
	}

	return tree, nil
}

func createVirtualSpan(traceId model.TraceID, span *model.Span, opName string, svcName string) *model.Span {
	spanId := model.SpanID(rand.Uint64())

	return &model.Span{
		TraceID:       traceId,
		SpanID:        spanId,
		OperationName: opName,
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
			ServiceName: svcName,
		},
		ProcessID: "1",
	}
}
