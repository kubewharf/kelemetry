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

package tftree

import (
	"encoding/json"
	"fmt"

	"github.com/jaegertracing/jaeger/model"
)

var ErrRootDoesNotExist = fmt.Errorf("SetRoot can only be used on nodes in the tree")

type SpanTree struct {
	spanMap      map[model.SpanID]*model.Span
	childrenMap  map[model.SpanID]map[model.SpanID]struct{}
	visitorStack map[model.SpanID]*stackEntry
	exited       map[model.SpanID]struct{}
	Root         *model.Span
}

type spanNode struct {
	tree *SpanTree
	node *model.Span
}

type stackEntry struct {
	unprocessedChildren []model.SpanID
}

func NewSpanTree(spans []*model.Span) *SpanTree {
	tree := &SpanTree{
		spanMap:      make(map[model.SpanID]*model.Span, len(spans)),
		childrenMap:  make(map[model.SpanID]map[model.SpanID]struct{}, len(spans)),
		visitorStack: make(map[model.SpanID]*stackEntry),
		exited:       make(map[model.SpanID]struct{}, len(spans)),
	}

	for _, span := range spans {
		tree.spanMap[span.SpanID] = span
		if len(span.References) == 0 {
			tree.Root = span
		} else {
			ref := span.References[0]
			parentId := ref.SpanID
			if _, exists := tree.childrenMap[parentId]; !exists {
				tree.childrenMap[parentId] = map[model.SpanID]struct{}{}
			}
			tree.childrenMap[parentId][span.SpanID] = struct{}{}
		}
	}

	if tree.Root == nil {
		panic("cyclic rootless tree detected")
	}

	return tree
}

func (tree *SpanTree) Clone() (*SpanTree, error) {
	copiedSpans := make([]*model.Span, len(tree.spanMap))
	for _, span := range tree.spanMap {
		spanCopy, err := CopySpan(span)
		if err != nil {
			return nil, err
		}

		copiedSpans = append(copiedSpans, spanCopy)
	}

	return NewSpanTree(copiedSpans), nil
}

func CopySpan(span *model.Span) (*model.Span, error) {
	spanJson, err := json.Marshal(span)
	if err != nil {
		return nil, err
	}

	var spanCopy *model.Span
	if err := json.Unmarshal(spanJson, &spanCopy); err != nil {
		return nil, err
	}

	return spanCopy, nil
}

func (tree *SpanTree) Span(id model.SpanID) *model.Span { return tree.spanMap[id] }
func (tree *SpanTree) Children(id model.SpanID) map[model.SpanID]struct{} {
	return tree.childrenMap[id]
}

func (tree *SpanTree) GetSpans() []*model.Span {
	spans := make([]*model.Span, 0, len(tree.spanMap))
	for _, span := range tree.spanMap {
		spans = append(spans, span)
	}
	return spans
}

// Sets the root to a span under the current root, and prunes other spans not under the new root.
func (tree *SpanTree) SetRoot(id model.SpanID) error {
	if len(tree.visitorStack) != 0 {
		panic("Cannot call SetRoot from a visitor")
	}

	newRoot, exists := tree.spanMap[id]
	if !exists {
		return ErrRootDoesNotExist
	}

	newRoot.References = nil // remove the original parent references

	tree.Root = newRoot

	collector := &spanIdCollector{spanIds: map[model.SpanID]struct{}{}}
	tree.Visit(collector)

	for spanId := range tree.spanMap {
		if _, exists := collector.spanIds[spanId]; !exists {
			delete(tree.spanMap, spanId)
		}
	}

	for spanId := range tree.childrenMap {
		if _, exists := collector.spanIds[spanId]; !exists {
			delete(tree.childrenMap, spanId)
		}
	}

	return nil
}

type spanIdCollector struct {
	spanIds map[model.SpanID]struct{}
}

func (collector *spanIdCollector) Enter(tree *SpanTree, span *model.Span) TreeVisitor {
	collector.spanIds[span.SpanID] = struct{}{}
	return collector
}
func (collector *spanIdCollector) Exit(tree *SpanTree, span *model.Span) {}

func (tree *SpanTree) Visit(visitor TreeVisitor) {
	spanNode{tree: tree, node: tree.Root}.visit(visitor)
}

func (subtree spanNode) visit(visitor TreeVisitor) {
	if _, exists := subtree.tree.spanMap[subtree.node.SpanID]; !exists {
		panic("cannot visit nonexistent node in tree")
	}

	var subvisitor TreeVisitor
	if visitor != nil {
		subvisitor = visitor.Enter(subtree.tree, subtree.node)
	}
	// enter before visitorStack is populated to allow removal

	if _, stillExists := subtree.tree.spanMap[subtree.node.SpanID]; !stillExists {
		// deleted during enter, skip exit
		return
	}

	stack := &stackEntry{}
	subtree.tree.visitorStack[subtree.node.SpanID] = stack

	stack.unprocessedChildren = make([]model.SpanID, 0, len(subtree.tree.childrenMap))
	for childId := range subtree.tree.childrenMap[subtree.node.SpanID] {
		stack.unprocessedChildren = append(stack.unprocessedChildren, childId)
	}

	for len(stack.unprocessedChildren) > 0 {
		children := stack.unprocessedChildren
		stack.unprocessedChildren = nil

		for _, childId := range children {
			child := subtree.tree.spanMap[childId]

			if child.SpanID == subtree.node.SpanID {
				panic(fmt.Sprintf("childrenMap is not a tree (spanId %s duplicated)", child.SpanID))
			}

			spanNode{
				tree: subtree.tree,
				node: child,
			}.visit(subvisitor)
		}
	}

	delete(subtree.tree.visitorStack, subtree.node.SpanID)

	if visitor != nil {
		visitor.Exit(subtree.tree, subtree.node)
	}

	if _, stillExists := subtree.tree.spanMap[subtree.node.SpanID]; !stillExists {
		// deleted during exit
		return
	}

	subtree.tree.exited[subtree.node.SpanID] = struct{}{}

	if len(subtree.tree.visitorStack) == 0 {
		for spanId := range subtree.tree.exited {
			delete(subtree.tree.exited, spanId)
		}
	}
}

// Adds a span as a child of another.
//
// parentId must not be exited yet (and may or may not be entered).
func (tree *SpanTree) Add(newSpan *model.Span, parentId model.SpanID) {
	if _, exited := tree.exited[parentId]; exited {
		panic("cannot add under already-exited span")
	}

	tree.spanMap[newSpan.SpanID] = newSpan
	newSpan.References = []model.SpanRef{{
		TraceID: tree.Root.TraceID,
		SpanID:  parentId,
		RefType: model.ChildOf,
	}}

	if _, exists := tree.childrenMap[parentId]; !exists {
		tree.childrenMap[parentId] = map[model.SpanID]struct{}{}
	}
	tree.childrenMap[parentId][newSpan.SpanID] = struct{}{}

	if stack, hasStack := tree.visitorStack[parentId]; hasStack {
		// parent already entered, not yet exited
		// therefore we have to push to its unprcoessed queue
		stack.unprocessedChildren = append(stack.unprocessedChildren, newSpan.SpanID)
	}
	// else, parent not yet entered, and there are no side effects to clean
}

// Moves a span from one span to another.
//
// movedSpan must not be entered yet.
// newParent must not be exited yet (and may or may not be entered).
func (tree *SpanTree) Move(movedSpanId model.SpanID, newParentId model.SpanID) {
	if _, exited := tree.exited[newParentId]; exited {
		panic("cannot move already-exited span")
	}

	movedSpan := tree.spanMap[movedSpanId]
	if len(movedSpan.References) == 0 {
		panic("cannot move root span")
	}

	ref := &movedSpan.References[0]
	oldParentId := ref.SpanID
	ref.SpanID = newParentId

	if _, exists := tree.childrenMap[newParentId]; !exists {
		tree.childrenMap[newParentId] = map[model.SpanID]struct{}{}
	}
	tree.childrenMap[newParentId][movedSpanId] = struct{}{}

	delete(tree.childrenMap[oldParentId], movedSpanId)

	if stack, hasStack := tree.visitorStack[newParentId]; hasStack {
		// parent already entered, not yet exited
		// therefore we have to push to its unprcoessed queue
		stack.unprocessedChildren = append(stack.unprocessedChildren, movedSpanId)
	}
	// else, parent not yet entered, and there are no side effects to clean
}

// Deletes a span entirely from the tree.
//
// All remaining children also get deleted from the list of spans.
// Can only be called when entering/exiting spanId or entering its parents (i.e. deleting descendents).
// Cannot be used to delete the root span.
// TreeVisitor.Exit will not be called if Delete is called during enter.
func (tree *SpanTree) Delete(spanId model.SpanID) {
	if tree.Root.SpanID == spanId {
		panic("cannot delete the root span")
	}
	if _, exists := tree.spanMap[spanId]; !exists {
		panic("cannot delete a deleted span")
	}
	if _, entered := tree.visitorStack[spanId]; entered {
		panic("cannot delete a parent span")
	}
	if _, exited := tree.exited[spanId]; exited {
		panic("cannot delete an exited span")
	}

	span := tree.spanMap[spanId]
	delete(tree.spanMap, spanId)

	if len(span.References) > 0 {
		parentSpanId := span.References[0].SpanID
		delete(tree.childrenMap[parentSpanId], spanId)
	}

	if childrenMap, hasChildren := tree.childrenMap[spanId]; hasChildren {
		for childId := range childrenMap {
			tree.Delete(childId)
		}

		delete(tree.childrenMap, spanId)
	}
}

// Adds all spans in a tree as a subtree in this span.
func (tree *SpanTree) AddTree(childTree *SpanTree, parentId model.SpanID) {
	if tree == childTree {
		panic("cannot add tree to itself")
	}

	tree.addSubtree(parentId, childTree, childTree.Root.SpanID)
}

func (tree *SpanTree) addSubtree(parentId model.SpanID, otherTree *SpanTree, subroot model.SpanID) {
	tree.Add(otherTree.Span(subroot), parentId)
	for child := range otherTree.Children(subroot) {
		tree.addSubtree(subroot, otherTree, child)
	}
}

type TreeVisitor interface {
	// Called before entering the descendents of the span.
	//
	// This method may call tree.Add, tree.Move and tree.Delete
	// with the ancestors of span, span itself and its descendents.
	Enter(tree *SpanTree, span *model.Span) TreeVisitor
	// Called after exiting the descendents of the span.
	Exit(tree *SpanTree, span *model.Span)
}
