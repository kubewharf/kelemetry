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

package tracer

import (
	"time"

	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("tracer", newMux)
}

type Tracer interface {
	CreateSpan(span Span) (SpanContext, error)
	InjectCarrier(spanContext SpanContext) ([]byte, error)
	ExtractCarrier(textMap []byte) (SpanContext, error)
}

type Span struct {
	Type       string
	Name       string
	StartTime  time.Time
	FinishTime time.Time
	Parent     SpanContext
	Follows    SpanContext
	Tags       map[string]string
	Logs       []Log
}

type SpanContext any

type Log struct {
	Type    zconstants.LogType
	Message string
	Attrs   [][2]string
}

type mux struct {
	*manager.Mux
}

func newMux() Tracer {
	return &mux{
		Mux: manager.NewMux("tracer", false),
	}
}

func (mux *mux) CreateSpan(span Span) (SpanContext, error) {
	return mux.Impl().(Tracer).CreateSpan(span)
}

func (mux *mux) InjectCarrier(spanContext SpanContext) ([]byte, error) {
	return mux.Impl().(Tracer).InjectCarrier(spanContext)
}

func (mux *mux) ExtractCarrier(textMap []byte) (SpanContext, error) {
	return mux.Impl().(Tracer).ExtractCarrier(textMap)
}
