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

package tfmodifier

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/pflag"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	"github.com/kubewharf/kelemetry/pkg/manager"
	utilmarshal "github.com/kubewharf/kelemetry/pkg/util/marshal"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

func init() {
	manager.Global.ProvideListImpl(
		"tf-modifier/link-selector",
		manager.Ptr(&LinkSelectorModifierFactory{}),
		&manager.List[tfconfig.ModifierFactory]{},
	)
}

type LinkSelectorModifierOptions struct {
	enable bool
}

func (options *LinkSelectorModifierOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "jaeger-tf-link-selector-modifier-enable", true, "enable link selector modifiers and list it in frontend")
}

func (options *LinkSelectorModifierOptions) EnableFlag() *bool { return &options.enable }

type LinkSelectorModifierFactory struct {
	options LinkSelectorModifierOptions
}

var _ manager.Component = &LinkSelectorModifierFactory{}

func (m *LinkSelectorModifierFactory) Options() manager.Options        { return &m.options }
func (m *LinkSelectorModifierFactory) Init() error                     { return nil }
func (m *LinkSelectorModifierFactory) Start(ctx context.Context) error { return nil }
func (m *LinkSelectorModifierFactory) Close(ctx context.Context) error { return nil }

func (*LinkSelectorModifierFactory) ListIndex() string { return "link-selector" }

func (*LinkSelectorModifierFactory) Build(jsonBuf []byte) (tfconfig.Modifier, error) {
	modifier := &LinkSelectorModifier{}

	if err := json.Unmarshal(jsonBuf, &modifier); err != nil {
		return nil, fmt.Errorf("parse link selector modifier config: %w", err)
	}

	return modifier, nil
}

type LinkSelectorModifier struct {
	Class            string                       `json:"modifierClass"`
	IncludeSiblings  bool                         `json:"includeSiblings"`
	PatternFilters   []LinkPattern                `json:"ifAll"`
	UpwardDistance   utilmarshal.Optional[uint32] `json:"upwardDistance"`
	DownwardDistance utilmarshal.Optional[uint32] `json:"downwardDistance"`
}

type LinkPattern struct {
	Parent            utilmarshal.ObjectFilter                       `json:"parent"`
	Child             utilmarshal.ObjectFilter                       `json:"child"`
	IncludeFromParent utilmarshal.Optional[bool]                     `json:"fromParent"`
	IncludeFromChild  utilmarshal.Optional[bool]                     `json:"fromChild"`
	LinkClass         utilmarshal.Optional[utilmarshal.StringFilter] `json:"linkClass"`
}

func (pattern *LinkPattern) Matches(parent utilobject.Key, child utilobject.Key, isFromParent bool, linkClass string) bool {
	if !pattern.Parent.Matches(parent) {
		return false
	}

	if !pattern.Child.Matches(child) {
		return false
	}

	if !pattern.IncludeFromParent.GetOr(true) && isFromParent {
		return false
	}

	if !pattern.IncludeFromChild.GetOr(true) && !isFromParent {
		return false
	}

	if pattern.LinkClass.IsSet && !pattern.LinkClass.Value.Matches(linkClass) {
		return false
	}

	return true
}

func (modifier *LinkSelectorModifier) ModifierClass() string {
	return fmt.Sprintf("kelemetry.kubewharf.io/link-selectors/%s", modifier.Class)
}

func (modifier *LinkSelectorModifier) Modify(config *tfconfig.Config) {
	intersectSelector := tfconfig.IntersectLinkSelector{
		patternLinkSelector{patterns: modifier.PatternFilters},
	}
	if !modifier.IncludeSiblings {
		intersectSelector = append(intersectSelector, denySiblingsLinkSelector{})
	}
	if modifier.UpwardDistance.IsSet {
		intersectSelector = append(
			intersectSelector,
			directedDistanceLinkSelector{distance: modifier.UpwardDistance.Value, direction: directionUpwards},
		)
	}
	if modifier.DownwardDistance.IsSet {
		intersectSelector = append(
			intersectSelector,
			directedDistanceLinkSelector{distance: modifier.DownwardDistance.Value, direction: directionDownwards},
		)
	}

	config.LinkSelector = tfconfig.UnionLinkSelector{config.LinkSelector, intersectSelector}
}

// Ensures that the path from the queried object to any other object in the tree is monotonic.
type denySiblingsLinkSelector struct {
	hasFirst          bool
	firstIsFromParent bool
}

func (s denySiblingsLinkSelector) Admit(
	parent utilobject.Key,
	child utilobject.Key,
	isFromParent bool,
	linkClass string,
) tfconfig.LinkSelector {
	if !s.hasFirst {
		return denySiblingsLinkSelector{hasFirst: true, firstIsFromParent: isFromParent}
	}
	if s.firstIsFromParent != isFromParent {
		return nil
	}
	return s
}

// The path from queried object to any other object in the tree must only contain links matching this pattern.
type patternLinkSelector struct {
	patterns []LinkPattern
}

func (s patternLinkSelector) Admit(parent utilobject.Key, child utilobject.Key, isFromParent bool, linkClass string) tfconfig.LinkSelector {
	for _, pattern := range s.patterns {
		if !pattern.Matches(parent, child, isFromParent, linkClass) {
			return nil
		}
	}

	return s
}

type direction bool

const (
	directionUpwards   direction = true
	directionDownwards direction = false
)

// The path from queried object to any other object in the tree must not exceed `distance` steps in the `direction` direction.
type directedDistanceLinkSelector struct {
	direction direction
	distance  uint32
}

func (d directedDistanceLinkSelector) Admit(
	parent utilobject.Key,
	child utilobject.Key,
	isFromParent bool,
	linkClass string,
) tfconfig.LinkSelector {
	if isFromParent != (d.direction == directionDownwards) {
		return d
	}
	if d.distance == 0 {
		return nil
	}
	return directedDistanceLinkSelector{
		direction: d.direction,
		distance:  d.distance - 1,
	}
}
