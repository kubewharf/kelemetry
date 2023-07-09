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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:validation:Required
// +kubebuilder:resource:path=linkrules,scope=Cluster
// +kubebuilder:object:root=true

// LinkRule defines a parent-child relation between objects in a Kelemetry trace.
type LinkRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Filter determines whether an object is a child object that matches this rule.
	Filter LinkRuleFilter `json:"filter"`

	// Parent indicates how to find the parent object from a matched child object.
	Parent LinkRuleParent `json:"parent"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type LinkRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LinkRule `json:"items"`
}

func (list *LinkRuleList) GetItems() []LinkRule { return list.Items }

// LinkRuleFilter determines whether an object is a child object that matches a rule.
type LinkRuleFilter struct {
	// Resources are the possible resource types that a child object may belong to.
	Resources []metav1.GroupResource `json:"resources"`

	// Selector selects matching child objects by label.
	Selector metav1.LabelSelector `json:"selector"`
}

// LinkRuleParent indicates how to find the parent object from a matched child object.
type LinkRuleParent struct {
	// ClusterTemplate is a Go text template string to compute the cluster name of the parent object.
	//
	// The context of the template is the child object that matched the rule.
	//
	// If empty or unspecified, uses the cluster of the child object.
	// +optional
	ClusterTemplate string `json:"clusterTemplate,omitempty"`

	// The type of the parent object.
	metav1.GroupVersionResource `json:",inline"`

	// Scope specifies whether the parent object is namespaced or cluster-scoped.
	//
	// If scope=Namespaced, the parent object namespace is inferred from the child object namespace.
	// Behavior is unspecified the child is cluster-scoped but the parent is namespace-scoped.
	Scope ScopeType `json:"scope"`

	// NameTemplate is a Go text template string to compute the parent object name.
	//
	// The context of the template is the child object that matched the rule.
	NameTemplate string `json:"nameTemplate"`
}

// ScopeType specifies whether an object is namespaced or cluster-scoped.
type ScopeType string

const (
	ClusterScope    ScopeType = "Cluster"
	NamespacedScope ScopeType = "Namespaced"
)
