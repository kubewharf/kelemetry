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

package extensionjaeger

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"text/template"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/pkg/metrics"
	jaegerstorage "github.com/jaegertracing/jaeger/plugin/storage"
	"github.com/jaegertracing/jaeger/storage/spanstore"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	"github.com/kubewharf/kelemetry/pkg/manager"
	utiljaeger "github.com/kubewharf/kelemetry/pkg/util/jaeger"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
)

func init() {
	manager.Global.ProvideListImpl("frontend-extension/jaeger-storage", manager.Ptr(&Storage{}), &manager.List[extension.ProviderFactory]{})
}

const jaegerStorageKind = "JaegerStorage"

type Storage struct {
	manager.BaseComponent
	Logger logrus.FieldLogger
}

func (*Storage) ListIndex() string { return jaegerStorageKind }

func getViper() (*viper.Viper, error) {
	factoryConfig := jaegerstorage.FactoryConfig{
		SpanWriterTypes:         utiljaeger.SpanStorageTypesToAddFlag,
		SpanReaderType:          "elasticsearch",
		DependenciesStorageType: "elasticsearch",
	}
	storageFactory, err := jaegerstorage.NewFactory(factoryConfig)
	if err != nil {
		return nil, fmt.Errorf("Cannot initialize storage factory: %w", err)
	}

	viper := viper.New()

	goFs := &flag.FlagSet{}
	storageFactory.AddFlags(goFs)
	pfs := &pflag.FlagSet{}
	pfs.AddGoFlagSet(goFs)
	if err := viper.BindPFlags(pfs); err != nil {
		return nil, fmt.Errorf("cannot bind extension storage factory flags: %w", err)
	}

	return viper, nil
}

const DefaultNumTracesLimit = 5

func (s *Storage) Configure(jsonBuf []byte) (extension.Provider, error) {
	providerArgs := &ProviderArgs{NumTracesLimit: DefaultNumTracesLimit}
	if err := json.Unmarshal(jsonBuf, providerArgs); err != nil {
		return nil, fmt.Errorf("cannot parse jaeger-storage config: %w", err)
	}

	viper, err := getViper()
	if err != nil {
		return nil, err
	}

	for argKey, argValue := range providerArgs.StorageArgs {
		viper.Set(argKey, argValue)
	}

	storageType := viper.GetString("span-storage.type")
	if len(storageType) == 0 {
		return nil, fmt.Errorf("span-storage.type must be specified for jaeger-storage extension")
	}

	factoryConfig := jaegerstorage.FactoryConfig{
		SpanReaderType:          storageType,
		SpanWriterTypes:         []string{storageType},
		DependenciesStorageType: storageType,
	}

	storageFactory, err := jaegerstorage.NewFactory(factoryConfig)
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate extension storage factory: %w", err)
	}

	zapLogger := zap.NewExample()

	storageFactory.InitFromViper(viper, zapLogger)
	if err := storageFactory.Initialize(metrics.NullFactory, zapLogger); err != nil {
		return nil, fmt.Errorf("cannot initialize extension storage: %w", err)
	}

	reader, err := storageFactory.CreateSpanReader()
	if err != nil {
		return nil, fmt.Errorf("cannot create span reader for extension storage: %w", err)
	}

	tagTemplates := map[string]*template.Template{}
	for tagKey, templateString := range providerArgs.TagTemplates {
		t, err := template.New(tagKey).Parse(templateString)
		if err != nil {
			return nil, fmt.Errorf("invalid tag template %q: %w", tagKey, err)
		}
		tagTemplates[tagKey] = t
	}

	totalTimeout, err := time.ParseDuration(providerArgs.TotalTimeout)
	if err != nil {
		return nil, fmt.Errorf("parse totalTimeout error: %w", err)
	}

	return &Provider{
		reader: reader,
		logger: s.Logger,

		service:      providerArgs.Service,
		operation:    providerArgs.Operation,
		tagTemplates: tagTemplates,
		numTraces:    providerArgs.NumTracesLimit,

		forObject:     providerArgs.ForObject,
		forAuditEvent: providerArgs.ForAuditEvent,

		totalTimeout:   totalTimeout,
		maxConcurrency: providerArgs.MaxConcurrency,

		rawConfig: jsonBuf,
	}, nil
}

type ProviderArgs struct {
	StorageArgs map[string]string `json:"storageArgs"`

	Service        string            `json:"service"`
	Operation      string            `json:"operation"`
	TagTemplates   map[string]string `json:"tagTemplates"`
	NumTracesLimit int               `json:"numTracesLimit"`

	ForObject     bool `json:"forObject"`
	ForAuditEvent bool `json:"forAuditEvent"`

	TotalTimeout   string `json:"totalTimeout"`
	MaxConcurrency int    `json:"maxConcurrency"`
}

type Provider struct {
	reader spanstore.Reader
	logger logrus.FieldLogger

	service      string
	operation    string
	tagTemplates map[string]*template.Template
	numTraces    int

	forObject     bool
	forAuditEvent bool

	totalTimeout   time.Duration
	maxConcurrency int

	rawConfig []byte
}

func (provider *Provider) Kind() string { return jaegerStorageKind }

func (provider *Provider) RawConfig() []byte { return provider.rawConfig }

func (provider *Provider) TotalTimeout() time.Duration { return provider.totalTimeout }
func (provider *Provider) MaxConcurrency() int         { return provider.maxConcurrency }

func (provider *Provider) FetchForObject(
	ctx context.Context,
	object utilobject.Rich,
	mainTags model.KeyValues,
	start, end time.Time,
) (*extension.FetchResult, error) {
	if !provider.forObject {
		return nil, nil
	}

	return provider.fetch(
		ctx, mainTags, start, end,
		objectTemplateArgs(object),
	)
}

func (provider *Provider) FetchForVersion(
	ctx context.Context,
	object utilobject.Rich,
	resourceVersion string,
	mainTags model.KeyValues,
	start, end time.Time,
) (*extension.FetchResult, error) {
	if !provider.forAuditEvent {
		return nil, nil
	}

	templateArgs := objectTemplateArgs(object)
	templateArgs["resourceVersion"] = resourceVersion

	return provider.fetch(
		ctx, mainTags, start, end,
		templateArgs,
	)
}

func objectTemplateArgs(object utilobject.Rich) map[string]any {
	var objectApiPath string
	if object.Group == "" {
		objectApiPath = fmt.Sprintf("/api/%s", object.Version)
	} else {
		objectApiPath = fmt.Sprintf("/apis/%s/%s", object.Group, object.Version)
	}

	if object.Namespace != "" && object.Resource != "namespaces" {
		objectApiPath += fmt.Sprintf("/namespaces/%s", object.Namespace)
	}

	objectApiPath += fmt.Sprintf("/%s/%s", object.Resource, object.Name)

	return map[string]any{
		"cluster":       object.Cluster,
		"group":         object.Group,
		"version":       object.Version,
		"resource":      object.Resource,
		"groupVersion":  object.GroupVersion().String(),
		"groupResource": object.GroupResource().String(),
		"namespace":     object.Namespace,
		"name":          object.Name,
		"objectApiPath": objectApiPath,
	}
}

func (provider *Provider) fetch(
	ctx context.Context,
	mainTags model.KeyValues,
	start, end time.Time,
	templateArgs map[string]any,
) (*extension.FetchResult, error) {
	queryTags := map[string]string{}

	for _, mainTag := range mainTags {
		templateArgs[mainTag.Key] = mainTag.Value()
	}

	for tagKey, tagTemplate := range provider.tagTemplates {
		buf := new(bytes.Buffer)
		err := tagTemplate.Execute(buf, templateArgs)
		if err != nil {
			return nil, fmt.Errorf("cannot execute tag template: %w", err)
		}

		queryTags[tagKey] = buf.String()
	}

	provider.logger.
		WithField("service", provider.service).
		WithField("operation", provider.operation).
		WithField("tags", queryTags).
		WithField("startTime", start).
		WithField("endTime", end).
		Info("search extension traces")

	traces, err := provider.reader.FindTraces(ctx, &spanstore.TraceQueryParameters{
		ServiceName:   provider.service,
		OperationName: provider.operation,
		Tags:          queryTags,
		StartTimeMin:  start,
		StartTimeMax:  end,
		NumTraces:     provider.numTraces,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot find traces: %w", err)
	}

	traceIds := []model.TraceID{}
	spans := []*model.Span{}

	for _, trace := range traces {
		if len(trace.Spans) == 0 {
			continue
		}

		traceIds = append(traceIds, trace.Spans[0].TraceID)
		spans = append(spans, trace.Spans...)
	}

	return &extension.FetchResult{
		Identifier: identifier{
			TraceIds: traceIds,
			Start:    start,
			End:      end,
		},
		Spans: spans,
	}, nil
}

type identifier struct {
	TraceIds []model.TraceID `json:"traceIds"`
	Start    time.Time       `json:"start"`
	End      time.Time       `json:"end"`
}

func (provider *Provider) LoadCache(ctx context.Context, jsonBuf []byte) ([]*model.Span, error) {
	var ident identifier
	if err := json.Unmarshal(jsonBuf, &ident); err != nil {
		return nil, fmt.Errorf("cannot unmarshal cached identifier: %w", err)
	}

	spans := []*model.Span{}

	for _, traceId := range ident.TraceIds {
		trace, err := provider.reader.GetTrace(ctx, traceId)
		if err != nil {
			return nil, fmt.Errorf("cannot get trace from extension storage: %w", err)
		}

		spans = append(spans, trace.Spans...)
	}

	return spans, nil
}
