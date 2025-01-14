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

package trace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jaegertracing/jaeger/model"
	uiconv "github.com/jaegertracing/jaeger/model/converter/json"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/clock"

	"github.com/kubewharf/kelemetry/pkg/frontend/clusterlist"
	jaegerreader "github.com/kubewharf/kelemetry/pkg/frontend/reader"
	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	pkghttp "github.com/kubewharf/kelemetry/pkg/http"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/metrics"
	"github.com/kubewharf/kelemetry/pkg/util/shutdown"
)

func init() {
	manager.Global.Provide("trace-server", manager.Ptr(&server{}))
}

type options struct {
	enable bool
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.BoolVar(&options.enable, "trace-server-enable", false, "enable trace server for frontend")
}

func (options *options) EnableFlag() *bool { return &options.enable }

type server struct {
	options          options
	Logger           logrus.FieldLogger
	Clock            clock.Clock
	Server           pkghttp.Server
	SpanReader       jaegerreader.Interface
	ClusterList      clusterlist.Lister
	TransformConfigs tfconfig.Provider

	RequestMetric *metrics.Metric[*requestMetric]
}

type requestMetric struct {
	Error metrics.LabeledError
}

func (*requestMetric) MetricName() string { return "extension_trace_request" }

func (server *server) Options() manager.Options {
	return &server.options
}

func (server *server) Init() error {
	server.Server.Routes().GET("/extensions/api/v1/trace", func(ctx *gin.Context) {
		logger := server.Logger.WithField("source", ctx.Request.RemoteAddr)
		defer shutdown.RecoverPanic(logger)
		metric := &requestMetric{}
		defer server.RequestMetric.DeferCount(server.Clock.Now(), metric)

		logger.WithField("query", ctx.Request.URL.RawQuery).Infof("GET /extensions/api/v1/trace %v", ctx.Request.URL.Query())

		if code, err := server.handleTrace(ctx, metric); err != nil {
			logger.WithError(err).Error()
			ctx.Status(code)
			_, _ = ctx.Writer.WriteString(err.Error())
			ctx.Abort()
		}
	})

	return nil
}

func (server *server) Start(ctx context.Context) error { return nil }

func (server *server) Close(ctx context.Context) error { return nil }

func (server *server) handleTrace(ctx *gin.Context, metric *requestMetric) (code int, err error) {
	query := traceQuery{}
	err = ctx.BindQuery(&query)
	if err != nil {
		metric.Error = metrics.MakeLabeledError("InvalidParam")
		return 400, fmt.Errorf("invalid param %w", err)
	}

	if query.DisplayMode == "" {
		query.DisplayMode = "tracing"
	}

	trace, listParams, code, err := server.findTrace(metric, query.DisplayMode, query)
	if err != nil {
		return code, err
	}

	hasLogs := false
	for _, span := range trace.Spans {
		if len(span.Logs) > 0 {
			hasLogs = true
		}
	}
	if !hasLogs && len(trace.Spans) > 0 {
		trace, err = server.SpanReader.GetTrace(context.Background(), spanstore.GetTraceParameters{
			TraceID:   trace.Spans[0].TraceID,
			StartTime: listParams.StartTimeMin,
			EndTime:   listParams.StartTimeMax,
		})
		if err != nil {
			metric.Error = metrics.MakeLabeledError("TraceError")
			return 500, fmt.Errorf("failed to find trace ids %w", err)
		}
	}

	pruneTrace(trace, query.SpanType)

	uiTrace := uiconv.FromDomain(trace)
	ctx.JSON(200, uiTrace)
	return 0, nil
}

func pruneTrace(trace *model.Trace, spanType string) {
	if len(spanType) == 0 {
		return
	}

	for _, span := range trace.Spans {
		var newLogs []model.Log
		for _, log := range span.Logs {
			_, ok := model.KeyValues(log.Fields).FindByKey(spanType)
			if ok {
				newLogs = append(newLogs, log)
			}
		}
		span.Logs = newLogs
	}
}

type traceQuery struct {
	Cluster     string `form:"cluster"`
	Resource    string `form:"resource"`
	Namespace   string `form:"namespace"`
	Name        string `form:"name"`
	Start       string `form:"start"`
	End         string `form:"end"`
	SpanType    string `form:"span_type"`
	DisplayMode string `form:"displayMode"`
}

func (server *server) findTrace(
	metric *requestMetric,
	serviceName string,
	query traceQuery,
) (trace *model.Trace, _listParams *spanstore.TraceQueryParameters, code int, err error) {
	cluster := query.Cluster
	resource := query.Resource
	namespace := query.Namespace
	name := query.Name

	if len(cluster) == 0 || len(resource) == 0 || len(name) == 0 {
		metric.Error = metrics.MakeLabeledError("EmptyParam")
		return nil, nil, 400, fmt.Errorf("cluster or resource or name is empty")
	}

	var hasCluster bool
	for _, knownCluster := range server.ClusterList.List() {
		if strings.EqualFold(strings.ToLower(knownCluster), strings.ToLower(cluster)) {
			hasCluster = true
		}
	}
	if !hasCluster {
		metric.Error = metrics.MakeLabeledError("UnknownCluster")
		return nil, nil, 404, fmt.Errorf("cluster %s not supported now", cluster)
	}

	startTimestamp, err := time.Parse(time.RFC3339, query.Start)
	if err != nil {
		metric.Error = metrics.MakeLabeledError("InvalidTimestamp")
		return nil, nil, 400, fmt.Errorf("invalid timestamp for start param %w", err)
	}

	endTimestamp, err := time.Parse(time.RFC3339, query.End)
	if err != nil {
		metric.Error = metrics.MakeLabeledError("InvalidTimestamp")
		return nil, nil, 400, fmt.Errorf("invalid timestamp for end param %w", err)
	}

	tags := map[string]string{
		"resource": resource,
		"name":     name,
	}
	if namespace != "" {
		tags["namespace"] = namespace
	}

	parameters := &spanstore.TraceQueryParameters{
		ServiceName:   serviceName,
		OperationName: cluster,
		Tags:          tags,
		StartTimeMin:  startTimestamp,
		StartTimeMax:  endTimestamp,
		NumTraces:     20,
	}
	traces, err := server.SpanReader.FindTraces(context.Background(), parameters)
	if err != nil {
		metric.Error = metrics.MakeLabeledError("TraceError")
		return nil, nil, 500, fmt.Errorf("failed to find trace ids %w", err)
	}

	if len(traces) > 1 {
		metric.Error = metrics.MakeLabeledError("MultiTraceMatch")
		return nil, nil, 500, fmt.Errorf("trace ids match query length is %d, not 1", len(traces))
	}
	if len(traces) == 0 {
		metric.Error = metrics.MakeLabeledError("NoTraceMatch")
		return nil, nil, 404, fmt.Errorf("could not find trace ids that match query")
	}

	return traces[0], parameters, 200, nil
}
