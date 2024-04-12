//go:build slow

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
package multileader_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"

	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/multileader"
	"github.com/kubewharf/kelemetry/pkg/metrics"
)

const NumSteps = 3

func TestElector(t *testing.T) {
	for i := 1; i <= 2; i++ {
		t.Run(fmt.Sprintf("run as %d processes", i), func(t *testing.T) {
			run(t, i)
		})
	}
}

func run(t *testing.T, numProcesses int) {
	assert := assert.New(t)

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	clock := clock.RealClock{}

	metrics, _ := metrics.NewMock(clock)

	client := &k8s.MockClient{
		Name:    "test-cluster",
		Objects: []runtime.Object{},
	}
	client.BindKlog(4)

	config := multileader.Config{
		Enable:    true,
		Namespace: "test-ns",
		Name:      "test-lease",
		// we need to use real time because k8s.io/client-go/leaderelection does not have a fake impl.
		LeaseDuration: time.Millisecond * 1500,
		RenewDeadline: time.Millisecond * 1000,
		RetryPeriod:   time.Millisecond * 200,
		NumLeaders:    2,
	}

	globalAcquisitions := [NumSteps]int32{}

	wg := sync.WaitGroup{}
	wg.Add(numProcesses)

	for processId := 0; processId < numProcesses; processId++ {
		processStopCh := make(chan struct{})

		go func(processId int) {
			defer wg.Done()

			config := config
			config.Identity = fmt.Sprintf("process-%d", processId)
			elector, err := multileader.NewElector(
				"test-comp",
				logger,
				clock,
				&config,
				client,
				metrics,
			)
			assert.NoError(err)

			leases := client.KubernetesClient().CoordinationV1().Leases("test-ns")

			stepCount := 0

			electorAcquisitions := new(int32)

			elector.Run(func(doneCh <-chan struct{}) {
				acquisitions := atomic.AddInt32(electorAcquisitions, 1)
				assert.Equal(int32(1), acquisitions)
				defer atomic.AddInt32(electorAcquisitions, -1)

				defer func() { stepCount++ }()

				clock.Sleep(config.RenewDeadline * 4)

				leaseList, err := leases.List(context.Background(), metav1.ListOptions{})
				assert.NoError(err)

				numRenewed := 0
				var myLease *coordinationv1.Lease
				for _, lease := range leaseList.Items {
					if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != config.Identity {
						continue
					}

					timeSinceRenew := clock.Since(lease.Spec.RenewTime.Time)
					if timeSinceRenew < config.RenewDeadline {
						numRenewed += 1
						myLease = lease.DeepCopy()
					}
				}

				assert.LessOrEqual(numRenewed, 1, "each elector should only renew one lease")

				select {
				case <-doneCh:
					assert.Failf("doneCh should not be closed until lease0 is deleted", "process %q step %d", config.Identity, stepCount)
				default:
				}

				atomic.AddInt32(&globalAcquisitions[stepCount], 1)

				switch stepCount {
				case 0:
					// assert that a crashed leader triggers leader re-election
				case 1:
					myLease.Spec.HolderIdentity = ptr.To("someone-else")
					myLease.Spec.LeaseDurationSeconds = ptr.To(
						600,
					) // an arbitrary number much longer than all other durations in the config

					_, err := leases.Update(context.Background(), myLease, metav1.UpdateOptions{})
					assert.NoError(err)

					<-doneCh // assert that doneCh is closed when the holder identity changes

					err = leases.Delete(
						context.Background(),
						myLease.Name,
						metav1.DeleteOptions{},
					) // delete the identity to allow subsequent re-acquisition quickly
					assert.NoError(err)
				case 2:
					close(processStopCh) // assert that closing processStopCh breaks the loop
				}
			}, processStopCh)
		}(processId)
	}

	wg.Wait()

	for step, cnt := range globalAcquisitions {
		assert.Equalf(int32(numProcesses), cnt, "all processes executed step %d", step)
	}
}
