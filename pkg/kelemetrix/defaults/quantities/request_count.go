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

package defaultquantities

import (
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-quantity-request-count", manager.Ptr(&kelemetrix.BaseQuantityComp[RequestCount]{}))
}

type RequestCount struct{}

func (RequestCount) Name() string                { return "request_count" }
func (RequestCount) Type() kelemetrix.MetricType { return kelemetrix.MetricTypeCount }
func (RequestCount) DefaultEnable() bool         { return true }

func (RequestCount) Quantify(message *audit.Message) (int64, bool, error) {
	if message.Stage != auditv1.StageResponseComplete {
		return 0, false, nil
	}
	return 1, true, nil
}
