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
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-default-quantities", manager.Ptr(&DefaultQuantifiers{}))
}

type DefaultQuantity struct {
	Name          string
	DefaultEnable bool
	Ty            kelemetrix.MetricType
	Mapper        func(*audit.Message) (int64, bool, error)
}

var DefaultQuantities = []DefaultQuantity{
	{Name: "request", DefaultEnable: true, Ty: kelemetrix.MetricTypeCount, Mapper: func(m *audit.Message) (int64, bool, error) {
		return 1, m.Stage == auditv1.StageResponseComplete, nil
	}},
	{Name: "mq_latency", DefaultEnable: true, Ty: kelemetrix.MetricTypeHistogram, Mapper: func(m *audit.Message) (int64, bool, error) {
		return time.Since(m.StageTimestamp.Time).Nanoseconds(), m.Stage == auditv1.StageResponseComplete, nil
	}},
	{Name: "watch_latency", DefaultEnable: true, Ty: kelemetrix.MetricTypeHistogram, Mapper: func(m *audit.Message) (int64, bool, error) {
		if m.Stage != auditv1.StageResponseComplete || m.Verb != "watch" {
			return 0, false, nil
		}
		return m.StageTimestamp.Time.Sub(m.RequestReceivedTimestamp.Time).Nanoseconds(), m.Stage == auditv1.StageResponseComplete, nil
	}},
}

type Options struct {
	EnableFlags []bool
}

func (options *Options) Setup(fs *pflag.FlagSet) {
	options.EnableFlags = make([]bool, len(DefaultQuantities))
	for i, quantity := range DefaultQuantities {
		fs.BoolVar(
			&options.EnableFlags[i],
			fmt.Sprintf("kelemetrix-quantity-enable-%s", quantity.Name),
			quantity.DefaultEnable,
			fmt.Sprintf("enable the %q metric quantity", quantity.Name),
		)
	}
}

func (options *Options) EnableFlag() *bool {
	ok := false
	for _, b := range options.EnableFlags {
		ok = ok || b
	}
	return &ok
}

type DefaultQuantifiers struct {
	options  Options
	Logger   logrus.FieldLogger
	Registry *kelemetrix.Registry
}

func (dq *DefaultQuantifiers) Options() manager.Options { return &dq.options }

func (dq *DefaultQuantifiers) Init() error {
	for i, quantity := range DefaultQuantities {
		if dq.options.EnableFlags[i] {
			dq.Registry.AddQuantifier(&quantifier{quantity: quantity, logger: dq.Logger.WithField("quantifier", quantity.Name)})
		}
	}

	return nil
}

func (dq *DefaultQuantifiers) Start(ctx context.Context) error { return nil }
func (dq *DefaultQuantifiers) Close(ctx context.Context) error { return nil }

type quantifier struct {
	quantity DefaultQuantity
	logger   logrus.FieldLogger
}

func (q *quantifier) Name() string                { return q.quantity.Name }
func (q *quantifier) Type() kelemetrix.MetricType { return q.quantity.Ty }

func (q *quantifier) Quantify(message *audit.Message) (int64, bool) {
	if value, hasValue, err := q.quantity.Mapper(message); err != nil {
		q.logger.WithError(err).Error("error generating quantity")
		return 0, false
	} else {
		return value, hasValue
	}
}
