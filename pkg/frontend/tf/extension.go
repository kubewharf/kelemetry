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
	"fmt"
	"time"

	"github.com/jaegertracing/jaeger/model"

	"github.com/kubewharf/kelemetry/pkg/frontend/extension"
	"github.com/kubewharf/kelemetry/pkg/frontend/tracecache"
	"github.com/kubewharf/kelemetry/pkg/util"
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
	newSpans := []*model.Span{}

	addNewSpans := func(extension extension.Provider, result *extension.FetchResult) error {
		identifierJson, err := json.Marshal(result.Identifier)
		if err != nil {
			return fmt.Errorf("encode extension trace cache identifier: %w", err)
		}

		x.Cache = append(x.Cache, tracecache.ExtensionCache{
			ProviderKind:     extension.Kind(),
			ProviderConfig:   json.RawMessage(extension.RawConfig()),
			CachedIdentifier: json.RawMessage(identifierJson),
		})
		newSpans = append(newSpans, result.Spans...)

		return nil
	}

	for _, span := range spans {
		tags := model.KeyValues(span.Tags)
		if tag, exists := tags.FindByKey(zconstants.NestLevel); exists && tag.VStr == zconstants.NestLevelObject {
			for _, extension := range extensions {
				objectRef, ok := objectRefFromTags(tags)
				if !ok {
					return nil, fmt.Errorf("expected object tags in nestingLevel=object spans")
				}

				result, err := extension.FetchForObject(ctx, objectRef, tags, start, end)
				if err != nil {
					return nil, fmt.Errorf("fetch extension trace for object: %w", err)
				}

				if result != nil {
					addNewSpans(extension, result)
				}
			}
		} else if tag, exists := tags.FindByKey(zconstants.TraceSource); exists && tag.VStr == zconstants.TraceSourceAudit {
			for _, extension := range extensions {
				objectRef, ok := objectRefFromTags(tags)
				if !ok {
					return nil, fmt.Errorf("expected object tags in traceSource=audit spans")
				}

				resourceVersion, ok := tags.FindByKey("resourceVersion")
				if !ok {
					return nil, fmt.Errorf("expected resourceVersion tag in traceSource=audit spans")
				}

				result, err := extension.FetchForVersion(ctx, objectRef, resourceVersion.VStr, tags, start, end)
				if err != nil {
					return nil, fmt.Errorf("fetch extension trace for object: %w", err)
				}

				if result != nil {
					addNewSpans(extension, result)
				}
			}
		}
	}

	return newSpans, nil
}

func objectRefFromTags(tags model.KeyValues) (util.ObjectRef, bool) {
	var object util.ObjectRef

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
			setRootParent(resultSpans, cache.ParentTrace, cache.ParentSpan)

			newSpans = append(newSpans, resultSpans...)
		}
	}

	return newSpans, nil
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
