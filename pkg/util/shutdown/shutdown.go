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

package shutdown

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/sirupsen/logrus"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type DeferFunc struct {
	name string
	fn   func(context.Context) error
}

type DeferList struct {
	list      []DeferFunc
	wasClosed int32
	mutex     sync.Mutex
}

func NewDeferList() *DeferList {
	return &DeferList{list: make([]DeferFunc, 0)}
}

func (list *DeferList) DeferContextWithLock(name string, fn func(ctx context.Context) error) {
	list.mutex.Lock()
	defer list.mutex.Unlock()
	list.DeferContext(name, fn)
}

func (list *DeferList) DeferWithLock(name string, fn func() error) {
	list.mutex.Lock()
	defer list.mutex.Unlock()
	list.Defer(name, fn)
}

func (list *DeferList) Defer(name string, fn func() error) {
	list.DeferContext(name, func(ctx context.Context) error { return fn() })
}

func (list *DeferList) DeferContext(name string, fn func(context.Context) error) {
	list.list = append(list.list, DeferFunc{name: name, fn: fn})
}

func (list *DeferList) LockedRun(ctx context.Context, logger logrus.FieldLogger) (string, error) {
	list.mutex.Lock()
	defer list.mutex.Unlock()
	return list.Run(ctx, logger)
}

// Run runs the defer list. Should be called from the Close function of components.
// Returns a nonempty string containing the defer message and the error that occurred
// upon error. Returns empty string and nil error upon success.
func (list *DeferList) Run(ctx context.Context, logger logrus.FieldLogger) (string, error) {
	wasClosed := atomic.SwapInt32(&list.wasClosed, 1)
	if wasClosed > 0 {
		return "", nil
	}

	for i := len(list.list) - 1; i >= 0; i-- {
		entry := &list.list[i]
		logger.Infof("Shutdown: %s", entry.name)
		if err := entry.fn(ctx); err != nil {
			return entry.name, err
		}
	}

	return "", nil
}

func (list *DeferList) RunWithChannel(ctx context.Context, logger logrus.FieldLogger, ch chan<- error) {
	if name, err := list.Run(ctx, logger); err != nil {
		ch <- fmt.Errorf("%s: %w", name, err)
	} else {
		ch <- nil
	}
}

func RecoverPanic(logger logrus.FieldLogger) {
	utilruntime.HandleCrash(func(err any) {
		if logger != nil {
			logger.WithField("error", err).Error()
		}
	})
}

type ShutdownTrigger struct {
	stopCh chan<- struct{}
}

// Converts a stopCh to a context.
func ContextWithStopCh(base context.Context, stopCh <-chan struct{}) context.Context {
	ctx, cancelFunc := context.WithCancel(base)
	go func() {
		<-stopCh
		cancelFunc()
	}()
	return ctx
}

func NewShutdownTrigger() (*ShutdownTrigger, <-chan struct{}) {
	ch := make(chan struct{}, 1)
	stopCh := make(chan struct{})
	go func() {
		defer RecoverPanic(nil)
		<-ch
		close(stopCh)
	}()

	return &ShutdownTrigger{
		stopCh: ch,
	}, stopCh
}

func (trigger *ShutdownTrigger) SetupSignalHandler() {
	go func() {
		defer RecoverPanic(nil)
		c := make(chan os.Signal, 2)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		trigger.Trigger()
	}()
}

func (trigger *ShutdownTrigger) Trigger() {
	select {
	case trigger.stopCh <- struct{}{}:
	default:
	}
}
