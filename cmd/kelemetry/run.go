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

package kelemetry

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func Main() {
	rootLogger := logrus.New()

	if err := Run(rootLogger); err != nil {
		rootLogger.Error(err.Error())
		os.Exit(1)
	}
}

func Run(rootLogger *logrus.Logger) error {
	manager := manager.Global
	shutdownTrigger, stopCh := shutdown.NewShutdownTrigger()
	provideUtils(manager, rootLogger, shutdownTrigger)

	err := manager.Build()
	if err != nil {
		return fmt.Errorf("cannot initialize components: %w", err)
	}

	fs := pflag.NewFlagSet("kelemetry", pflag.ContinueOnError)

	var loggingOptions loggingOptions
	loggingOptions.setup(fs)
	manager.SetupFlags(fs)

	if err := fs.Parse(os.Args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}

		return err
	}

	if err := loggingOptions.execute(rootLogger, fs); err != nil {
		return err
	}

	fs.VisitAll(func(flag *pflag.Flag) {
		rootLogger.WithField("name", flag.Name).WithField("value", flag.Value.String()).Info("Option")
	})

	manager.TrimDisabled(rootLogger)

	rootCtx := context.Background()
	ctx, cancelFunc := context.WithCancel(rootCtx)
	defer cancelFunc()

	if err := manager.Init(ctx, rootLogger); err != nil {
		return err
	}

	shutdownTrigger.SetupSignalHandler()
	if err := manager.Start(rootLogger, stopCh); err != nil {
		return err
	}

	rootLogger.Info("Startup complete")
	<-stopCh
	rootLogger.Info("Received shutdown signal")

	return manager.Close(rootLogger)
}

func provideUtils(m *manager.Manager, logger logrus.FieldLogger, shutdownTrigger *shutdown.ShutdownTrigger) {
	m.ProvideUtil(func(ctx *manager.UtilContext) (logrus.FieldLogger, error) {
		return logger.WithField("mod", ctx.ComponentName), nil
	})
	m.ProvideUtil(func() (*shutdown.ShutdownTrigger, error) {
		return shutdownTrigger, nil
	})
	m.ProvideUtil(func() (clock.Clock, error) {
		return clock.RealClock{}, nil
	})
}
