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

	"github.com/jaegertracing/jaeger/model"
	"github.com/spf13/pflag"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("jaeger-transform", newTransformer)
}

type TransformerOptions struct{}

func (options *TransformerOptions) Setup(fs *pflag.FlagSet) {
}

func (options *TransformerOptions) EnableFlag() *bool { return nil }

type Transformer struct {
	options TransformerOptions
	configs tfconfig.Provider
}

func newTransformer(configs tfconfig.Provider) *Transformer {
	return &Transformer{
		configs: configs,
	}
}

func (transformer *Transformer) Options() manager.Options        { return &transformer.options }
func (transformer *Transformer) Init(ctx context.Context) error  { return nil }
func (transformer *Transformer) Start(ctx context.Context) error { return nil }
func (transformer *Transformer) Close(ctx context.Context) error { return nil }

func (transformer *Transformer) Transform(trace *model.Trace, rootSpan model.SpanID, configId tfconfig.Id) {
	config := transformer.configs.GetById(configId)
	if config == nil {
		config = transformer.configs.GetById(transformer.configs.DefaultId())
	}

	tree := tftree.NewSpanTree(trace)

	if config.UseSubtree {
		tree.SetRoot(rootSpan)
	}

	for _, step := range config.Steps {
		tree.Visit(step.Visitor)
	}

	trace.Spans = tree.GetSpans()
}
