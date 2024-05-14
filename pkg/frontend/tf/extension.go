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

package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jaegertracing/jaeger/model"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	"github.com/kubewharf/kelemetry/pkg/frontend/tracecache"
	utilobject "github.com/kubewharf/kelemetry/pkg/util/object"
	"github.com/kubewharf/kelemetry/pkg/util/semaphore"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type ExtensionProcessor interface {
	ProcessExtensions(
		ctx context.Context,
		transformer *Transformer,
		extensions []extension.Provider,
		spans []*model.Span,
		start, end time.Time,
	) ([]*model.Span, error)
}

type FetchExtensionsAndStoreCache struct {
	Cache []tracecache.ExtensionCache
}

var _ ExtensionProcessor = &FetchExtensionsAndStoreCache{}

func (x *FetchExtensionsAndStoreCache) ProcessExtensions(
	ctx context.Context,
	transformer *Transformer,
	extensions []extension.Provider,
	spans []*model.Span,
	start, end time.Time,
) ([]*model.Span, error) {
	var mutex sync.Mutex

	newSpans := []*model.Span{}

	addNewSpans := func(extension extension.Provider, result *extension.FetchResult, under *model.Span) error {
		if result == nil || len(result.Spans) == 0 {
			return nil
		}

		identifierJson, err := json.Marshal(result.Identifier)
		if err != nil {
			return fmt.Errorf("encode extension trace cache identifier: %w", err)
		}

		mutex.Lock()
		defer mutex.Unlock()

		x.Cache = append(x.Cache, tracecache.ExtensionCache{
			ParentTrace:      under.TraceID,
			ParentSpan:       under.SpanID,
			ProviderKind:     extension.Kind(),
			ProviderConfig:   json.RawMessage(extension.RawConfig()),
			CachedIdentifier: json.RawMessage(identifierJson),
		})

		setRootParent(result.Spans, under.TraceID, under.SpanID)
		setTraceId(result.Spans, under.TraceID)

		newSpans = append(newSpans, result.Spans...)

		return nil
	}

	extensionSemaphores := make([]*semaphore.Semaphore, len(extensions))
	for extId, extension := range extensions {
		extensionSemaphores[extId] = semaphore.New(extension.MaxConcurrency())
	}

	for _, span := range spans {
		span := span

		tags := model.KeyValues(span.Tags)
		if tag, isPseudo := tags.FindByKey(zconstants.PseudoType); isPseudo && tag.VStr == string(zconstants.PseudoTypeObject) {
			for extId, ext := range extensions {
				ext := ext

				objectRef, ok := objectRefFromTags(tags)
				if !ok {
					return nil, fmt.Errorf("expected object tags in nestingLevel=object spans")
				}

				extensionSemaphores[extId].Schedule(func(ctx context.Context) (semaphore.Publish, error) {
					result, err := ext.FetchForObject(ctx, objectRef, tags, start, end)
					if err != nil {
						return nil, fmt.Errorf("fetch extension trace for object: %w", err)
					}

					return func() error { return addNewSpans(ext, result, span) }, nil
				})
			}
		} else if tag, exists := tags.FindByKey(zconstants.TraceSource); exists && tag.VStr == zconstants.TraceSourceAudit {
			for extId, ext := range extensions {
				ext := ext

				objectRef, ok := objectRefFromTags(tags)
				if !ok {
					return nil, fmt.Errorf("expected object tags in traceSource=audit spans")
				}

				resourceVersion, ok := tags.FindByKey("resourceVersion")
				if !ok {
					return nil, fmt.Errorf("expected resourceVersion tag in traceSource=audit spans")
				}

				extensionSemaphores[extId].Schedule(func(ctx context.Context) (semaphore.Publish, error) {
					result, err := ext.FetchForVersion(ctx, objectRef, resourceVersion.VStr, tags, start, end)
					if err != nil {
						return nil, fmt.Errorf("fetch extension trace for object: %w", err)
					}

					return func() error { return addNewSpans(ext, result, span) }, nil
				})
			}
		}
	}

	fullSem := semaphore.NewUnbounded()
	for extId := range extensionSemaphores {
		extId := extId

		fullSem.Schedule(func(ctx context.Context) (semaphore.Publish, error) {
			semRunCtx, cancelFunc := context.WithTimeout(ctx, extensions[extId].TotalTimeout())
			defer cancelFunc()

			err := extensionSemaphores[extId].Run(semRunCtx)
			if err != nil {
				if errors.Is(err, semRunCtx.Err()) {
					transformer.Logger.WithField("extension", extensions[extId].Kind()).
						Debug("context timed out, dropping error silently to carry results obtained before timeout")
				} else {
					return nil, err
				}
			}

			return nil, nil
		})
	}

	err := fullSem.Run(ctx)
	if err != nil {
		return nil, err
	}

	return newSpans, nil
}

func objectRefFromTags(tags model.KeyValues) (utilobject.VersionedKey, bool) {
	var object utilobject.VersionedKey

	assign := func(name string, field *string) bool {
		value, ok := tags.FindByKey(name)
		if ok && value.VType == model.StringType {
			*field = value.VStr
			return true
		}
		return false
	}

	ok := assign("cluster", &object.Cluster) &&
		assign("group", &object.Group) &&
		assign("version", &object.Version) &&
		assign("resource", &object.Resource) &&
		assign("namespace", &object.Namespace) &&
		assign("name", &object.Name)

	return object, ok
}

type LoadExtensionCache struct {
	Cache []tracecache.ExtensionCache
}

var _ ExtensionProcessor = &LoadExtensionCache{}

func (x *LoadExtensionCache) ProcessExtensions(
	ctx context.Context,
	transformer *Transformer,
	extensions []extension.Provider,
	spans []*model.Span,
	start, end time.Time,
) ([]*model.Span, error) {
	newSpans := []*model.Span{}

	for _, cache := range x.Cache {
		factory, hasFactory := transformer.ExtensionFactory.Indexed[cache.ProviderKind]
		if !hasFactory {
			return nil, fmt.Errorf("no factory for provider kind %q", cache.ProviderKind)
		}

		provider, err := factory.Configure(cache.ProviderConfig)
		if err != nil {
			return nil, fmt.Errorf("invalid provider factory: %w", err)
		}

		resultSpans, err := provider.LoadCache(ctx, []byte(cache.CachedIdentifier))
		if err != nil {
			return nil, fmt.Errorf("cannot load trace from cached identifier: %w", err)
		}

		if resultSpans != nil {
			setTraceId(resultSpans, spans[0].TraceID)
			setRootParent(resultSpans, cache.ParentTrace, cache.ParentSpan)

			newSpans = append(newSpans, resultSpans...)
		}
	}

	return newSpans, nil
}

func setTraceId(spans []*model.Span, traceId model.TraceID) {
	for _, span := range spans {
		span.TraceID = traceId

		for i := range span.References {
			span.References[i].TraceID = traceId
		}
	}
}

func setRootParent(spans []*model.Span, parentTrace model.TraceID, parentSpan model.SpanID) {
	for _, span := range spans {
		if len(span.References) == 0 {
			span.References = []model.SpanRef{
				model.NewChildOfRef(parentTrace, parentSpan),
			}
		}
	}
}
