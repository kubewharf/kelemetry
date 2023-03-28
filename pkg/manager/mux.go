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

package manager

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/pflag"
)

type MuxImpl interface {
	Component

	GetMuxImplBase() *MuxImplBase
	MuxImplName() (name string, isDefault bool)
}

type MuxImplBase struct {
	parent *Mux
}

func (base *MuxImplBase) GetMuxImplBase() *MuxImplBase {
	return base
}

func (base *MuxImplBase) isChosenMuxImpl() bool {
	return base.parent != nil &&
		base.parent.choices[base.parent.which] != nil &&
		base.parent.choices[base.parent.which].GetMuxImplBase() == base
}

func (base *MuxImplBase) setMuxImplParent(parent *Mux) {
	base.parent = parent
}

func (base *MuxImplBase) GetMux() Component {
	return base.parent
}

func (base *MuxImplBase) GetAdditionalOptions() MuxAdditionalOptions {
	return base.parent.additionalOptions
}

type MuxAdditionalOptions interface {
	Setup(fs *pflag.FlagSet)
}

type Mux struct {
	name              string
	enableFlag        *bool
	choices           map[string]MuxImpl
	defaultChoice     string
	additionalOptions MuxAdditionalOptions
	which             string
	whichValue        MuxImpl
}

func (mux *Mux) IsMux() *Mux {
	return mux
}

type MuxInterface interface {
	IsMux() *Mux
}

func NewMux(name string, explicitEnable bool) *Mux {
	enableFlag := (*bool)(nil)
	if explicitEnable {
		enableFlag = new(bool)
	}

	return &Mux{
		name:       name,
		enableFlag: enableFlag,
		choices:    map[string]MuxImpl{},
	}
}

func NewMockMux(name string, which string, whichValue MuxImpl) *Mux {
	return &Mux{
		name:       name,
		choices:    map[string]MuxImpl{which: whichValue},
		which:      which,
		whichValue: whichValue,
	}
}

func (mux *Mux) WithAdditionalOptions(options MuxAdditionalOptions) *Mux {
	if mux.additionalOptions != nil {
		panic("WithAdditionalOptions can only be called once per object")
	}

	mux.additionalOptions = options
	return mux
}

func (mux *Mux) WithImpl(impl MuxImpl) *Mux {
	name, isDefault := impl.MuxImplName()

	impl.GetMuxImplBase().setMuxImplParent(mux)

	mux.choices[name] = impl
	if isDefault {
		if mux.defaultChoice != "" {
			panic(fmt.Sprintf("multiple default impls for %q: %q and %q", mux.name, mux.defaultChoice, name))
		}
		mux.defaultChoice = name
	}

	return mux
}

func (mux *Mux) Setup(fs *pflag.FlagSet) {
	if mux.defaultChoice == "" {
		panic(fmt.Sprintf("no default choice for %q", mux.name))
	}
	choices := []string{}
	for name := range mux.choices {
		choices = append(choices, name)
	}
	sort.Strings(choices)
	usageMessage := fmt.Sprintf("implementation of %s. Possible values are %q.", mux.name, choices)
	fs.StringVar(&mux.which, mux.name, mux.defaultChoice, usageMessage)

	if mux.additionalOptions != nil {
		mux.additionalOptions.Setup(fs)
	}
}

func (mux *Mux) EnableFlag() *bool { return mux.enableFlag }

func (mux *Mux) Options() Options { return mux }

func (mux *Mux) Impl() MuxImpl {
	if mux.whichValue == nil {
		return mux.choices[mux.which]
	}

	return mux.whichValue
}

func (mux *Mux) Init(ctx context.Context) error {
	var exists bool
	mux.whichValue, exists = mux.choices[mux.which]
	if !exists {
		return fmt.Errorf("invalid --%s: no implementation called %q", mux.name, mux.which)
	}

	return nil
}

func (mux *Mux) Start(ctx context.Context) error {
	return nil
}

func (mux *Mux) Close(ctx context.Context) error {
	return nil
}
