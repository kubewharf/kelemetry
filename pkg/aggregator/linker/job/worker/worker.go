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

package linkjobworker

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/kelemetry/pkg/aggregator"
	"github.com/kubewharf/kelemetry/pkg/aggregator/linker"
	linkjob "github.com/kubewharf/kelemetry/pkg/aggregator/linker/job"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide("linker-job-worker", manager.Ptr(&worker{}))
}

type workerOptions struct {
	WorkerCount int
}

func (options *workerOptions) Setup(fs *pflag.FlagSet) {
	fs.IntVar(&options.WorkerCount, "linker-worker-count", 0, "Number of workers to execute link jobs")
}
func (options *workerOptions) EnableFlag() *bool { return pointer.Bool(options.WorkerCount > 0) }

type worker struct {
	options    workerOptions
	Logger     logrus.FieldLogger
	Linkers    *manager.List[linker.Linker]
	Subscriber linkjob.Subscriber
	Aggregator aggregator.Aggregator

	ch <-chan *linkjob.LinkJob
}

func (worker *worker) Options() manager.Options { return &worker.options }
func (worker *worker) Init() error {
	worker.ch = worker.Subscriber.Subscribe(context.Background()) // never unsubscribe
	return nil
}

func (worker *worker) Start(ctx context.Context) error {
	for workerId := 0; workerId < worker.options.WorkerCount; workerId++ {
		go func(workerId int) {
			defer shutdown.RecoverPanic(worker.Logger)

			for {
				select {
				case <-ctx.Done():
					return
				case job := <-worker.ch:
					worker.executeJob(ctx, worker.Logger.WithFields(job.Object.AsFields("job")), job)
				}
			}
		}(workerId)
	}

	return nil
}
func (worker *worker) Close(ctx context.Context) error { return nil }

func (worker *worker) executeJob(ctx context.Context, logger logrus.FieldLogger, job *linkjob.LinkJob) {
	for _, linker := range worker.Linkers.Impls {
		linkerLogger := logger.WithField("linker", fmt.Sprintf("%T", linker))
		if err := worker.execute(ctx, linkerLogger, linker, job); err != nil {
			logger.WithError(err).Error("generating links")
		}
	}
}

func (worker *worker) execute(ctx context.Context, logger logrus.FieldLogger, linker linker.Linker, job *linkjob.LinkJob) error {
	links, err := linker.Lookup(ctx, job.Object)
	if err != nil {
		return metrics.LabelError(fmt.Errorf("calling linker: %w", err), "CallLinker")
	}

	for _, link := range links {
		linkedSpan, err := worker.Aggregator.EnsureObjectSpan(ctx, link.Object, job.EventTime)
		if err != nil {
			return metrics.LabelError(fmt.Errorf("creating object span: %w", err), "CreateLinkedObjectSpan")
		}

		forwardTags := map[string]string{}
		zconstants.TagLinkedObject(forwardTags, zconstants.LinkRef{
			Key:   link.Object.Key,
			Role:  link.Role,
			Class: link.Class,
		})
		_, _, err = worker.Aggregator.GetOrCreatePseudoSpan(
			ctx,
			job.Object,
			zconstants.PseudoTypeLink,
			job.EventTime,
			job.Span,
			nil,
			forwardTags,
			link.DedupId,
		)
		if err != nil {
			return metrics.LabelError(
				fmt.Errorf("creating link span from source object to linked object: %w", err),
				"CreateForwardLinkSpan",
			)
		}

		backwardTags := map[string]string{}
		zconstants.TagLinkedObject(backwardTags, zconstants.LinkRef{
			Key:   job.Object.Key,
			Role:  zconstants.ReverseLinkRole(link.Role),
			Class: link.Class,
		})
		_, _, err = worker.Aggregator.GetOrCreatePseudoSpan(
			ctx,
			link.Object,
			zconstants.PseudoTypeLink,
			job.EventTime,
			linkedSpan,
			nil,
			backwardTags,
			fmt.Sprintf("%s@%s", link.DedupId, job.Object.String()),
		)
		if err != nil {
			return metrics.LabelError(
				fmt.Errorf("creating link span from linked object to source object: %w", err),
				"CreateBackwardLinkSpan",
			)
		}
	}

	return nil
}
