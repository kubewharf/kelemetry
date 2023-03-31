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

package objectcache_test

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/k8s"
	"github.com/kubewharf/kelemetry/pkg/k8s/objectcache"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util"
)

func TestGet(t *testing.T) {
	assert := assert.New(t)

	clock := clock.RealClock{} // the clock is unused
	metricsClient, metricsOutput := metrics.NewMock(clock)

	cache := &objectcache.ObjectCache{
		Logger: logrus.New(),
		Clock:  clock,
		Clients: &k8s.MockClients{
			TargetClusterName: "test-cluster",
			Clients: map[string]*k8s.MockClient{
				"test-cluster": {
					Name: "test-cluster",
					Objects: []runtime.Object{
						&corev1.ConfigMap{
							TypeMeta: metav1.TypeMeta{
								APIVersion: "v1",
								Kind:       "ConfigMap",
							},
							ObjectMeta: metav1.ObjectMeta{
								Name:      "test-cm",
								Namespace: "default",
							},
							Data: map[string]string{"foo": "bar"},
						},
					},
				},
			},
		},
		Metrics:            metricsClient,
		DiffCache:          nil, // TODO
		CacheRequestMetric: metrics.New[*objectcache.CacheRequestMetric](metricsClient),
	}

	assert.NoError(cache.Init())

	for i := 0; i < 2; i++ {
		uns, err := cache.Get(context.Background(), util.ObjectRef{
			Cluster:              "test-cluster",
			GroupVersionResource: corev1.SchemeGroupVersion.WithResource("configmaps"),
			Namespace:            "default",
			Name:                 "test-cm",
		})
		assert.NoError(err)

		fooValue, fooExists, err := unstructured.NestedString(uns.Object, "data", "foo")
		assert.NoError(err)

		if i == 0 {
			assert.True(fooExists)
			assert.Equal("bar", fooValue)

			penetrations := metricsOutput.Get("object_cache_request", map[string]string{
				"error": "Penetrated",
				"hit":   "false",
			})
			assert.Equal(int64(1), penetrations.Int)
		} else {
			assert.True(fooExists)
			assert.Equal("bar", fooValue)

			hits := metricsOutput.Get("object_cache_request", map[string]string{
				"error": "nil",
				"hit":   "true",
			})
			assert.Equal(int64(1), hits.Int)
		}
	}
}
