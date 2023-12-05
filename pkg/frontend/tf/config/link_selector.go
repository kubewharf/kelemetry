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
	// The first output indicates whether the selector admits this link.
	// The second output is the selector state that transitive links should use for admission.
	// The second output may still be useful even if the first output is false when it is part of a UnionLinkSelector.
	// A nil state "fuses" the selector, i.e. always return false for further transitive links.
	Admit(parent utilobject.Key, child utilobject.Key, parentIsSource bool, linkClass string) (_admit bool, _nextState LinkSelector)
}

func nilableLinkSelectorAdmit(
	selector LinkSelector,
	parent utilobject.Key,
	child utilobject.Key,
	parentIsSource bool,
	linkClass string,
) (bool, LinkSelector) {
	if selector == nil {
		return false, nil
	}

	return selector.Admit(parent, child, parentIsSource, linkClass)
}

type ConstantLinkSelector bool

func (selector ConstantLinkSelector) Admit(
	parent utilobject.Key,
	child utilobject.Key,
	parentIsSource bool,
	linkClass string,
) (_admit bool, _nextState LinkSelector) {
	if selector {
		return true, selector
	}

	return false, selector
}

type IntersectLinkSelector []LinkSelector

func (selector IntersectLinkSelector) Admit(
	parentKey utilobject.Key,
	childKey utilobject.Key,
	parentIsSource bool,
	linkClass string,
) (_admit bool, _nextState LinkSelector) {
	admit := true
	nextMemberStates := make([]LinkSelector, len(selector))

	for i, member := range selector {
		memberAdmit, nextMemberState := nilableLinkSelectorAdmit(member, parentKey, childKey, parentIsSource, linkClass)
		admit = admit && memberAdmit
		nextMemberStates[i] = nextMemberState
	}

	return admit, IntersectLinkSelector(nextMemberStates)
}

type UnionLinkSelector []LinkSelector

func (selector UnionLinkSelector) Admit(
	parentKey utilobject.Key,
	childKey utilobject.Key,
	parentIsSource bool,
	linkClass string,
) (_admit bool, _nextState LinkSelector) {
	admit := false
	nextMemberStates := make([]LinkSelector, len(selector))

	for i, member := range selector {
		memberAdmit, nextMemberState := nilableLinkSelectorAdmit(member, parentKey, childKey, parentIsSource, linkClass)
		admit = admit || memberAdmit
		nextMemberStates[i] = nextMemberState
	}

	return admit, UnionLinkSelector(nextMemberStates)
}
