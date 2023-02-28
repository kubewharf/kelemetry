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

package clustername

import (
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("cluster-name", newResolver)
}

type Resolver interface {
	Resolve(ip string) string
}

type mux struct {
	*manager.Mux
}

func newResolver() Resolver {
	return &mux{
		Mux: manager.NewMux("cluster-name-resolver", false),
	}
}

func (mux *mux) Resolve(ip string) string {
	return mux.Impl().(Resolver).Resolve(ip)
}
