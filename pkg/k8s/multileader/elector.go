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

// Cooperative leader election, where a fixed number of controllers can act as the leaders simultaneously.
package multileader

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/channel"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

type Config struct {
	Enable        bool
	Namespace     string
	Name          string
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
	NumLeaders    int
	Identity      string // for testing only
}

func (config *Config) SetupOptions(fs *pflag.FlagSet, prefix string, component string, defaultNumLeaders int) {
	prefix += "-leader-election"

	fs.BoolVar(&config.Enable, prefix+"-enable", true, fmt.Sprintf("enable leader election for %s", component))
	fs.StringVar(&config.Namespace, prefix+"-namespace", "default", fmt.Sprintf("namespace for %s leader election", component))
	fs.StringVar(&config.Name, prefix+"-name", fmt.Sprintf("kelemetry-%s", prefix),
		fmt.Sprintf("lease object name prefix for %s leader election, to be prepended with a leader number", component))
	fs.DurationVar(&config.LeaseDuration, prefix+"-lease-duration", time.Second*15,
		fmt.Sprintf("duration for which %s non-leader candidates wait before force acquiring leadership", component))
	fs.DurationVar(&config.RenewDeadline, prefix+"-renew-deadline", time.Second*10,
		fmt.Sprintf("duration for which %s leaders renew their own leadership", component))
	fs.DurationVar(&config.RetryPeriod, prefix+"-retry-period", time.Second*2,
		fmt.Sprintf("duration for which %s leader electors wait between tries of actions", component))
	fs.IntVar(&config.NumLeaders, prefix+"-num-leaders", defaultNumLeaders,
		fmt.Sprintf("number of leaders elected for %s", component))
}

type Elector struct {
	enable         bool
	name           string
	logger         logrus.FieldLogger
	clock          clock.Clock
	isLeaderMetric *metrics.Metric[*isLeaderMetric]
	isLeaderFlag   []uint32
	Identity       string

	configCreator func() ([]*leaderelection.LeaderElectionConfig, <-chan event, error)
}

type isLeaderMetric struct {
	Component string
	LeaderId  int
}

func (*isLeaderMetric) MetricName() string { return "is_leader" }

func NewElector(
	component string,
	logger logrus.FieldLogger,
	clock clock.Clock,
	config *Config,
	client k8s.Client,
	metricsClient metrics.Client,
) (*Elector, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("cannot get hostname for leader election: %w", err)
	}

	var identity string
	if config.Identity != "" {
		identity = config.Identity
	} else {
		identity = hostname + "_" + string(uuid.NewUUID())
	}

	elector := &Elector{
		enable:         config.Enable,
		name:           component,
		logger:         logger.WithField("identity", identity),
		clock:          clock,
		isLeaderMetric: metrics.New[*isLeaderMetric](metricsClient),
		isLeaderFlag:   make([]uint32, config.NumLeaders),
		Identity:       identity,
		configCreator: func() ([]*leaderelection.LeaderElectionConfig, <-chan event, error) {
			configs := make([]*leaderelection.LeaderElectionConfig, 0, config.NumLeaders)

			for i := int(0); i < config.NumLeaders; i++ {
				rl, err := resourcelock.New(
					resourcelock.LeasesResourceLock,
					config.Namespace,
					fmt.Sprintf("%s-%d", config.Name, i),
					client.KubernetesClient().CoreV1(),
					client.KubernetesClient().CoordinationV1(),
					resourcelock.ResourceLockConfig{
						Identity:      identity,
						EventRecorder: client.EventRecorder(component),
					},
				)
				if err != nil {
					return nil, nil, fmt.Errorf("cannot initialize %s leader election resource lock: %w", component, err)
				}

				leaderElectionConfig := leaderelection.LeaderElectionConfig{
					Lock:            rl,
					LeaseDuration:   config.LeaseDuration,
					RenewDeadline:   config.RenewDeadline,
					RetryPeriod:     config.RetryPeriod,
					ReleaseOnCancel: true,
				}
				configs = append(configs, &leaderElectionConfig)
			}

			eventCh := configureCallbacks(configs)
			return configs, eventCh, nil
		},
	}

	return elector, nil
}

func configureCallbacks(configs []*leaderelection.LeaderElectionConfig) <-chan event {
	ch := make(chan event, 1)

	for i := range configs {
		i := i

		configs[i].Callbacks.OnStartedLeading = func(ctx context.Context) {
			// only the first event needs to get sent
			// other goroutines should exit immediately to avoid leak
			select {
			case ch <- event{i: i, termCtx: ctx}:
			default:
			}
		}
		configs[i].Callbacks.OnStoppedLeading = func() {
		}
	}

	return ch
}

func (elector *Elector) Run(ctx context.Context, run func(ctx context.Context)) {
	defer shutdown.RecoverPanic(elector.logger)

	if !elector.enable {
		wg := &sync.WaitGroup{}
		wg.Add(len(elector.isLeaderFlag))

		for i := range elector.isLeaderFlag {
			go func(i int) {
				atomic.StoreUint32(&elector.isLeaderFlag[i], 1)
				defer atomic.StoreUint32(&elector.isLeaderFlag[i], 0)
				defer wg.Done()
				run(ctx)
			}(i)
		}

		wg.Wait()

		return
	}

	for {
		if !elector.spinOnce(ctx, run) {
			return
		}
	}
}

func (elector *Elector) RunLeaderMetricLoop(ctx context.Context) {
	defer shutdown.RecoverPanic(elector.logger)

	for {
		select {
		case <-elector.clock.After(time.Second * 30):
			for leaderId := range elector.isLeaderFlag {
				flag := atomic.LoadUint32(&elector.isLeaderFlag[leaderId])
				elector.isLeaderMetric.With(&isLeaderMetric{
					Component: elector.name,
					LeaderId:  leaderId,
				}).Gauge(float64(flag))
			}
		case <-ctx.Done():
			for leaderId := range elector.isLeaderFlag {
				elector.isLeaderMetric.With(&isLeaderMetric{
					Component: elector.name,
					LeaderId:  leaderId,
				}).Gauge(0)
			}
			return
		}
	}
}

func (elector *Elector) spinOnce(ctx context.Context, run func(ctx context.Context)) (shouldContinue bool) {
	baseCtx, cancelFunc := context.WithCancel(ctx)
	defer cancelFunc()

	leaderId, termCtx, err := elector.waitForLeader(baseCtx)
	if err != nil {
		elector.logger.WithError(err).Error("leader election error")
		return true
	}

	if termCtx == nil {
		return false
	}

	spinCompleted := make(chan struct{})

	go func() {
		defer shutdown.RecoverPanic(elector.logger)
		atomic.StoreUint32(&elector.isLeaderFlag[leaderId], 1)
		defer atomic.StoreUint32(&elector.isLeaderFlag[leaderId], 0)
		defer cancelFunc() // if leader panics, release the lease
		defer close(spinCompleted)

		run(termCtx)
	}()

	select {
	case <-termCtx.Done():
		elector.logger.Warn("lost leader lease")

		channel.NoisyWaitChannelClose(baseCtx, spinCompleted, time.Second*5, func(msg string) {
			elector.logger.WithField("channel", "spinComplete").Info(msg)
		})

		return true
	case <-baseCtx.Done():
		return false
	}
}

func (elector *Elector) waitForLeader(baseCtx context.Context) (leaderId int, termCtx context.Context, err error) {
	configs, eventCh, err := elector.configCreator()
	if err != nil {
		return -1, nil, err
	}

	cancelFuncs := []func(){}
	defer func() {
		for _, fn := range cancelFuncs {
			fn()
		}
	}()

	wg := &sync.WaitGroup{}
	wg.Add(len(configs))

	for i, config := range configs {
		ctx, cancelFunc := context.WithCancel(baseCtx)
		cancelFuncs = append(cancelFuncs, cancelFunc)

		sub, err := leaderelection.NewLeaderElector(*config)
		if err != nil {
			return -1, nil, fmt.Errorf("cannot initialize elector: %w", err)
		}

		go func(i int) {
			defer shutdown.RecoverPanic(elector.logger)
			defer wg.Done()

			elector.logger.WithField("i", i).Debug("start running subelector")
			sub.Run(ctx)
			elector.logger.WithField("i", i).Debug("subelector stopped")
		}(i)
	}

	var event event
	select {
	case <-baseCtx.Done():
		return -1, nil, nil

	case event = <-eventCh:
	}

	for i, cancelFunc := range cancelFuncs {
		if i != event.i {
			cancelFunc()
		}
	}
	cancelFuncs = nil // do not cancel the leader context

	elector.logger.WithField("elector", event.i).Info("acquired leader lease")

	return event.i, event.termCtx, nil
}

type event struct {
	i int

	// this is a value struct, so we are not leaking any contexts beyond function scope here.
	termCtx context.Context //nolint:containedctx
}
