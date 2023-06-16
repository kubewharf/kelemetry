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

package objectspanresourcetagger

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/aggregator/objectspandecorator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/resourcetagger"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.ProvideListImpl("resource-object-tag", manager.Ptr(&ObjectSpanTag{}), &manager.List[objectspandecorator.Decorator]{})
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "resource-object-tag-enable", false, "enable custom object span tag for resource")
}

func (options *options) EnableFlag() *bool {
	return &options.enable
}

type ObjectSpanTag struct {
	ResourceTagger *resourcetagger.ResourceTagger
	options        options
}

var _ manager.Component = &ObjectSpanTag{}

func (d *ObjectSpanTag) Options() manager.Options        { return &d.options }
func (d *ObjectSpanTag) Init() error                     { return nil }
func (d *ObjectSpanTag) Start(ctx context.Context) error { return nil }
func (d *ObjectSpanTag) Close(ctx context.Context) error { return nil }

func (d *ObjectSpanTag) Decorate(ctx context.Context, object util.ObjectRef, traceSource string, tags map[string]string) {
	if tags == nil {
		return
	}

	newTags := map[string]any{}
	d.ResourceTagger.DecorateTag(ctx, object, traceSource, newTags)

	for tagKey, tagValue := range newTags {
		tags[tagKey] = fmt.Sprint(tagValue)
	}
}
