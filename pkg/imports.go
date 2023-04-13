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

// Default packages in the default build of kelemetry
package kelemetry_pkg

import (
	_ "github.com/kubewharf/kelemetry/pkg/aggregator/aggregatorevent"
	_ "github.com/kubewharf/kelemetry/pkg/aggregator/eventdecorator/eventtagger"
	_ "github.com/kubewharf/kelemetry/pkg/aggregator/objectdecorator"
	_ "github.com/kubewharf/kelemetry/pkg/aggregator/spancache/etcd"
	_ "github.com/kubewharf/kelemetry/pkg/aggregator/spancache/local"
	_ "github.com/kubewharf/kelemetry/pkg/aggregator/tracer/otel"
	_ "github.com/kubewharf/kelemetry/pkg/annotationlinker"
	_ "github.com/kubewharf/kelemetry/pkg/audit"
	_ "github.com/kubewharf/kelemetry/pkg/audit/consumer"
	_ "github.com/kubewharf/kelemetry/pkg/audit/dump"
	_ "github.com/kubewharf/kelemetry/pkg/audit/forward"
	_ "github.com/kubewharf/kelemetry/pkg/audit/mq/local"
	_ "github.com/kubewharf/kelemetry/pkg/audit/producer"
	_ "github.com/kubewharf/kelemetry/pkg/audit/webhook"
	_ "github.com/kubewharf/kelemetry/pkg/audit/webhook/clustername"
	_ "github.com/kubewharf/kelemetry/pkg/audit/webhook/clustername/address"
	_ "github.com/kubewharf/kelemetry/pkg/diff/api"
	_ "github.com/kubewharf/kelemetry/pkg/diff/cache/etcd"
	_ "github.com/kubewharf/kelemetry/pkg/diff/cache/local"
	_ "github.com/kubewharf/kelemetry/pkg/diff/controller"
	_ "github.com/kubewharf/kelemetry/pkg/diff/decorator"
	_ "github.com/kubewharf/kelemetry/pkg/event"
	_ "github.com/kubewharf/kelemetry/pkg/frontend"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/backend/jaeger-storage"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/clusterlist/options"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/http/redirect"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/http/trace"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/tf/config/default"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/tracecache/etcd"
	_ "github.com/kubewharf/kelemetry/pkg/frontend/tracecache/local"
	_ "github.com/kubewharf/kelemetry/pkg/k8s/config/mapoption"
	_ "github.com/kubewharf/kelemetry/pkg/metrics/noop"
	_ "github.com/kubewharf/kelemetry/pkg/metrics/prometheus"
	_ "github.com/kubewharf/kelemetry/pkg/ownerlinker"
)
