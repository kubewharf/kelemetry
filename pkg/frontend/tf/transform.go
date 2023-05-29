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

package transform

import (
	"context"
	"errors"
	"fmt"

	"github.com/jaegertracing/jaeger/model"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("jaeger-transform", manager.Ptr(&Transformer{}))
}

type TransformerOptions struct{}

func (options *TransformerOptions) Setup(fs *pflag.FlagSet) {
}

func (options *TransformerOptions) EnableFlag() *bool { return nil }

type Transformer struct {
	options TransformerOptions
	Logger  logrus.FieldLogger
	Configs tfconfig.Provider
}

func (transformer *Transformer) Options() manager.Options        { return &transformer.options }
func (transformer *Transformer) Init() error                     { return nil }
func (transformer *Transformer) Start(ctx context.Context) error { return nil }
func (transformer *Transformer) Close(ctx context.Context) error { return nil }

func (transformer *Transformer) Transform(trace *model.Trace, rootObject *tftree.GroupingKey, configId tfconfig.Id) error {
	if len(trace.Spans) == 0 {
		return fmt.Errorf("cannot transform empty trace")
	}

	config := transformer.Configs.GetById(configId)
	if config == nil {
		config = transformer.Configs.GetById(transformer.Configs.DefaultId())
	}

	tree := tftree.NewSpanTree(trace)

	transformer.groupDuplicates(tree)

	if config.UseSubtree && rootObject != nil {
		var rootSpan model.SpanID
		hasRootSpan := false

		for _, span := range tree.GetSpans() {
			if key, hasKey := tftree.GroupingKeyFromSpan(span); hasKey && key == *rootObject {
				rootSpan = span.SpanID
				hasRootSpan = true
			}
		}

		if hasRootSpan {
			if err := tree.SetRoot(rootSpan); err != nil {
				if errors.Is(err, tftree.ErrRootDoesNotExist) {
					return fmt.Errorf(
						"trace data does not contain desired root span %v as indicated by the exclusive flag (%w)",
						rootSpan,
						err,
					)
				}

				return fmt.Errorf("cannot set root: %w", err)
			}
		}
	}

	for _, step := range config.Steps {
		step.Run(tree)
	}

	trace.Spans = tree.GetSpans()

	return nil
}

// merge spans of the same object from multiple traces
func (transformer *Transformer) groupDuplicates(tree *tftree.SpanTree) {
	commonSpans := map[tftree.GroupingKey][]model.SpanID{}

	for _, span := range tree.GetSpans() {
		if key, hasKey := tftree.GroupingKeyFromSpan(span); hasKey {
			commonSpans[key] = append(commonSpans[key], span.SpanID)
		}
	}

	for _, spans := range commonSpans {
		if len(spans) > 1 {
			// only retain the first span

			desiredParent := spans[0]

			originalTags := tree.Span(desiredParent).Tags
			originalTagKeys := make(sets.Set[string], len(originalTags))
			for _, tag := range originalTags {
				originalTagKeys.Insert(tag.Key)
			}

			for _, obsoleteParent := range spans[1:] {
				for _, tag := range tree.Span(obsoleteParent).Tags {
					if !originalTagKeys.Has(tag.Key) {
						originalTags = append(originalTags, tag)
						originalTagKeys.Insert(tag.Key)
					}
				}

				children := tree.Children(obsoleteParent)
				for child := range children {
					tree.Move(child, desiredParent)
				}

				if tree.Root.SpanID == obsoleteParent {
					tree.Root = tree.Span(desiredParent)
				}

				tree.Delete(obsoleteParent)
			}
		}
	}
}
