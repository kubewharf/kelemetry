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

package tfconfig

import utilobject "github.com/kubewharf/kelemetry/pkg/util/object"

type LinkSelector interface {
	// Whether to follow the given link.
	//
	// If link should be followed, return a non-nil LinkSelector.
	// The returned object will be used to recursively follow links in the linked object.
	Admit(parent utilobject.Key, child utilobject.Key, parentIsSource bool, linkClass string) LinkSelector
}

type ConstantLinkSelector bool

func (selector ConstantLinkSelector) Admit(
	parent utilobject.Key,
	child utilobject.Key,
	parentIsSource bool,
	linkClass string,
) LinkSelector {
	if selector {
		return selector
	}

	return nil
}

type IntersectLinkSelector []LinkSelector

func (selector IntersectLinkSelector) Admit(
	parentKey utilobject.Key,
	childKey utilobject.Key,
	parentIsSource bool,
	linkClass string,
) LinkSelector {
	newChildren := make([]LinkSelector, len(selector))

	for i, child := range selector {
		newChildren[i] = child.Admit(parentKey, childKey, parentIsSource, linkClass)
		if newChildren[i] == nil {
			return nil
		}
	}

	return IntersectLinkSelector(newChildren)
}

type UnionLinkSelector []LinkSelector

func (selector UnionLinkSelector) Admit(
	parentKey utilobject.Key,
	childKey utilobject.Key,
	parentIsSource bool,
	linkClass string,
) LinkSelector {
	newChildren := make([]LinkSelector, len(selector))

	ok := false
	for i, child := range selector {
		if child != nil {
			newChildren[i] = child.Admit(parentKey, childKey, parentIsSource, linkClass)
			if newChildren[i] != nil {
				ok = true
			}
		}
	}

	if ok {
		return UnionLinkSelector(newChildren)
	}

	return nil
}
