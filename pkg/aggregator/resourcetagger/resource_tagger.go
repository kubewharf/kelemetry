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

package resourcetagger

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/jsonpath"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func init() {
	manager.Global.Provide("resource-tagger", manager.Ptr(&ResourceTagger{}))
}

var defaultTagMapping = []string{"pods#nodes:spec.nodeName"}

type options struct {
	resourceTagMappings []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(
		&options.resourceTagMappings,
		"resource-tag-mapping",
		defaultTagMapping,
		"tag a resource (pods, nodes, etc.) span from jsonpath, comma separated. "+
			"supported setting format: resource[.group]#tagkey1:jsonpath1;tagkey2:jsonpath2, "+
			"the tag value is parsed from jsonpath. e.g. 'pods#nodes:spec.nodeName;yourTag:status.hostIP,nodes#custom-tag:metadata.name'")
}

func (options *options) EnableFlag() *bool {
	return nil
}

type ResourceTagger struct {
	options            options
	Clock              clock.Clock
	Logger             logrus.FieldLogger
	ObjectCache        *objectcache.ObjectCache
	resourceTagMapping map[schema.GroupResource]map[string]string

	TagEventMetric *metrics.Metric[*tagEventMetric]
}

type tagEventMetric struct {
	TraceSource string
	Result      string
}

func (*tagEventMetric) MetricName() string { return "aggregator_resource_tagger" }

var _ manager.Component = &ResourceTagger{}

func (d *ResourceTagger) Options() manager.Options {
	return &d.options
}

func (d *ResourceTagger) Init() error {
	if err := d.registerTagMapping(); err != nil {
		return err
	}
	return nil
}

func (d *ResourceTagger) Start(ctx context.Context) error { return nil }

func (d *ResourceTagger) Close(ctx context.Context) error { return nil }

func (d *ResourceTagger) registerTagMapping() error {
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

func (d *ResourceTagger) registerResource(gr schema.GroupResource, tagPathMapping map[string]string) {
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

func (d *ResourceTagger) DecorateTag(ctx context.Context, object util.ObjectRef, traceSource string, tags map[string]any) {
	if tags == nil {
		return
	}

	gr := object.GroupResource()
	if _, exists := d.resourceTagMapping[gr]; !exists {
		return
	}

	logger := d.Logger.WithFields(object.AsFields("object")).WithField("traceSource", traceSource)
	tagMetric := &tagEventMetric{TraceSource: traceSource, Result: "Unknown"}
	defer d.TagEventMetric.DeferCount(time.Now(), tagMetric)

	raw := object.Raw

	if raw == nil {
		logger.Debug("Fetching dynamic object for tag decorator")

		var err error
		raw, err = d.ObjectCache.Get(ctx, object)
		if err != nil {
			tagMetric.Result = "FetchErr"
			logger.WithError(err).Error("cannot fetch object value")
			return
		}

		if raw == nil {
			tagMetric.Result = "NotFound"
			logger.Debug("object no longer exists")
			return
		}
	}

	for tagKey, jsonPath := range d.resourceTagMapping[gr] {
		val, err := getValFromJsonPath(raw, jsonPath)
		if err != nil {
			continue
		}
		tagMetric.Result = "Success"
		tags[tagKey] = val
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
