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
	"fmt"
	"text/template"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

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

func GenerateTarget(
	rule *kelemetryv1a1.LinkRuleTargetTemplate,
	sourceObjectRef utilobject.Rich,
	sourceObject *unstructured.Unstructured,
) (_target utilobject.VersionedKey, _ error) {
	cluster := sourceObjectRef.Cluster
	if rule.ClusterTemplate != "" {
		var err error
		cluster, err = executeTemplate("clusterTemplate", rule.ClusterTemplate, sourceObject)
		if err != nil {
			return _target, err
		}
	}

	namespace, err := executeTemplate("namespaceTemplate", rule.NamespaceTemplate, sourceObject)
	if err != nil {
		return _target, err
	}

	name, err := executeTemplate("nameTemplate", rule.NameTemplate, sourceObject)
	if err != nil {
		return _target, err
	}

	return utilobject.VersionedKey{
		Key: utilobject.Key{
			Cluster:   cluster,
			Group:     rule.Group,
			Resource:  rule.Resource,
			Namespace: namespace,
			Name:      name,
		},
		Version: rule.Version,
	}, nil
}

func executeTemplate(templateName string, templateString string, sourceObject *unstructured.Unstructured) (string, error) {
	tmpl, err := template.New(templateName).Parse(templateString)
	if err != nil {
		return "", fmt.Errorf("cannot parse %s as Go text/template: %w", templateName, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, sourceObject.Object); err != nil {
		return "", fmt.Errorf("cannot execute %s: %w", templateName, err)
	}

	return buf.String(), nil
}
