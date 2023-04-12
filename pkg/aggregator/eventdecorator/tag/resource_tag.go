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

package tag

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/jsonpath"

	"github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	"github.com/kubewharf/kelemetry/pkg/aggregator/eventdecorator"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("resource-tagger", manager.Ptr(&tagDecorator{}))
}

var defaultTagMapping = []string{"pods#nodes:spec.nodeName"}

type options struct {
	enable              bool
	resourceTagMappings []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "resource-custom-tag-enable", false, "enable custom event tag for resource")

	fs.StringSliceVar(
		&options.resourceTagMappings,
		"resource-tag-mapping",
		defaultTagMapping,
		"tag a resource (pods, nodes, etc.) span from jsonpath, comma separated. "+
			"supported setting format: resource[.group]#tagkey1:jsonpath1;tagkey2:jsonpath2, "+
			"the tag value is parsed from jsonpath. e.g. 'pods#nodes:spec.nodeName;yourTag:status.hostIP,nodes#custom-tag:metadata.name'")
}

func (options *options) EnableFlag() *bool {
	return &options.enable
}

type tagDecorator struct {
	options            options
	Logger             logrus.FieldLogger
	ObjectCache        *objectcache.ObjectCache
	EventDecorator     eventdecorator.UnionEventDecorator
	resourceTagMapping map[schema.GroupResource]map[string]string
}

func (d *tagDecorator) Close() error {
	return nil
}

var _ manager.Component = &tagDecorator{}

func (d *tagDecorator) Options() manager.Options {
	return &d.options
}

func (d *tagDecorator) Init(ctx context.Context) error {
	d.EventDecorator.AddDecorator(d)
	if err := d.registerTagMapping(); err != nil {
		return err
	}
	return nil
}

func (d *tagDecorator) Start(stopCh <-chan struct{}) error { return nil }

func (d *tagDecorator) registerTagMapping() error {
	for _, resourceTagMapping := range d.options.resourceTagMappings {
		slices := strings.Split(resourceTagMapping, "#")
		if len(slices) != 2 {
			return fmt.Errorf("invalide resource tag mapping %s", resourceTagMapping)
		}
		resource := slices[0]
		tagMappings := slices[1]

		gv := schema.ParseGroupResource(resource)

		for _, tagMapping := range strings.Split(tagMappings, ";") {
			tagSlice := strings.Split(tagMapping, ":")
			if len(tagSlice) != 2 {
				return fmt.Errorf("invalide tag mapping %s (%s)", resourceTagMapping, tagMapping)
			}
			tagKey := tagSlice[0]
			tagJsonPath := tagSlice[1]
			d.registerResource(gv, map[string]string{
				tagKey: tagJsonPath,
			})
		}
	}

	return nil
}

func (d *tagDecorator) registerResource(gr schema.GroupResource, tagPathMapping map[string]string) {
	if d.resourceTagMapping == nil {
		d.resourceTagMapping = map[schema.GroupResource]map[string]string{}
	}

	if d.resourceTagMapping[gr] == nil {
		d.resourceTagMapping[gr] = map[string]string{}
	}

	for tag, path := range tagPathMapping {
		d.resourceTagMapping[gr][tag] = path
	}
}

func (d *tagDecorator) Decorate(ctx context.Context, object util.ObjectRef, event *aggregatorevent.Event) {
	gr := object.GroupResource()
	if _, exists := d.resourceTagMapping[gr]; !exists {
		return
	}

	raw := object.Raw
	logger := d.Logger.WithField("object", object)

	if raw == nil {
		logger.Debug("Fetching dynamic object")
		var err error
		raw, err = d.ObjectCache.Get(ctx, object)
		if err != nil {
			logger.WithError(err).Error("cannot fetch object value")
			return
		}

		if raw == nil {
			logger.Debug("object no longer exists")
			return
		}
	}

	for tagKey, jsonPath := range d.resourceTagMapping[gr] {
		val, err := getValFromJsonPath(raw, jsonPath)
		if err != nil {
			return
		}
		event.Tags[tagKey] = val
	}
}

func getValFromJsonPath(obj *unstructured.Unstructured, jsonPath string) (string, error) {
	parser := jsonpath.New("json path")
	jsonPath = strings.TrimPrefix(jsonPath, ".")
	err := parser.Parse(fmt.Sprintf("{.%s}", jsonPath))
	if err != nil {
		return "", err
	}
	buf := new(bytes.Buffer)
	err = parser.Execute(buf, obj.Object)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}
