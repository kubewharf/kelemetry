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

package auditdump

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/kubewharf/kelemetry/pkg/audit"
	auditwebhook "github.com/kubewharf/kelemetry/pkg/audit/webhook"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("audit-dump", manager.Ptr(&dumper{}))
}

type options struct {
	target string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringVar(&options.target, "audit-dump-file", "", "append received audit events to the specified path (leave empty to disable)")
}

func (options *options) EnableFlag() *bool {
	isEnable := options.target != ""
	return &isEnable
}

type dumper struct {
	options options
	Logger  logrus.FieldLogger
	Webhook auditwebhook.Webhook
	stream  interface {
		io.WriteCloser
		Sync() error
	}
	recvCh <-chan *audit.Message
}

func (dumper *dumper) Options() manager.Options {
	return &dumper.options
}

func (dumper *dumper) Init(ctx context.Context) (err error) {
	dumper.stream, err = os.OpenFile(dumper.options.target, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open file to dump audit events: %w", err)
	}

	dumper.recvCh = dumper.Webhook.AddSubscriber("audit-dump-subscriber")

	return nil
}

func (dumper *dumper) Start(stopCh <-chan struct{}) error {
	go func() {
		defer shutdown.RecoverPanic(dumper.Logger)

		flushCh := chan struct{}(nil)

		for {
			select {
			case <-stopCh:
				return
			case <-flushCh:
				if err := dumper.stream.Sync(); err != nil {
					dumper.Logger.WithError(err).Error("Cannot flush file")
				}
			case message, chOpen := <-dumper.recvCh:
				if !chOpen {
					return
				}

				if err := dumper.handleEvent(message); err != nil {
					dumper.Logger.WithError(err).Error("Cannot append message")
				}
			}
		}
	}()

	return nil
}

func (dumper *dumper) handleEvent(message *audit.Message) error {
	buf, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("cannot reserialize message: %w", err)
	}

	for _, bytes := range [][]byte{buf, []byte("\n")} {
		_, err = dumper.stream.Write(bytes)
		if err != nil {
			return fmt.Errorf("cannot write to file: %w", err)
		}
	}

	return nil
}

func (dumper *dumper) Close() error {
	return dumper.stream.Close()
}
