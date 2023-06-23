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

package kelemetrix

import (
	"github.com/sirupsen/logrus"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

type BaseQuantityDef interface {
	Name() string
	Type() MetricType
	DefaultEnable() bool

	Quantify(message *audit.Message) (float64, bool, error)
}

type BaseQuantifier[T BaseQuantityDef] struct {
	manager.BaseComponent
	Logger logrus.FieldLogger
	Def    T `managerRecurse:""`
}

func NewBaseQuantifierForTest[T BaseQuantityDef](def T) *BaseQuantifier[T] {
	return &BaseQuantifier[T]{Logger: logrus.New(), Def: def}
}

func (q *BaseQuantifier[T]) Name() string     { return q.Def.Name() }
func (q *BaseQuantifier[T]) Type() MetricType { return q.Def.Type() }
func (q *BaseQuantifier[T]) Quantify(message *audit.Message) (float64, bool) {
	if value, hasValue, err := q.Def.Quantify(message); err != nil {
		q.Logger.WithError(err).Error("error generating quantity")
		return 0, false
	} else {
		return value, hasValue
	}
}
