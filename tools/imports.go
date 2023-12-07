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

// Dummy package for dependencies only used as `go run`
// to prevent go.mod formatting tools from automatically removing them.
//
// This package should not be imported by the binary package.
package kelemetry_tools

import (
	_ "github.com/itchyny/gojq/cli"
	_ "k8s.io/code-generator/cmd/deepcopy-gen/args"
	_ "sigs.k8s.io/controller-tools/pkg/genall"
)
