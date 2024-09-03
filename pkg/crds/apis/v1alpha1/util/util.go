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

package kelemetryv1a1util

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/itchyny/gojq"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kelemetryv1a1 "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

func TestObject(objectRef utilobject.Key, object metav1.Object, rule *kelemetryv1a1.LinkRuleSourceFilter) (bool, error) {
	objectGr := metav1.GroupResource{Group: objectRef.Group, Resource: objectRef.Resource}

	if rule.Resources != nil {
		matchedGr := false
		for _, ruleGr := range *rule.Resources {
			if ruleGr == objectGr {
				matchedGr = true
			}
		}

		if !matchedGr {
			return false, nil
		}
	}

	selector, err := metav1.LabelSelectorAsSelector(&rule.Selector)
	if err != nil {
		return false, fmt.Errorf("rule has invalid label selector: %w", err)
	}

	objectLabels := labels.Set(object.GetLabels())

	return selector.Matches(objectLabels), nil
}

func GenerateTargets(
	ctx context.Context,
	templates []kelemetryv1a1.LinkRuleTargetTemplate,
	sourceObjectRef utilobject.Rich,
	context map[string]any,
) (_targets []utilobject.VersionedKey, _ error) {
	targets := make([]utilobject.VersionedKey, 0)

	for _, template := range templates {
		clusters, err := executeTemplate(ctx, "cluster", template.Cluster, context, []string{sourceObjectRef.Cluster})
		if err != nil {
			return _targets, err
		}

		if len(clusters) == 0 {
			clusters = []string{sourceObjectRef.Cluster}
		}

		types, err := executeTemplate(ctx, "type", template.Type, context, nil)
		if err != nil {
			return _targets, err
		}

		namespaces, err := executeTemplate(ctx, "namespace", template.Namespace, context, []string{sourceObjectRef.Namespace})
		if err != nil {
			return _targets, err
		}

		names, err := executeTemplate(ctx, "name", template.Name, context, []string{sourceObjectRef.Name})
		if err != nil {
			return _targets, err
		}

		for _, cluster := range clusters {
			for _, gvrString := range types {
				gvrSplit := strings.Split(gvrString, "/")
				var gvr schema.GroupVersionResource
				switch len(gvrSplit) {
				case 2:
					gvr.Version = gvrSplit[0]
					gvr.Resource = gvrSplit[1]
				case 3:
					gvr.Group = gvrSplit[0]
					gvr.Version = gvrSplit[1]
					gvr.Resource = gvrSplit[2]
				default:
					return nil, fmt.Errorf("invalid g/v/r notation %q", gvrString)
				}

				for _, namespace := range namespaces {
					for _, name := range names {
						targets = append(targets, utilobject.VersionedKey{
							Key: utilobject.Key{
								Cluster:   cluster,
								Group:     gvr.Group,
								Resource:  gvr.Resource,
								Namespace: namespace,
								Name:      name,
							},
							Version: gvr.Version,
						})
					}
				}
			}
		}
	}

	return targets, nil
}

func PrepareContext(sourceObject *unstructured.Unstructured, clusterName string, resource string) map[string]any {
	data := sourceObject.DeepCopy().Object
	metadata := data["metadata"].(map[string]any)
	metadata["clusterName"] = clusterName
	metadata["resource"] = resource
	return data
}

func executeTemplate(
	ctx context.Context,
	templateName string,
	templateObject kelemetryv1a1.TextTemplate,
	context map[string]any,
	defaultOutput []string,
) (output []string, err error) {
	if templateObject.GoTemplate != "" {
		output, err = executeGoTemplate(templateName, templateObject.Jq, context)
	} else if templateObject.Jq != "" {
		output, err = executeJqTemplate(ctx, templateName, templateObject.Jq, context)
	} else if templateObject.Literal != nil {
		output, err = []string{*templateObject.Literal}, nil
	} else {
		output, err = defaultOutput, nil
	}

	if err != nil {
		return nil, err
	}

	return output, nil
}

func executeGoTemplate(templateName string, templateString string, context map[string]any) ([]string, error) {
	tmpl, err := template.New(templateName).Parse(templateString)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s as Go text/template: %w", templateName, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, context); err != nil {
		return nil, fmt.Errorf("cannot execute %s: %w", templateName, err)
	}

	output := make([]string, 0)
	for _, value := range strings.Split(buf.String(), ",") {
		if value != "" {
			output = append(output, value)
		}
	}

	return output, nil
}

func executeJqTemplate(ctx context.Context, templateName string, templateString string, context map[string]any) ([]string, error) {
	query, err := gojq.Parse(templateString)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s as JQ query: %w", templateName, err)
	}

	iter := query.RunWithContext(ctx, context)

	output := make([]string, 0)

	for {
		value, ok := iter.Next()
		if !ok {
			break
		}

		switch value := value.(type) {
		case error:
			return nil, fmt.Errorf("error executing JQ query for %s: %w", templateName, value)
		case string:
			output = append(output, value)
		case []any:
			for _, item := range value {
				if stringItem, isString := item.(string); isString {
					output = append(output, stringItem)
				} else {
					return nil, fmt.Errorf("%s query must return string or []string, got []%T", templateName, item)
				}
			}
		default:
			return nil, fmt.Errorf("%s query must return string or []string, got %T", templateName, value)
		}
	}

	return output, nil
}
