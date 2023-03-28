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

package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("http", newServer)
}

type Server interface {
	manager.Component
	Routes() gin.IRoutes
}

type options struct {
	address string
	port    uint16

	cert string
	key  string

	readTimeout       time.Duration
	readHeaderTimeout time.Duration
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.address, "http-address", "", "HTTP server bind address")
	fs.Uint16Var(&options.port, "http-port", 8080, "HTTP server port")
	fs.StringVar(&options.cert, "http-tls-cert", "", "HTTP server TLS certificate (leave empty to disable HTTPS)")
	fs.StringVar(&options.key, "http-tls-key", "", "HTTP server TLS key (leave empty to disable HTTPS)")
	fs.DurationVar(&options.readTimeout, "http-read-timeout", 0, "HTTP server timeout for reading requests")
	fs.DurationVar(
		&options.readHeaderTimeout,
		"http-read-header-timeout",
		0,
		"HTTP server timeout for reading request headers",
	)
}

func (options *options) EnableFlag() *bool { return nil }

type server struct {
	options options
	logger  logrus.FieldLogger

	router *gin.Engine
	server *http.Server
}

func newServer(
	logger logrus.FieldLogger,
) Server {
	return &server{
		logger: logger,
	}
}

func (server *server) Options() manager.Options {
	return &server.options
}

func (server *server) Init() error {
	server.router = gin.New()

	return nil
}

func (server *server) Start(ctx context.Context) error {
	hs := &http.Server{
		Addr:              net.JoinHostPort(server.options.address, fmt.Sprint(server.options.port)),
		ReadHeaderTimeout: server.options.readHeaderTimeout,
		ReadTimeout:       server.options.readTimeout,
		Handler:           server.router,
	}
	server.server = hs

	listener, err := net.Listen("tcp", hs.Addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", hs.Addr, err)
	}

	var serveFunc func() error
	if server.options.cert != "" && server.options.key != "" {
		serveFunc = func() error { return hs.ServeTLS(listener, server.options.cert, server.options.key) }
	} else {
		serveFunc = func() error { return hs.Serve(listener) }
	}

	go func() {
		defer shutdown.RecoverPanic(server.logger)

		err := serveFunc()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			server.logger.WithError(err).Error()
		}
	}()

	return nil
}

func (server *server) Close(ctx context.Context) error {
	ctx, cancelFunc := context.WithTimeout(ctx, time.Second*10)
	defer cancelFunc()

	if err := server.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown error: %w", err)
	}

	return nil
}

func (server *server) Routes() gin.IRoutes {
	return server.router
}
