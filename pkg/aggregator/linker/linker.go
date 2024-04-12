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

package linker

import (
	"context"

	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type Linker interface {
	LinkerName() string
	Lookup(ctx context.Context, object utilobject.Rich) ([]LinkerResult, error)
}

type LinkerResult struct {
	// The linked object.
	Object utilobject.Rich
	// Determines whether the linked object should be displayed as a parent or child of the object span.
	Role zconstants.LinkRoleValue
	// Classifies links to help with link selection in tfconfig.
	Class string
	// DedupId is a variable in a span cache key that distinguishes the link pseudospan
	// from other pseudospans in the same object in the same time window.
	//
	// This ID shall be consistent such that the link pseudospan does not get recreated many times,
	// and shall be unique such that each link creator maintains its own links.
	DedupId string
}
