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

package httptrace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/template"
	"time"

	"github.com/jaegertracing/jaeger/model"
	"github.com/sirupsen/logrus"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util"
	filterutil "github.com/kubewharf/kelemetry/pkg/util/filter"
)

func init() {
	manager.Global.ProvideListImpl("frontend-extension/http-trace",
		manager.Ptr(&NodeTraceFactory{}), &manager.List[extension.ProviderFactory]{})
}

const extensionKind = "HTTPTrace"

type NodeTraceFactory struct {
	manager.BaseComponent
	Logger logrus.FieldLogger
}

func (*NodeTraceFactory) ListIndex() string { return extensionKind }

func (s *NodeTraceFactory) Configure(jsonBuf []byte) (extension.Provider, error) {
	providerArgs := &ProviderArgs{}
	if err := json.Unmarshal(jsonBuf, providerArgs); err != nil {
		return nil, fmt.Errorf("cannot parse httptrace config: %w", err)
	}

	for i, traceArg := range providerArgs.TraceBackends {
		argsTemplates := map[string]*template.Template{}
		for tagKey, templateString := range traceArg.ArgsTemplates {
			t, err := template.New(tagKey).Funcs(templateFuncs()).Parse(templateString)
			if err != nil {
				return nil, fmt.Errorf("invalid tag template %q: %w", tagKey, err)
			}
			argsTemplates[tagKey] = t
		}
		urlTemplate, err := template.New("url").Funcs(templateFuncs()).Parse(traceArg.URLTemplate)
		if err != nil {
			return nil, fmt.Errorf("invalid url template %q: %w", traceArg.URLTemplate, err)
		}
		providerArgs.TraceBackends[i].urlTemplate = urlTemplate
		providerArgs.TraceBackends[i].argsTemplates = argsTemplates
	}

	totalTimeout, err := time.ParseDuration(providerArgs.TotalTimeout)
	if err != nil {
		return nil, fmt.Errorf("parse totalTimeout error: %w", err)
	}

	return &Provider{
		logger:         s.Logger,
		service:        providerArgs.Service,
		operation:      providerArgs.Operation,
		traceBackends:  providerArgs.TraceBackends,
		totalTimeout:   totalTimeout,
		maxConcurrency: providerArgs.MaxConcurrency,
		rawConfig:      jsonBuf,
	}, nil
}

type ProviderArgs struct {
	Service   string `json:"service"`
	Operation string `json:"operation"`

	TraceBackends []TraceBackend `json:"traceBackends"`

	TotalTimeout   string `json:"totalTimeout"`
	MaxConcurrency int    `json:"maxConcurrency"`
}

type TraceBackend struct {
	TagFilters    filterutil.TagFilters `json:"tagFilters"`
	ArgsTemplates map[string]string     `json:"argsTemplates"`
	URLTemplate   string                `json:"urlTemplate"`

	ForObject     bool `json:"forObject"`
	ForAuditEvent bool `json:"forAuditEvent"`

	urlTemplate   *template.Template
	argsTemplates map[string]*template.Template
}

type Provider struct {
	logger         logrus.FieldLogger
	service        string
	operation      string
	traceBackends  []TraceBackend
	totalTimeout   time.Duration
	maxConcurrency int
	rawConfig      []byte
}

func (provider *Provider) Kind() string { return extensionKind }

func (provider *Provider) RawConfig() []byte { return provider.rawConfig }

func (provider *Provider) TotalTimeout() time.Duration { return provider.totalTimeout }
func (provider *Provider) MaxConcurrency() int         { return provider.maxConcurrency }

func (provider *Provider) FetchForObject(
	ctx context.Context,
	object util.ObjectRef,
	mainTags model.KeyValues,
	start, end time.Time,
) (*extension.FetchResult, error) {
	var backends []TraceBackend
	for _, b := range provider.traceBackends {
		if b.ForObject {
			backends = append(backends, b)
		}
	}
	if len(backends) == 0 {
		return nil, nil
	}

	return provider.fetch(
		ctx, object, mainTags, start, end,
		objectTemplateArgs(object),
		backends,
	)
}

func (provider *Provider) FetchForVersion(
	ctx context.Context,
	object util.ObjectRef,
	resourceVersion string,
	mainTags model.KeyValues,
	start, end time.Time,
) (*extension.FetchResult, error) {
	var backends []TraceBackend
	for _, b := range provider.traceBackends {
		if b.ForAuditEvent {
			backends = append(backends, b)
		}
	}
	if len(backends) == 0 {
		return nil, nil
	}

	templateArgs := objectTemplateArgs(object)
	templateArgs["resourceVersion"] = resourceVersion

	return provider.fetch(
		ctx, object, mainTags, start, end,
		templateArgs,
		backends,
	)
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

	var spans []*model.Span

	for _, traceId := range ident.TraceIds {
		trace, err := provider.getTrace(ctx, traceId)
		if err != nil {
			return nil, fmt.Errorf("cannot get trace from extension storage: %w", err)
		}

		spans = append(spans, trace.Spans...)
	}

	return spans, nil
}

func objectTemplateArgs(object util.ObjectRef) map[string]any {
	return map[string]any{
		"cluster":       object.Cluster,
		"group":         object.Group,
		"version":       object.Version,
		"resource":      object.Resource,
		"groupVersion":  object.GroupVersionResource.GroupVersion().String(),
		"groupResource": object.GroupVersionResource.GroupResource().String(),
		"namespace":     object.Namespace,
		"name":          object.Name,
	}
}

// fetch returns traces search from first successful traceBackend
func (provider *Provider) fetch(
	ctx context.Context,
	_ util.ObjectRef,
	mainTags model.KeyValues,
	start, end time.Time,
	templateArgs map[string]any,
	traceBackends []TraceBackend,
) (*extension.FetchResult, error) {
	for _, mainTag := range mainTags {
		templateArgs[mainTag.Key] = mainTag.Value()
	}

	templateArgs["start"] = start
	templateArgs["end"] = end

	stringTags := map[string]string{}
	for key, val := range templateArgs {
		if strVal, ok := val.(string); ok {
			stringTags[key] = strVal
		}
	}

	for _, traceBackend := range traceBackends {
		if !traceBackend.TagFilters.Check(stringTags) {
			continue
		}

		queryArgs := map[string]string{}
		for tagKey, tagTemplate := range traceBackend.argsTemplates {
			buf := new(bytes.Buffer)
			err := tagTemplate.Execute(buf, templateArgs)
			if err != nil {
				return nil, fmt.Errorf("cannot execute tag template: %w", err)
			}

			queryArgs[tagKey] = buf.String()
		}

		buf := new(bytes.Buffer)
		err := traceBackend.urlTemplate.Execute(buf, templateArgs)
		if err != nil {
			return nil, fmt.Errorf("cannot execute tag template: %w", err)
		}
		traceUrl := buf.String()

		traces, err := provider.findTraces(ctx, traceUrl, queryArgs)
		provider.logger.
			WithField("url", traceUrl).
			WithField("args", queryArgs).
			WithField("startTime", start).
			WithField("endTime", end).
			WithError(err).
			Info("search node traces")
		if err != nil {
			continue
		}

		var traceIds []model.TraceID
		var spans []*model.Span
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

	return nil, nil
}

func (provider *Provider) findTraces(ctx context.Context, traceUrl string, queryArgs map[string]string) ([]*model.Trace, error) {
	query := url.Values{}
	for k, v := range queryArgs {
		query.Add(k, v)
	}
	traceUrl = fmt.Sprintf("%s?%s", traceUrl, query.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceUrl, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(res.Body)
	defer res.Body.Close()
	if res.StatusCode > 299 {
		return nil, fmt.Errorf("response failed with status code: %d and body: %s", res.StatusCode, string(body))
	}
	if err != nil {
		return nil, fmt.Errorf("read response error: %w", err)
	}

	var traces TraceResponse
	if err := json.Unmarshal(body, &traces); err != nil {
		return nil, fmt.Errorf("cannot parse HTTP extension response: %w", err)
	}

	return traces.Data, nil
}

type TraceResponse struct {
	Data []*model.Trace
}

// TODO
func (provider *Provider) getTrace(ctx context.Context, traceId model.TraceID) (*model.Trace, error) {
	return nil, fmt.Errorf("trace cache for HTTP extension is unimplemented")
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"unixMicro": unixMicro,
	}
}

func unixMicro(t interface{}) string {
	if tt, ok := t.(time.Time); ok {
		return fmt.Sprintf("%v", tt.UnixMicro())
	}
	return ""
}
