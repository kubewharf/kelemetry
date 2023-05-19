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

package defaulttags

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/kelemetry/pkg/audit"
	"github.com/kubewharf/kelemetry/pkg/kelemetrix"
	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("kelemetrix-default-tags", manager.Ptr(&DefaultTagProvider{}))
}

type DefaultTag struct {
	Name   string
	Mapper func(*audit.Message) (string, error)
}

var DefaultTags = []DefaultTag{
	// server related
	{Name: "cluster", Mapper: func(m *audit.Message) (string, error) { return m.Cluster, nil }},
	{Name: "apiserverAddr", Mapper: func(m *audit.Message) (string, error) { return m.ApiserverAddr, nil }},

	// client related
	{Name: "username", Mapper: func(m *audit.Message) (string, error) {
		username := m.User.Username
		if m.ImpersonatedUser != nil {
			username = m.ImpersonatedUser.Username
		}
		if strings.HasPrefix(username, "system:node:") {
			return "system:node", nil
		}
		return username, nil
	}},
	{Name: "clientAddr", Mapper: func(m *audit.Message) (string, error) {
		if len(m.SourceIPs) == 0 {
			return "", nil
		}
		return m.SourceIPs[0], nil
	}},

	// request related
	{Name: "verb", Mapper: func(m *audit.Message) (string, error) { return m.Verb, nil }},
	{Name: "code", Mapper: func(m *audit.Message) (string, error) {
		if m.ResponseStatus == nil {
			return "", nil
		}
		return fmt.Sprint(m.ResponseStatus.Code), nil
	}},
	{Name: "errorReason", Mapper: func(m *audit.Message) (string, error) {
		if m.ResponseStatus == nil {
			return "", nil
		}

		return string(m.ResponseStatus.Reason), nil
	}},
	{Name: "resourceVersion", Mapper: func(m *audit.Message) (string, error) {
		url, err := url.Parse(m.RequestURI)
		if err != nil {
			return "kelemetrix::ErrUriParse", fmt.Errorf("cannot parse request URI: %w", err)
		}

		rv := url.Query()["resourceVersion"]
		if len(rv) == 0 {
			return "Empty", nil
		}

		if rv[0] == "0" {
			return "Zero", nil
		}

		return "NonZero", nil
	}},
	{Name: "hasSelector", Mapper: func(m *audit.Message) (string, error) {
		url, err := url.Parse(m.RequestURI)
		if err != nil {
			return "kelemetrix::ErrUriParse", fmt.Errorf("cannot parse request URI: %w", err)
		}

		query := url.Query()
		if query.Has("labelSelector") || query.Has("fieldSelector") {
			return "true", nil
		}

		return "false", nil
	}},

	// object related
	{Name: "group", Mapper: func(m *audit.Message) (string, error) {
		if m.ObjectRef == nil {
			return "", nil
		}
		return m.ObjectRef.APIGroup, nil
	}},
	{Name: "version", Mapper: func(m *audit.Message) (string, error) {
		if m.ObjectRef == nil {
			return "", nil
		}
		return m.ObjectRef.APIVersion, nil
	}},
	{Name: "resource", Mapper: func(m *audit.Message) (string, error) {
		if m.ObjectRef == nil {
			return "", nil
		}
		return m.ObjectRef.Resource, nil
	}},
	{Name: "subresource", Mapper: func(m *audit.Message) (string, error) {
		if m.ObjectRef == nil {
			return "", nil
		}
		return m.ObjectRef.Subresource, nil
	}},
	{Name: "namespace", Mapper: func(m *audit.Message) (string, error) {
		if m.ObjectRef == nil {
			return "", nil
		}
		return m.ObjectRef.Namespace, nil
	}},
	{Name: "name", Mapper: func(m *audit.Message) (string, error) {
		if m.ObjectRef == nil {
			return "", nil
		}
		return m.ObjectRef.Name, nil
	}},
}

type Options struct{}

func (options *Options) Setup(fs *pflag.FlagSet) {}

func (options *Options) EnableFlag() *bool { return pointer.Bool(true) }

type DefaultTagProvider struct {
	options  Options
	Logger   logrus.FieldLogger
	Registry *kelemetrix.Registry
}

func (tp *DefaultTagProvider) Options() manager.Options { return &tp.options }

func (tp *DefaultTagProvider) Init() error {
	for _, tag := range DefaultTags {
		tp.Registry.AddTagProvider(&provider{tag: tag, logger: tp.Logger.WithField("tagProvider", tag.Name)})
	}

	return nil
}

func (tp *DefaultTagProvider) Start(ctx context.Context) error { return nil }
func (tp *DefaultTagProvider) Close(ctx context.Context) error { return nil }

type provider struct {
	tag    DefaultTag
	logger logrus.FieldLogger
}

func (p *provider) TagNames() []string { return []string{p.tag.Name} }

func (p *provider) ProvideValues(message *audit.Message, slice []string) {
	if value, err := p.tag.Mapper(message); err != nil {
		p.logger.WithError(err).Error("error generating tag")
		if strings.HasPrefix(value, "kelemetrix::Err") {
			slice[0] = value
		} else {
			slice[0] = "kelemetrix::ErrUnknown"
		}
	} else {
		slice[0] = value
	}
}
