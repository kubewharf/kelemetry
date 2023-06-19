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

	reflectutil "github.com/kubewharf/kelemetry/pkg/util/reflect"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

var Global = New()

// Component is an object that takes in arguments and has a start/stop lifecycle.
type Component interface {
	// Returns the options required for this component.
	Options() Options
	// Validates options. Initializes the component with a context. Registers inter-component connections.
	Init() error
	// Starts the component with a stop channel for shutdown.
	Start(ctx context.Context) error
	// Stops the component. This method should not return until all required workers have joined.
	Close(ctx context.Context) error
}

type BaseComponent struct{}

func (*BaseComponent) Options() Options                { return &NoOptions{} }
func (*BaseComponent) Init() error                     { return nil }
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

	// Adds a closure that is called before calling Init() on the referencing component
	AddOnInit func(func() error)
}

type componentInfo struct {
	name         string
	ty           reflect.Type
	component    Component
	dependents   uint
	dependencies map[reflect.Type]*componentInfo
}

type utilFactory struct {
	constructor func([]reflect.Value) (any, error)
	reqs        []reflect.Type
}

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
	onInitHooks        map[reflect.Type][]func() error
	listImpls          map[reflect.Type][]reflect.Type
	lateDependentsOf   map[reflect.Type][]reflect.Type
	lateDependenciesOf map[reflect.Type][]reflect.Type

	components map[reflect.Type]*componentInfo

	orderedComponents []*componentInfo
}

func New() *Manager {
	return &Manager{
		utils:              map[reflect.Type]utilFactory{},
		componentFactories: map[reflect.Type]*componentFactory{},
		onInitHooks:        map[reflect.Type][]func() error{},
		listImpls:          map[reflect.Type][]reflect.Type{},
		lateDependentsOf:   map[reflect.Type][]reflect.Type{},
		lateDependenciesOf: map[reflect.Type][]reflect.Type{},
	}
}

// ProvideUtil provides a parameter factory that gets called for every requesting component.
// The factory should be in the form `func(reflect.Type) (T, error)`,
// where the input is the type of the requesting component and T is the type of root.
func (manager *Manager) ProvideUtil(factory any) {
	factoryTy := reflect.TypeOf(factory)

	reqs := make([]reflect.Type, factoryTy.NumIn())
	for i := range reqs {
		reqs[i] = factoryTy.In(i)
	}
	manager.utils[factoryTy.Out(0)] = utilFactory{
		constructor: func(args []reflect.Value) (any, error) {
			out := reflect.ValueOf(factory).Call(args)
			if len(out) >= 2 && !out[1].IsNil() {
				return nil, out[1].Interface().(error)
			}

			return out[0].Interface(), nil
		},
		reqs: reqs,
	}
}

// Provide provides a Component factory that gets called up to once.
// The factory can accept any number of parameters with types that were provided as roots or components,
// and return either `T` or `(T, error)`, where `T` is the type of component.
// The parameter type must be identical to the return type explicitly declared in the factory (not subtypes/supertypes).
func (manager *Manager) Provide(name string, factory ComponentFactory) {
	manager.provideComponent(name, factory)
}

type ComponentFactory interface {
	ComponentType() reflect.Type
	Params() []reflect.Type
	Call([]reflect.Value) []reflect.Value
}

func Func(closure any) ComponentFactory {
	return &closureComponentFactory{closure: closure}
}

type closureComponentFactory struct {
	closure any
}

func (factory *closureComponentFactory) ComponentType() reflect.Type {
	return reflect.TypeOf(factory.closure).Out(0)
}

func (factory *closureComponentFactory) Params() []reflect.Type {
	factoryTy := reflect.TypeOf(factory.closure)
	params := make([]reflect.Type, factoryTy.NumIn())
	for i := range params {
		params[i] = factoryTy.In(i)
	}
	return params
}

func (factory *closureComponentFactory) Call(values []reflect.Value) []reflect.Value {
	return reflect.ValueOf(factory.closure).Call(values)
}

func Ptr[CompTy any](obj CompTy) ComponentFactory {
	compTy := reflectutil.TypeOf[CompTy]()

	objTy := reflect.TypeOf(obj)
	if !(objTy.Kind() == reflect.Ptr && objTy.Elem().Kind() == reflect.Struct) {
		panic("manager.Ptr() only accepts pointer-to-struct")
	}

	params := []reflect.Type{}
	valueHandlers := []func(reflect.Value){}

	var populate func(structTy reflect.Type, structValue reflect.Value)
	populate = func(structTy reflect.Type, structValue reflect.Value) {
		for i := 0; i < structTy.NumField(); i++ {
			i := i

			field := structTy.Field(i)
			if field.IsExported() && !field.Anonymous && structValue.Field(i).IsZero() {
				if _, recurse := field.Tag.Lookup("managerRecurse"); recurse {
					nestedType := field.Type
					nestedValue := structValue.Field(i)

					if nestedType.Kind() == reflect.Pointer {
						nestedType = nestedType.Elem()
						nestedValue = nestedValue.Elem()
					}
					if nestedType.Kind() != reflect.Struct {
						panic("managerRecurse fields must be of struct types")
					}

					populate(nestedType, nestedValue)
				} else if _, skipFill := field.Tag.Lookup("managerSkipFill"); !skipFill {
					params = append(params, field.Type)
					valueHandlers = append(valueHandlers, func(value reflect.Value) {
						structValue.Field(i).Set(value)
					})
				}
			}
		}
	}

	populate(objTy.Elem(), reflect.ValueOf(obj).Elem())

	return &pointerComponentFactory{
		obj:           obj,
		compTy:        compTy,
		params:        params,
		valueHandlers: valueHandlers,
	}
}

type pointerComponentFactory struct {
	obj           any
	compTy        reflect.Type
	params        []reflect.Type
	valueHandlers []func(reflect.Value)
}

func (factory *pointerComponentFactory) ComponentType() reflect.Type { return factory.compTy }
func (factory *pointerComponentFactory) Params() []reflect.Type      { return factory.params }
func (factory *pointerComponentFactory) Call(values []reflect.Value) []reflect.Value {
	for i, value := range values {
		factory.valueHandlers[i](value)
	}

	if constructor, ok := factory.obj.(ComponentConstruct); ok {
		constructor.ComponentConstruct()
	}

	return []reflect.Value{reflect.ValueOf(factory.obj)}
}

type ComponentConstruct interface {
	ComponentConstruct()
}

func (manager *Manager) provideComponent(name string, factory ComponentFactory) reflect.Type {
	compTy := factory.ComponentType()
	params := factory.Params()

	for _, paramTy := range params {
		if paramTy.Implements(reflectutil.TypeOf[ListInterface]()) {
			list := reflect.Zero(paramTy).Interface().(ListInterface)
			if _, exists := manager.componentFactories[paramTy]; !exists {
				manager.initListComponent(list)
			}
		}
	}

	cf := &componentFactory{
		name:     name,
		muxImpls: []reflect.Type{},
	}

	cf.build = func(manager *Manager) (*componentInfo, error) {
		ctx := UtilContext{
			ComponentName: name,
			ComponentType: compTy,
			AddOnInit: func(f func() error) {
				manager.onInitHooks[compTy] = append(manager.onInitHooks[compTy], f)
			},
		}

		deps := map[reflect.Type]*componentInfo{}

		args := make([]reflect.Value, 0, len(params)+len(manager.lateDependenciesOf[compTy]))
		for _, param := range params {
			arg, err := manager.resolve(param, &ctx, deps)
			if err != nil {
				return nil, err
			}

			args = append(args, reflect.ValueOf(arg))
		}
		for _, dep := range manager.lateDependenciesOf[compTy] {
			arg, err := manager.resolve(dep, &ctx, deps)
			if err != nil {
				return nil, err
			}

			args = append(args, reflect.ValueOf(arg))
		}

		var impls []MuxImpl
		for _, dep := range cf.muxImpls {
			impl, err := manager.resolve(dep, &ctx, deps)
			if err != nil {
				return nil, err
			}
			impls = append(impls, impl.(MuxImpl))
		}

		out := factory.Call(args)

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

		info := &componentInfo{
			name:         name,
			ty:           compTy,
			component:    comp,
			dependents:   0,
			dependencies: deps,
		}
		return info, nil
	}

	manager.componentFactories[compTy] = cf

	return compTy
}

// ProvideMuxImpl provides a MuxImpl factory.
// It is similar to Manager.Provide(), but with an additional parameter interfaceFunc,
// where the argument is in the form `Interface.AnyFunc`.
func (manager *Manager) ProvideMuxImpl(name string, factory ComponentFactory, interfaceFunc any) {
	compTy := manager.provideComponent(name, factory)
	itfTy := reflect.TypeOf(interfaceFunc).In(0)

	manager.preBuildTasks = append(manager.preBuildTasks, func() {
		factory := manager.componentFactories[itfTy]
		factory.muxImpls = append(factory.muxImpls, compTy)
	})
}

func (manager *Manager) ProvideListImpl(name string, factory ComponentFactory, list ListInterface) {
	compTy := manager.provideComponent(name, factory)

	listTy := reflect.TypeOf(list)
	interfaceTy := list.listInterfaceTy()

	if _, exists := manager.componentFactories[listTy]; !exists {
		manager.initListComponent(list)
	}

	manager.lateDependenciesOf[listTy] = append(manager.lateDependenciesOf[listTy], compTy)
	manager.lateDependentsOf[compTy] = append(manager.lateDependentsOf[compTy], listTy)

	manager.listImpls[interfaceTy] = append(manager.listImpls[interfaceTy], compTy)
}

func (manager *Manager) initListComponent(list ListInterface) {
	itfTy := list.listInterfaceTy()
	manager.provideComponent(itfTy.String() + "-list", list.listFactory(manager))
}

type List[T any] struct {
	BaseComponent
	Impls []T
}

type ListInterface interface {
	listInterfaceTy() reflect.Type
	listFactory(manager *Manager) ComponentFactory
	listRemoveImpl(obj Component)
}

func (*List[T]) listInterfaceTy() reflect.Type { return reflectutil.TypeOf[T]() }

func (*List[T]) listFactory(manager *Manager) ComponentFactory {
	return &listComponentFactory[T]{manager: manager}
}

func (list *List[T]) listRemoveImpl(obj Component) {
	newList := []T{}
	for _, impl := range list.Impls {
		if any(impl) == obj {
			continue
		}
		newList = append(newList, impl)
	}
	list.Impls = newList
}

type listComponentFactory[T any] struct{ manager *Manager }

func (*listComponentFactory[T]) ComponentType() reflect.Type { return reflectutil.TypeOf[*List[T]]() }
func (*listComponentFactory[T]) Params() []reflect.Type      { return nil }
func (f *listComponentFactory[T]) Call(args []reflect.Value) []reflect.Value {
	impls := []T{}
	for _, arg := range args {
		impls = append(impls, arg.Interface().(T))
	}

	return []reflect.Value{reflect.ValueOf(&List[T]{
		Impls: impls,
	})}
}

// A generic type that implements GenericUtil can be used as a component parameter.
type GenericUtil interface {
	ImplementsGenericUtilMarker()

	// The parameters requested by the util type.
	UtilReqs() []reflect.Type
	// Constructs the util type, where the args have types same as the return value of `UtilReqs`.
	Construct(args []reflect.Value) error
}

func (manager *Manager) resolve(
	req reflect.Type,
	ctx *UtilContext,
	deps map[reflect.Type]*componentInfo,
) (any, error) {
	if req == reflect.TypeOf(manager) {
		return manager, nil
	}
	if req == reflect.TypeOf(ctx) {
		return ctx, nil
	}

	factory, hasUtilFactory := manager.utils[req]

	if req.Implements(reflectutil.TypeOf[GenericUtil]()) {
		if req.Kind() != reflect.Pointer {
			panic("GenericUtil must be implemented on pointer receivers")
		}

		// Do not reuse this value in the constructor to avoid sharing states.
		reqs := reflect.Zero(req).Interface().(GenericUtil).UtilReqs()

		factory = utilFactory{
			constructor: func(values []reflect.Value) (any, error) {
				instance := reflect.New(req.Elem()).Interface().(GenericUtil)
				err := instance.Construct(values)
				if err != nil {
					return nil, fmt.Errorf("cannot construct util %T for %v: %w", instance, req, err)
				}
				return instance, nil
			},
			reqs: reqs,
		}

		hasUtilFactory = true
	}

	if hasUtilFactory {
		utilArgs := []reflect.Value{}

		for _, utilReq := range factory.reqs {
			utilArg, err := manager.resolve(utilReq, ctx, deps)
			if err != nil {
				return nil, fmt.Errorf("resolving dependency %v for util %v: %w", utilReq, req, err)
			}

			utilArgs = append(utilArgs, reflect.ValueOf(utilArg))
		}

		ret, err := factory.constructor(utilArgs)
		if err != nil {
			return nil, fmt.Errorf("constructing util %v: %w", req, err)
		}

		return ret, nil
	}

	if factory, exists := manager.componentFactories[req]; exists {
		delete(manager.componentFactories, req)

		comp, err := factory.build(manager)
		if err != nil {
			return nil, fmt.Errorf("building component %v: %w", req, err)
		}

		manager.components[req] = comp
	}

	if info, exists := manager.components[req]; exists {
		if ctx != nil {
			// ctx describes the dependent, nil if this is not requested from a dependent
			info.dependents += 1
			if manager.lateDependentsOf[info.ty] != nil && info.dependents > 1 {
				return nil, fmt.Errorf("list implementations cannot have any dependents other than the list")
			}
		}
		deps[req] = info
		return info.component, nil
	}

	return nil, fmt.Errorf("unknown dependency type %v", req)
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
	name   string
	ty     reflect.Type
	isMux  bool
	isList bool
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
		_, isList := info.component.(ListInterface)
		*list = append(*list, dotNode{
			name:   info.name,
			ty:     ty,
			isMux:  isMux,
			isList: isList,
		})
	}

	for _, list := range [][]dotNode{roots, nonRoots} {
		sort.Slice(list, func(i, j int) bool {
			return list[i].name < list[j].name
		})
	}

	cleanName := func(name string) string {
		return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(name, "/", "\\n"), "-", "\\n"), ".", "\\n")
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
			if node.isList {
				style, fillColor = "filled", "#ff8822"
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
	for _, comp := range manager.components {
		enableFlag := comp.component.Options().EnableFlag()
		if enableFlag != nil && !*enableFlag {
			dependents := manager.lateDependentsOf[comp.ty]
			for _, dependent := range dependents {
				delete(manager.components[dependent].dependencies, comp.ty)
				if list, isList := comp.component.(ListInterface); isList {
					list.listRemoveImpl(comp.component)
				}
				comp.dependents -= 1
			}
		}
	}

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
				"Cannot disable %q because %d components (%v) depend on it but are not disabled",
				disabledComp.name,
				disabledComp.dependents,
				dependents,
			)
			return
		}
	}

	manager.orderComponents()
}

func (manager *Manager) orderComponents() {
	manager.orderedComponents = make([]*componentInfo, 0, len(manager.components))
	keys := map[reflect.Type]uint8{}
	for key := range manager.components {
		keys[key] = 2
	}
	for len(keys) > 0 {
		var key reflect.Type
		for key_ := range keys {
			key = key_
			break
		}
		if err := manager.insertDeps(key, &manager.orderedComponents, keys); err != nil {
			panic(fmt.Sprintf("dependency cycle detected: %v", err))
		}
	}
}

func (manager *Manager) insertDeps(of reflect.Type, into *[]*componentInfo, keys map[reflect.Type]uint8) []reflect.Type {
	if step, exists := keys[of]; !exists {
		return nil // already inserted
	} else if step == 1 {
		return []reflect.Type{of}
	}

	keys[of] = 1
	for dep := range manager.components[of].dependencies {
		if err := manager.insertDeps(dep, into, keys); err != nil {
			return append(err, of)
		}
	}
	*into = append(*into, manager.components[of])
	delete(keys, of)
	return nil
}

func (manager *Manager) Init(ctx context.Context, logger logrus.FieldLogger) error {
	for _, comp := range manager.orderedComponents {
		logger.WithField("mod", comp.name).Info("Initializing")

		for _, onInit := range manager.onInitHooks[comp.ty] {
			if err := onInit(); err != nil {
				return fmt.Errorf("error calling on-init hook for %q: %w", comp.name, err)
			}
		}

		if err := comp.component.Init(); err != nil {
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
