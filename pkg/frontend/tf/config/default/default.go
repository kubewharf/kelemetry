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

package tfconfigdefault

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	tfconfig "github.com/kubewharf/kelemetry/pkg/frontend/tf/config"
	tfstep "github.com/kubewharf/kelemetry/pkg/frontend/tf/step"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.ProvideMuxImpl("jaeger-transform-config/default", manager.Ptr(&DefaultProvider{
		configs:        make(map[tfconfig.Id]*tfconfig.Config),
		nameToConfigId: make(map[string]tfconfig.Id),
	}), tfconfig.Provider.DefaultId)
}

type options struct {
	resourceTagsToCollect []string
}

func (options *options) Setup(fs *pflag.FlagSet) {
	fs.StringSliceVar(
		&options.resourceTagsToCollect,
		"jaeger-transform-collect-object-tags",
		[]string{"nodes"},
		"add these tags to object span if it's children spans have these tags. e.g. 'nodes, apiservices.apiregistration.k8s.io'")
}

func (options *options) EnableFlag() *bool { return nil }

type DefaultProvider struct {
	manager.MuxImplBase

	options        options
	configs        map[tfconfig.Id]*tfconfig.Config
	nameToConfigId map[string]tfconfig.Id
	defaultConfig  tfconfig.Id
}

var (
	_ manager.Component = &DefaultProvider{}
	_ tfconfig.Provider = &DefaultProvider{}
)

func (p *DefaultProvider) MuxImplName() (name string, isDefault bool) { return "default", true }

func (p *DefaultProvider) Options() manager.Options { return &p.options }

func (p *DefaultProvider) Init() error {
	p.registerDefaults()
	return nil
}

func (p *DefaultProvider) Register(config *tfconfig.Config) {
	p.configs[config.Id] = config
	p.nameToConfigId[config.Name] = config.Id
}

func (p *DefaultProvider) registerDefaults() {
	p.defaultConfig = 0x20000000

	p.Register(&tfconfig.Config{
		Id:   0x0000_0000,
		Name: "tree",
		Steps: []tfconfig.Step{
			p.getInitialSteps(),
			p.getFinalSteps(),
		},
	})
	p.Register(&tfconfig.Config{
		Id:   0x1000_0000,
		Name: "timeline",
		Steps: []tfconfig.Step{
			p.getInitialSteps(),
			tfconfig.VisitorStep{Visitor: tfstep.ExtractNestingVisitor{
				MatchesNestLevel: func(level string) bool {
					return true
				},
			}},
			p.getFinalSteps(),
		},
	})
	p.Register(&tfconfig.Config{
		Id:   0x2000_0000,
		Name: "tracing",
		Steps: []tfconfig.Step{
			p.getInitialSteps(),
			tfconfig.VisitorStep{Visitor: tfstep.ExtractNestingVisitor{
				MatchesNestLevel: func(level string) bool {
					return level != zconstants.NestLevelObject
				},
			}},
			getCollapseStep(),
			tfconfig.VisitorStep{Visitor: tfstep.CompactDurationVisitor{}},
			p.getFinalSteps(),
		},
	})
	p.Register(&tfconfig.Config{
		Id:   0x3000_0000,
		Name: "grouped",
		Steps: []tfconfig.Step{
			p.getInitialSteps(),
			tfconfig.VisitorStep{Visitor: tfstep.ExtractNestingVisitor{
				MatchesNestLevel: func(level string) bool {
					return level != zconstants.NestLevelObject
				},
			}},
			getCollapseStep(),
			tfconfig.VisitorStep{Visitor: tfstep.GroupByTraceSourceVisitor{
				ShouldBeGrouped: func(traceSource string) bool {
					// events are more user-friendly, so let's promote them
					return traceSource != zconstants.TraceSourceEvent
				},
			}},
			tfconfig.VisitorStep{Visitor: tfstep.CompactDurationVisitor{}},
			p.getFinalSteps(),
		},
	})

	exclusiveConfigs := make([]*tfconfig.Config, 0, len(p.configs))
	for _, config := range p.configs {
		stepsCopy := make([]tfconfig.Step, len(config.Steps))
		copy(stepsCopy, config.Steps)

		exclusiveConfigs = append(exclusiveConfigs, &tfconfig.Config{
			Id:         config.Id | 0x0100_0000,
			Name:       fmt.Sprintf("%s (exclusive)", config.Name),
			UseSubtree: true,
			Steps:      stepsCopy,
		})
	}

	for _, config := range exclusiveConfigs {
		p.Register(config)
	}
}

func getCollapseStep() tfconfig.Step {
	return tfconfig.VisitorStep{Visitor: tfstep.CollapseNestingVisitor{
		ShouldCollapse: func(traceSource string) bool { return true },
		TagMappings: map[string][]tfstep.TagMapping{
			zconstants.TraceSourceAudit: {
				{FromSpanTag: "userAgent", ToLogField: "userAgent"},
				{FromSpanTag: "sourceIP", ToLogField: "sourceIP"},
			},
			zconstants.TraceSourceEvent: {
				{FromSpanTag: "action", ToLogField: "action"},
				{FromSpanTag: "source", ToLogField: "source"},
			},
		},
		AuditDiffClasses: tfstep.NewAuditDiffClassification(tfstep.AuditDiffClass{
			ShouldDisplay: true,
			Name:          "diff",
			Priority:      0,
		}).AddClass(tfstep.AuditDiffClass{
			ShouldDisplay: true,
			Name:          "verbose diff",
			Priority:      10,
		}, []string{
			"metadata.resourceVersion",
			"metadata.generation",
			"metadata.annotations.latest-update",
			"metadata.annotations.tce.kubernetes.io/lastUpdate",
		}),
		LogTypeMapping: map[zconstants.LogType]string{
			zconstants.LogTypeEventMessage:   "message",
			zconstants.LogTypeObjectSnapshot: "snapshot",
			zconstants.LogTypeRealError:      "error",
			zconstants.LogTypeRealVerbose:    "",
			zconstants.LogTypeKelemetryError: "_debug",
		},
	}}
}

func (p *DefaultProvider) getInitialSteps() tfconfig.Step {
	return tfconfig.BatchStep{
		Steps: []tfconfig.Step{
			tfconfig.VisitorStep{Visitor: tfstep.PruneChildlessVisitor{}},
			tfconfig.VisitorStep{Visitor: tfstep.ReplaceNameVisitor{}},
			tfconfig.VisitorStep{Visitor: tfstep.ObjectTagsVisitor{
				ResourceTags: p.options.resourceTagsToCollect,
			}},
			tfconfig.VisitorStep{Visitor: tfstep.ServiceOperationReplaceVisitor{
				TraceSource: zconstants.TraceSourceObject,
				Dest:        tfstep.ReplaceDestService,
				Source:      []string{"resource"},
			}},
			tfconfig.VisitorStep{Visitor: tfstep.ServiceOperationReplaceVisitor{
				TraceSource: zconstants.TraceSourceObject,
				Dest:        tfstep.ReplaceDestOperation,
				Source:      []string{"namespace", "name"},
			}},
		},
	}
}

func (p *DefaultProvider) getFinalSteps() tfconfig.Step {
	return tfconfig.BatchStep{
		Steps: []tfconfig.Step{
			tfconfig.VisitorStep{Visitor: tfstep.ClusterNameVisitor{}},
			tfconfig.VisitorStep{Visitor: tfstep.PruneTagsVisitor{}},
		},
	}
}

func (p *DefaultProvider) Start(ctx context.Context) error { return nil }

func (p *DefaultProvider) Close(ctx context.Context) error { return nil }

func (p *DefaultProvider) Names() []string {
	names := make([]string, 0, len(p.nameToConfigId))
	for name := range p.nameToConfigId {
		names = append(names, name)
	}
	return names
}

func (p *DefaultProvider) DefaultName() string {
	return p.configs[p.defaultConfig].Name
}

func (p *DefaultProvider) DefaultId() tfconfig.Id {
	return p.defaultConfig
}

func (p *DefaultProvider) GetByName(name string) *tfconfig.Config {
	id, exists := p.nameToConfigId[name]
	if !exists {
		return nil
	}
	return p.configs[id]
}

func (p *DefaultProvider) GetById(id tfconfig.Id) *tfconfig.Config {
	return p.configs[id]
}
