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
	"context"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

type BaseQuantityDef interface {
	Name() string
	Type() MetricType
	DefaultEnable() bool

	Quantify(message *audit.Message) (int64, bool, error)
}

type BaseQuantityOptions[T BaseQuantityDef] struct {
	Enable  bool
	TagSets map[string]string
}

func (options *BaseQuantityOptions[T]) Setup(fs *pflag.FlagSet) {
	var t T

	optionName := strings.ReplaceAll(t.Name(), "_", "-")

	fs.BoolVar(
		&options.Enable,
		fmt.Sprintf("kelemetrix-quantity-%s-enable", optionName),
		t.DefaultEnable(),
		fmt.Sprintf("enable the %q metric quantity", t.Name()),
	)
	fs.StringToStringVar(
		&options.TagSets,
		fmt.Sprintf("kelemetrix-quantity-%s-tag-sets", optionName),
		map[string]string{"full": "*", "gr": "group*resource"},
		fmt.Sprintf(
			"sets of tags to report for %s to mitigate cardinality explosion; "+
				"each value is either \"*\" or a list of tags separated by \"*\"",
			t.Name(),
		),
	)
}

func (options *BaseQuantityOptions[T]) EnableFlag() *bool { return &options.Enable }

type BaseQuantityComp[T BaseQuantityDef] struct {
	options  BaseQuantityOptions[T]
	Logger   logrus.FieldLogger
	Registry *Registry
}

func (comp *BaseQuantityComp[T]) Options() manager.Options { return &comp.options }

func (comp *BaseQuantityComp[T]) Init() error {
	comp.Registry.AddQuantifier(&baseQuantifierImpl[T]{comp: comp})
	return nil
}

func (comp *BaseQuantityComp[T]) Start(ctx context.Context) error { return nil }
func (comp *BaseQuantityComp[T]) Close(ctx context.Context) error { return nil }

type baseQuantifierImpl[T BaseQuantityDef] struct {
	t    T
	comp *BaseQuantityComp[T]
}

func (q *baseQuantifierImpl[T]) Name() string     { return q.t.Name() }
func (q *baseQuantifierImpl[T]) Type() MetricType { return q.t.Type() }
func (q *baseQuantifierImpl[T]) TagSets() map[string]func(string) bool {
	m := make(map[string]func(string) bool, len(q.comp.options.TagSets))

	for name, set := range q.comp.options.TagSets {
		m[name] = ParseTagSet(set)
	}

	return m
}

func (q *baseQuantifierImpl[T]) Quantify(message *audit.Message) (int64, bool) {
	if value, hasValue, err := q.t.Quantify(message); err != nil {
		q.comp.Logger.WithError(err).Error("error generating quantity")
		return 0, false
	} else {
		return value, hasValue
	}
}
