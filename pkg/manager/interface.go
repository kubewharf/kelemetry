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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

var Global = New()

// Component is an object that takes in arguments and has a start/stop lifecycle.
type Component interface {
	// Returns the options required for this component.
	Options() Options
	// Validates options. Initializes the component with a context. Registers inter-component connections.
	Init(ctx context.Context) error
	// Starts the component with a stop channel for shutdown.
	Start(ctx context.Context) error
	// Stops the component. This method should not return until all required workers have joined.
	Close(ctx context.Context) error
}

type BaseComponent struct{}

func (*BaseComponent) Options() Options                { return &NoOptions{} }
func (*BaseComponent) Init(ctx context.Context) error  { return nil }
func (*BaseComponent) Start(ctx context.Context) error { return nil }
func (*BaseComponent) Close(ctx context.Context) error { return nil }

// Options is an object that holds the flags.
type Options interface {
	// Setup registers required flags into this Options object.
	Setup(fs *pflag.FlagSet)
	// EnableFlag returns the option that indicates whether the component should be enabled.
	// If EnableFlag is non-nil, the component is disabled when *EnableFlag() is false.
	// Otherwise, the component is disabled when all dependents are disabled.
	EnableFlag() *bool
}

type NoOptions struct{}

func (*NoOptions) Setup(fs *pflag.FlagSet) {}
func (*NoOptions) EnableFlag() *bool       { return nil }

type UtilContext struct {
	ComponentName string
	ComponentType reflect.Type
}

type componentInfo struct {
	name         string
	component    Component
	order        int
	dependents   uint
	dependencies map[reflect.Type]*componentInfo
}

type utilFactory = func(UtilContext) (any, error)

type componentFactory struct {
	name     string
	build    func(*Manager) (*componentInfo, error)
	muxImpls []reflect.Type
}

type Manager struct {
	shutdownTimeout time.Duration

	utils map[reflect.Type]utilFactory

	componentFactories map[reflect.Type]*componentFactory
	preBuildTasks      []func()

	components map[reflect.Type]*componentInfo

	orderedComponents []*componentInfo
}

func New() *Manager {
	return &Manager{
		utils:              map[reflect.Type]utilFactory{},
		componentFactories: map[reflect.Type]*componentFactory{},
	}
}

// ProvideUtil provides a parameter factory that gets called for every requesting component.
// The factory should be in the form `func(reflect.Type) (T, error)`,
// where the input is the type of the requesting component and T is the type of root.
func (manager *Manager) ProvideUtil(factory any) {
	manager.utils[reflect.TypeOf(factory).Out(0)] = func(ctx UtilContext) (any, error) {
		out := reflect.ValueOf(factory).Call([]reflect.Value{reflect.ValueOf(ctx)})
		if len(out) >= 2 && !out[1].IsNil() {
			return nil, out[1].Interface().(error)
		}

		return out[0].Interface(), nil
	}
}

// Provide provides a Component factory that gets called up to once.
// The factory can accept any number of parameters with types that were provided as roots or components,
// and return either `T` or `(T, error)`, where `T` is the type of component.
// The parameter type must be identical to the return type explicitly declared in the factory (not subtypes/supertypes).
func (manager *Manager) Provide(name string, factory any) {
	manager.provideComponent(name, factory)
}

func (manager *Manager) provideComponent(name string, factory any) reflect.Type {
	factoryType := reflect.TypeOf(factory)
	compTy := factoryType.Out(0)

	cf := &componentFactory{
		name:     name,
		muxImpls: []reflect.Type{},
	}

	cf.build = func(manager *Manager) (*componentInfo, error) {
		ctx := UtilContext{
			ComponentName: name,
			ComponentType: compTy,
		}

		deps := map[reflect.Type]*componentInfo{}

		args := make([]reflect.Value, factoryType.NumIn())
		for i := 0; i < len(args); i++ {
			arg, err := manager.resolve(factoryType.In(i), &ctx, deps)
			if err != nil {
				return nil, err
			}

			args[i] = reflect.ValueOf(arg)
		}

		var impls []MuxImpl
		for _, dep := range cf.muxImpls {
			impl, err := manager.resolve(dep, &ctx, deps)
			if err != nil {
				return nil, err
			}
			impls = append(impls, impl.(MuxImpl))
		}

		out := reflect.ValueOf(factory).Call(args)

		if len(out) >= 2 && !out[1].IsNil() {
			return nil, out[1].Interface().(error)
		}

		comp := out[0].Interface().(Component)

		if mux, isMux := comp.(MuxInterface); isMux {
			mux := mux.IsMux()

			for _, impl := range impls {
				mux.WithImpl(impl)
			}
		}

		return &componentInfo{
			name:         name,
			component:    comp,
			order:        len(manager.components),
			dependents:   0,
			dependencies: deps,
		}, nil
	}

	manager.componentFactories[compTy] = cf

	return compTy
}

// ProvideMuxImpl provides a MuxImpl factory.
// It is similar to Manager.Provide(), but with an additional parameter interfaceFunc,
// where the argument is in the form `Interface.AnyFunc`.
func (manager *Manager) ProvideMuxImpl(name string, factory any, interfaceFunc any) {
	compTy := manager.provideComponent(name, factory)
	itfTy := reflect.TypeOf(interfaceFunc).In(0)

	manager.preBuildTasks = append(manager.preBuildTasks, func() {
		factory := manager.componentFactories[itfTy]
		factory.muxImpls = append(factory.muxImpls, compTy)
	})
}

func (manager *Manager) resolve(
	req reflect.Type,
	ctx *UtilContext,
	deps map[reflect.Type]*componentInfo,
) (any, error) {
	if factory, exists := manager.utils[req]; exists {
		return factory(*ctx)
	}

	if factory, exists := manager.componentFactories[req]; exists {
		delete(manager.componentFactories, req)

		comp, err := factory.build(manager)
		if err != nil {
			return nil, err
		}

		manager.components[req] = comp
	}

	if info, exists := manager.components[req]; exists {
		if ctx != nil {
			// ctx describes the dependent, nil if this is not requested from a dependent
			info.dependents += 1
		}
		deps[req] = info
		return info.component, nil
	}

	return nil, fmt.Errorf("unknown dependency type %s", req)
}

// Build constructs all components.
func (manager *Manager) Build() error {
	for _, task := range manager.preBuildTasks {
		task()
	}

	manager.components = make(map[reflect.Type]*componentInfo, len(manager.componentFactories))

	for len(manager.componentFactories) > 0 {
		var ty reflect.Type
		for key := range manager.componentFactories {
			ty = key
			break
		}

		_, err := manager.resolve(ty, nil, make(map[reflect.Type]*componentInfo, 1))
		if err != nil {
			return err
		}
	}

	return nil
}

type dotNode struct {
	name  string
	ty    reflect.Type
	isMux bool
}

func (manager *Manager) Dot() string {
	roots := []dotNode{}
	nonRoots := []dotNode{}
	for ty, info := range manager.components {
		list := &roots
		for _, otherInfo := range manager.components {
			if _, exists := otherInfo.dependencies[ty]; exists {
				list = &nonRoots
			}
		}
		_, isMux := info.component.(MuxInterface)
		*list = append(*list, dotNode{
			name:  info.name,
			ty:    ty,
			isMux: isMux,
		})
	}

	for _, list := range [][]dotNode{roots, nonRoots} {
		sort.Slice(list, func(i, j int) bool {
			return list[i].name < list[j].name
		})
	}

	cleanName := func(name string) string {
		return strings.ReplaceAll(strings.ReplaceAll(name, "/", "\\n"), "-", "\\n")
	}

	out := "digraph G {\n"
	nodeIds := map[reflect.Type]int{}

	id := 0
	for sliceNumber, slice := range [][]dotNode{roots, nonRoots} {
		if sliceNumber == 0 {
			out += "\t{\n\trank=source;\n"
		}
		for _, node := range slice {
			style, fillColor := "", "white"
			if node.isMux {
				style, fillColor = "filled", "#2288ff"
			}
			out += fmt.Sprintf(
				"\t\tn%d [label=\"%s\", style=\"%s\", fillcolor=\"%s\"]\n",
				id,
				cleanName(node.name),
				style,
				fillColor,
			)
			nodeIds[node.ty] = id
			id += 1
		}
		if sliceNumber == 0 {
			out += "\t}\n"
		}
	}

	edges := [][2]int{}

	for ty, info := range manager.components {
		for dep := range info.dependencies {
			edges = append(edges, [2]int{nodeIds[ty], nodeIds[dep]})
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})

	for _, edge := range edges {
		out += fmt.Sprintf("\tn%d -> n%d\n", edge[0], edge[1])
	}

	out += "}\n"
	return out
}

func (manager *Manager) SetupFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&manager.shutdownTimeout, "shutdown-timeout", time.Second*15, "timeout for graceful shutdown after receiving SIGTERM")

	for _, comp := range manager.components {
		comp.component.Options().Setup(fs)
	}
}

func (manager *Manager) TrimDisabled(logger logrus.FieldLogger) {
	disabled := map[reflect.Type]*componentInfo{}

	for {
		hasChange := false
		for ty, comp := range manager.components {
			enableFlag := comp.component.Options().EnableFlag()

			var shouldDisable bool
			var silentDisable bool
			if muxImpl, isMuxImpl := comp.component.(MuxImpl); isMuxImpl {
				parent := muxImpl.GetMuxImplBase().parent

				var parentComp *componentInfo
				for _, otherComp := range manager.components {
					otherMux, isMux := otherComp.component.(MuxInterface)
					if isMux && otherMux.IsMux() == parent {
						parentComp = otherComp
						break
					}
				}

				shouldDisable = true
				// if parentComp == nil, already disabled and trimmed
				if parentComp != nil && muxImpl.GetMuxImplBase().isChosenMuxImpl() {
					if parent.EnableFlag() != nil {
						shouldDisable = !*parent.EnableFlag()
					} else {
						shouldDisable = parentComp.dependents == 0
					}
				}

				silentDisable = true
			} else {
				if enableFlag != nil {
					shouldDisable = !*enableFlag
				} else {
					shouldDisable = comp.dependents == 0
				}
			}

			if shouldDisable {
				for _, depComp := range comp.dependencies {
					depComp.dependents -= 1
				}

				hasChange = true
				delete(manager.components, ty)
				disabled[ty] = comp

				if !silentDisable {
					logger.WithField("mod", comp.name).Warn("Component disabled")
				}
			}
		}

		if !hasChange {
			break
		}
	}

	// validate incorrect disabling combinations
	for disabledTy, disabledComp := range disabled {
		if _, ok := disabledComp.component.(MuxImpl); ok {
			continue
		}

		if disabledComp.dependents > 0 {
			var dependents []string
			for _, comp := range manager.components {
				if _, exists := comp.dependencies[disabledTy]; exists {
					dependents = append(dependents, comp.name)
					break
				}
			}

			logger.Fatalf(
				"Cannot disable %q because %v depend on it but are not disabled",
				disabledComp.name,
				dependents,
			)
			return
		}
	}

	manager.orderedComponents = make([]*componentInfo, 0, len(manager.components))
	for _, comp := range manager.components {
		manager.orderedComponents = append(manager.orderedComponents, comp)
	}

	sort.Slice(manager.orderedComponents, func(i, j int) bool {
		return manager.orderedComponents[i].order < manager.orderedComponents[j].order
	})
}

func (manager *Manager) Init(ctx context.Context, logger logrus.FieldLogger) error {
	for _, comp := range manager.orderedComponents {
		logger.WithField("mod", comp.name).Info("Initializing")

		if err := comp.component.Init(ctx); err != nil {
			return fmt.Errorf("error initializing %q: %w", comp.name, err)
		}
	}

	return nil
}

func (manager *Manager) Start(logger logrus.FieldLogger, ctx context.Context) error {
	for _, comp := range manager.orderedComponents {
		logger.WithField("mod", comp.name).Info("Starting")

		if err := comp.component.Start(ctx); err != nil {
			return fmt.Errorf("error starting %q: %w", comp.name, err)
		}
	}

	return nil
}

func (manager *Manager) Close(ctx context.Context, logger logrus.FieldLogger) error {
	ctx, cancelFunc := context.WithTimeout(ctx, manager.shutdownTimeout)
	defer cancelFunc()

	for offset := len(manager.orderedComponents) - 1; offset >= 0; offset-- {
		comp := manager.orderedComponents[offset]
		modLogger := logger.WithField("mod", comp.name)
		modLogger.Info("Closing")

		// continue closing even if some components panicked during close
		okCh := make(chan struct{})
		go func(okCh chan<- struct{}) {
			defer close(okCh)
			defer shutdown.RecoverPanic(modLogger)

			if err := comp.component.Close(ctx); err != nil {
				modLogger.WithError(err).Error("Error closing")
			}
		}(okCh)
		<-okCh
	}

	logger.Info("Shutdown complete")

	return nil
}
