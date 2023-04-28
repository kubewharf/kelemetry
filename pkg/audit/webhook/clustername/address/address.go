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

package address

import (
	"context"

	"github.com/kubewharf/kelemetry/pkg/audit/webhook/clustername"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideMuxImpl("cluster-name/address", manager.Ptr(&AddressResolver{}), clustername.Resolver.Resolve)
}

type AddressResolver struct {
	manager.MuxImplBase
}

func (_ *AddressResolver) MuxImplName() (name string, isDefault bool) { return "address", true }

func (resolver *AddressResolver) Options() manager.Options { return &manager.NoOptions{} }

func (resolver *AddressResolver) Init() error { return nil }

func (resolver *AddressResolver) Start(ctx context.Context) error { return nil }

func (resolver *AddressResolver) Close(ctx context.Context) error { return nil }

func (resolver *AddressResolver) Resolve(ip string) string { return ip }
