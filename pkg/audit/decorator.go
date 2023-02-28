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

package audit

import (
	"github.com/kubewharf/kelemetry/pkg/aggregator"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("audit-decorator-list", NewDecoratorList)
}

type Decorator interface {
	Decorate(message *Message, event *aggregator.Event)
}

type DecoratorList interface {
	manager.Component

	AddDecorator(decorator Decorator)

	Decorate(message *Message, event *aggregator.Event)
}

type decoratorList struct {
	manager.BaseComponent
	decorators []Decorator
}

func NewDecoratorList() DecoratorList {
	return &decoratorList{
		decorators: []Decorator{},
	}
}

func (list *decoratorList) AddDecorator(decorator Decorator) {
	list.decorators = append(list.decorators, decorator)
}

func (list *decoratorList) Decorate(message *Message, event *aggregator.Event) {
	for _, decorator := range list.decorators {
		decorator.Decorate(message, event)
	}
}
