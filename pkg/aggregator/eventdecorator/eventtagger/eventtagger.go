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

package eventtagger

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/aggregator/eventdecorator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/resourcetagger"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("resource-event-tagger", manager.Ptr(&eventTagDecorator{}))
}

type options struct {
	enable      bool
	filterVerbs []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "resource-event-tag-enable", false, "enable custom event tag for resource")
	fs.StringSliceVar(
		&options.filterVerbs,
		"resource-event-tag-filter-verbs",
		[]string{"create"},
		"add resource tag for audit verbs. e.g 'create,update,patch'")
}

func (options *options) EnableFlag() *bool {
	return &options.enable
}

type eventTagDecorator struct {
	ResourceTagger *resourcetagger.ResourceTagger
	options        options
	EventDecorator eventdecorator.UnionEventDecorator
	filterVerbs    map[string]struct{}
}

func (d *eventTagDecorator) Close() error {
	return nil
}

var _ manager.Component = &eventTagDecorator{}

func (d *eventTagDecorator) Options() manager.Options {
	return &d.options
}

func (d *eventTagDecorator) Init(ctx context.Context) error {
	d.EventDecorator.AddDecorator(d)

	d.filterVerbs = map[string]struct{}{}
	for _, item := range d.options.filterVerbs {
		d.filterVerbs[item] = struct{}{}
	}
	return nil
}

func (d *eventTagDecorator) Start(stopCh <-chan struct{}) error { return nil }

func (d *eventTagDecorator) Decorate(ctx context.Context, object util.ObjectRef, event *aggregatorevent.Event) {
	if event == nil {
		return
	}

	if _, exist := d.filterVerbs[fmt.Sprint(event.Tags["tag"])]; !exist {
		return
	}
	d.ResourceTagger.DecorateTag(ctx, object, event.TraceSource, event.Tags)
}
