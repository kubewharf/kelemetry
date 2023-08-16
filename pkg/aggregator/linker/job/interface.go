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

package linkjob

import (
	"context"
	"time"

	"github.com/kubewharf/kelemetry/pkg/aggregator/tracer"
	"github.com/kubewharf/kelemetry/pkg/manager"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

type Publisher interface {
	Publish(job *LinkJob)
}

type Subscriber interface {
	Subscribe(ctx context.Context) <-chan *LinkJob
}

func init() {
	manager.Global.Provide("linker-job-publisher", manager.Ptr[Publisher](&publisherMux{
		Mux: manager.NewMux("linker-job-publisher", false),
	}))
	manager.Global.Provide("linker-job-subscriber", manager.Ptr[Subscriber](&subscriberMux{
		Mux: manager.NewMux("linker-job-subscriber", false),
	}))
}

type publisherMux struct {
	*manager.Mux
}

func (mux *publisherMux) Publish(job *LinkJob) {
	mux.Impl().(Publisher).Publish(job)
}

type subscriberMux struct {
	*manager.Mux
}

func (mux *subscriberMux) Subscribe(ctx context.Context) <-chan *LinkJob {
	return mux.Impl().(Subscriber).Subscribe(ctx)
}

type LinkJob struct {
	Object    utilobject.Rich
	EventTime time.Time
	Span      tracer.SpanContext
}
