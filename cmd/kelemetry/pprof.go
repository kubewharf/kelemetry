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
	"net/http"
	_ "net/http/pprof"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("pprof", manager.Ptr(&pprofServer{}))
}

type pprofOptions struct {
	enable bool
	addr   string
}

func (options *pprofOptions) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "pprof-enable", false, "enable pprof")
	fs.StringVar(&options.addr, "pprof-addr", ":6060", "pprof server bind address")
}

func (options *pprofOptions) EnableFlag() *bool { return &options.enable }

type pprofServer struct {
	options pprofOptions
	Logger  logrus.FieldLogger
}

func (server *pprofServer) Options() manager.Options { return &server.options }

func (server *pprofServer) Init() error {
	go func() {
		defer shutdown.RecoverPanic(server.Logger)
		server.Logger.Error(http.ListenAndServe(server.options.addr, nil))
	}()

	return nil
}

func (server *pprofServer) Start(ctx context.Context) error { return nil }

func (server *pprofServer) Close(ctx context.Context) error { return nil }
