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

// LinkRule instructs Kelemetry to display multiple objects (the "source" and the "target") in the same trace
// by looking up the "target" when the span of the "source" object gets created.
//
// LinkRule is bidirectional.
// Once the link is recorded, searching "source" or "target" would both display the other trace in the link
// as long as the link is not filtered out by tfconfig.
type LinkRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// SourceFilter determines whether a source object matches this rule.
	SourceFilter LinkRuleSourceFilter `json:"sourceFilter"`

	// TargetTemplate indicates how to find the target object from a matched source object.
	TargetTemplates []LinkRuleTargetTemplate `json:"targetTemplates"`

	// Link specifies how the two objects are linked together.
	Link LinkRuleLink `json:"link"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type LinkRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LinkRule `json:"items"`
}

// LinkRuleSourceFilter determines whether an object is a child object that matches a rule.
type LinkRuleSourceFilter struct {
	// Resources are the possible resource types that a child object may belong to.
	//
	// +optional
	Resources *[]metav1.GroupResource `json:"resources"`

	// Selector selects matching child objects by label.
	//
	// +optional
	Selector metav1.LabelSelector `json:"selector"`
}

// LinkRuleTargetTemplate indicates how to find the target object from a matched source object.
type LinkRuleTargetTemplate struct {
	// Cluster computes the cluster name of the target object.
	//
	// If empty or unspecified, uses the cluster of the child object.
	// +optional
	Cluster TextTemplate `json:"cluster,omitempty"`

	// The type of the target object, in the form `{groupName}/{version}/{resource}`.
	Type TextTemplate `json:"type"`

	// The namespace of the target object.
	//
	// Cluster-scoped target objects should emit an empty namespace.
	// If the namespace is empty, the target object is expected to be cluster-scoped.
	//
	// Inherits the same namespace as the source object if unspecified.
	// +optional
	Namespace TextTemplate `json:"namespace"`

	// The name of the target object.
	//
	// Inherits the same name as the source object if unspecified.
	// +optional
	Name TextTemplate `json:"name"`
}

// TextTemplate is a union struct that supports templating one or multiple values with one of the supported formats.
//
// Template types that accept a context receive the source object as the template with the following additional fields:
// - .metadata.resource, the plural name of the object
// - .metadata.clusterName, the cluster name used by Kelemetry for the cluster that owns the source object
type TextTemplate struct {
	// A string literal, resolved as-is.
	// +optional
	Literal *string `json:"literal"`
	// Parsed as a Go `text/template`. May output multiple comma-delimited values (trailing comma allowed).
	// +optional
	GoTemplate string `json:"goTemplate"`
	// Parsed as a gojq query string. May output a string or an array of strings.
	// +optional
	Jq string `json:"jq"`
}

// LinkRuleLink specifies how the two objects are linked together.
type LinkRuleLink struct {
	// TargetRole selects the display position of the target object.
	//
	// One of the target objects will be arbitrarily selected if there are multiple links with preferTargetParent=true.
	// +kubebuilder:default=Child
	TargetRole TargetRole `json:"targetRole"`

	// If Class is non empty, a pseudospan named the given value is inserted in the hierarchy between the target and source objects.
	//
	// Multiple links with the same nonempty Class share the same pseudospan.
	// +optional
	Class string `json:"class,omitempty"`
}

// TargetRole selects whether the target object is rendered as the parent or as the child of the source object.
type TargetRole string

const (
	TargetRoleChild  TargetRole = "Child"
	TargetRoleParent TargetRole = "Parent"
)
