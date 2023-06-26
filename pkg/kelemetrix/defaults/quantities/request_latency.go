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
	"fmt"
	"net/url"
	"strconv"

	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

	"github.com/kubewharf/kelemetry/pkg/audit"
	k8sconfig "github.com/kubewharf/kelemetry/pkg/k8s/config"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.ProvideListImpl(
		"kelemetrix-quantity-request-latency",
		manager.Ptr(&kelemetrix.BaseQuantifier[RequestLatency]{}),
		&manager.List[kelemetrix.Quantifier]{},
	)
	manager.Global.ProvideListImpl(
		"kelemetrix-quantity-request-latency-ratio",
		manager.Ptr(&kelemetrix.BaseQuantifier[RequestLatencyRatio]{}),
		&manager.List[kelemetrix.Quantifier]{},
	)
}

type RequestLatency struct{}

func (RequestLatency) Name() string                { return "request_latency" }
func (RequestLatency) Type() kelemetrix.MetricType { return kelemetrix.MetricTypeHistogram }
func (RequestLatency) DefaultEnable() bool         { return true }

func (RequestLatency) Quantify(message *audit.Message) (float64, bool, error) {
	if message.Stage != auditv1.StageResponseComplete {
		return 0, false, nil
	}
	return float64(message.StageTimestamp.Time.Sub(message.RequestReceivedTimestamp.Time).Nanoseconds()), true, nil
}

type RequestLatencyRatio struct {
	Config k8sconfig.Config
}

func (RequestLatencyRatio) Name() string                { return "request_latency_ratio" }
func (RequestLatencyRatio) Type() kelemetrix.MetricType { return kelemetrix.MetricTypeSummary }
func (RequestLatencyRatio) DefaultEnable() bool         { return true }

func (r RequestLatencyRatio) Quantify(message *audit.Message) (float64, bool, error) {
	if message.Stage != auditv1.StageResponseComplete {
		return 0, false, nil
	}

	url, err := url.Parse(message.RequestURI)
	if err != nil {
		return 0, false, fmt.Errorf("cannot parse request URI: %w", err)
	}

	latency := message.StageTimestamp.Time.Sub(message.RequestReceivedTimestamp.Time)
	requestTimeoutStrings := url.Query()["timeoutSeconds"]

	var requestTimeout float64
	if len(requestTimeoutStrings) > 0 {
		requestTimeout, err = strconv.ParseFloat(requestTimeoutStrings[0], 64)
		if err != nil {
			return 0, false, fmt.Errorf("invalid timeoutSeconds value %q", requestTimeoutStrings[0])
		}
	} else {
		cluster := r.Config.Provide(message.Cluster)
		if cluster != nil {
			requestTimeout = cluster.DefaultRequestTimeout.Seconds()
		} else {
			// unknown cluster
			requestTimeout = 60.
		}
	}

	ratio := requestTimeout / latency.Seconds()
	return ratio, true, nil
}
