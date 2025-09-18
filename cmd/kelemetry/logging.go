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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

type loggingOptions struct {
	level     string
	formatter string

	file       string
	maxSizeMb  int
	maxBackups int
	maxAgeDays int
	localTime  bool
	compress   bool

	dot   string
	usage string
}

func (options *loggingOptions) setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.level, "log-level", "debug", "logrus log level")
	fs.StringVar(&options.formatter, "log-format", "text", "logrus log format")

	fs.StringVar(&options.file, "log-file", "", "logrus log output file (leave empty for stdout)")
	fs.IntVar(&options.maxSizeMb, "log-max-size-mb", 1024, "max log file size in megabytes, 0 to disable size-based rotation")
	fs.IntVar(&options.maxBackups, "log-max-backups", 10, "max log file backups, 0 to disable count-based cleanup")
	fs.IntVar(&options.maxAgeDays, "log-max-age-days", 30, "max log file age in days, 0 to disable time-based cleanup")
	fs.BoolVar(&options.localTime, "log-local-time", true, "use local time for log file names")
	fs.BoolVar(&options.compress, "log-compress", true, "compress rotated log files")

	fs.StringVar(&options.dot, "dot", "", "write dependencies as graphviz output")
	fs.StringVar(&options.usage, "usage", "", "write command usage to file")
}

func (options *loggingOptions) execute(logger *logrus.Logger, fs *pflag.FlagSet) error {
	logLevel, err := logrus.ParseLevel(options.level)
	if err != nil {
		return fmt.Errorf("invalid log level %q: %w", options.level, err)
	}
	logger.SetLevel(logLevel)

	switch options.formatter {
	case "text":
		logger.SetFormatter(&logrus.TextFormatter{})
	case "json":
		logger.SetFormatter(&logrus.JSONFormatter{})
	default:
		return fmt.Errorf("invalid log formatter %q", options.formatter)
	}

	if options.file != "" {
		logger.SetOutput(&lumberjack.Logger{
			Filename:   options.file,
			MaxSize:    options.maxSizeMb,
			MaxBackups: options.maxBackups,
			MaxAge:     options.maxAgeDays,
			LocalTime:  options.localTime,
			Compress:   options.compress,
		})
	}

	if options.dot != "" {
		file, err := os.Create(options.dot)
		if err != nil {
			return err
		}

		_, err = file.WriteString(manager.Global.Dot())
		if err != nil {
			return err
		}

		logger.Infof("graphviz written to %s", options.dot)

		os.Exit(0)
	}

	if options.usage != "" {
		for _, replace := range []struct {
			f func() (string, error)
			v string
		}{
			{f: os.Executable, v: "$EXEC"},
			{f: func() (string, error) {
				if exec, err := os.Executable(); err == nil {
					return filepath.Dir(exec), nil
				} else {
					return "", err
				}
			}, v: "$(dirname $EXEC)"},
			{f: os.Getwd, v: "$PWD"},
			{f: os.UserHomeDir, v: "$HOME"},
		} {
			if path, err := replace.f(); err == nil {
				fs.VisitAll(func(f *pflag.Flag) {
					f.DefValue = strings.ReplaceAll(f.DefValue, path, replace.v)
				})
			}
		}

		flagUsages := fs.FlagUsages()

		file, err := os.Create(options.usage)
		if err != nil {
			return err
		}
		_, err = file.WriteString(flagUsages)
		if err != nil {
			return err
		}

		logger.Infof("options written to %s", options.usage)

		os.Exit(0)
	}

	return nil
}
