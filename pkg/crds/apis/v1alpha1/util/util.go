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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	kelemetryv1a1 "github.com/kubewharf/kelemetry/pkg/crds/apis/v1alpha1"
	kelemetryv1a1client "github.com/kubewharf/kelemetry/pkg/crds/client/clientset/versioned/typed/apis/v1alpha1"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
	globalinformer "github.com/kubewharf/kelemetry/pkg/util/informer/global"
)

type (
	linkRuleAdapter  = globalinformer.SimpleAdapter[*kelemetryv1a1.LinkRule, *kelemetryv1a1.LinkRuleList, kelemetryv1a1client.LinkRuleInterface]
	LinkRuleInformer = globalinformer.Comp[*kelemetryv1a1.LinkRule, *linkRuleAdapter, *globalinformer.CastStoreWrapper[*kelemetryv1a1.LinkRule]]
)

func init() {
	manager.Global.Provide("listrule-informer", manager.Ptr[*LinkRuleInformer](&LinkRuleInformer{
		Adapter: &linkRuleAdapter{
			Name: "listrules",
			ClientBuilder: func(c *rest.Config) (kelemetryv1a1client.LinkRuleInterface, error) {
				client, err := kelemetryv1a1client.NewForConfig(c)
				if err != nil {
					return nil, fmt.Errorf("create kelemetryv1a1 client from config: %w", err)
				}

				return client.LinkRules(), nil
			},
		},
	}))
}

func TestObject(objectRef util.ObjectRef, object metav1.Object, rule *kelemetryv1a1.LinkRuleFilter) (bool, error) {
	objectGr := metav1.GroupResource{Group: objectRef.Group, Resource: objectRef.Resource}

	matchedGr := false
	for _, ruleGr := range rule.Resources {
		if ruleGr == objectGr {
			matchedGr = true
		}
	}

	if !matchedGr {
		return false, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(&rule.Selector)
	if err != nil {
		return false, fmt.Errorf("rule has invalid label selector: %w", err)
	}

	objectLabels := labels.Set(object.GetLabels())

	return selector.Matches(objectLabels), nil
}

func GenerateParent(
	rule *kelemetryv1a1.LinkRuleParent,
	childObjectRef util.ObjectRef,
	childObject *unstructured.Unstructured,
) (_r util.ObjectRef, _ error) {
	cluster := childObjectRef.Cluster
	if rule.ClusterTemplate != "" {
		var err error
		cluster, err = executeTemplate("clusterTemplate", rule.ClusterTemplate, childObject)
		if err != nil {
			return _r, err
		}
	}

	namespace := ""
	if rule.Scope == kelemetryv1a1.NamespacedScope {
		namespace = childObject.GetNamespace()
	}

	name, err := executeTemplate("nameTemplate", rule.NameTemplate, childObject)
	if err != nil {
		return _r, err
	}

	return util.ObjectRef{
		Cluster: cluster,
		GroupVersionResource: schema.GroupVersionResource{
			Group:    rule.Group,
			Version:  rule.Version,
			Resource: rule.Resource,
		},
		Namespace: namespace,
		Name:      name,
	}, nil
}

func executeTemplate(templateName string, templateString string, childObject *unstructured.Unstructured) (string, error) {
	tmpl, err := template.New(templateName).Parse(templateString)
	if err != nil {
		return "", fmt.Errorf("cannot parse %s as Go text/template: %w", templateName, err)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, childObject.Object); err != nil {
		return "", fmt.Errorf("cannot execute %s: %w", templateName, err)
	}

	return buf.String(), nil
}
