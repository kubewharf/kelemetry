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

package tfstep

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/jaegertracing/jaeger/model"

	tfscheme "github.com/kubewharf/kelemetry/pkg/frontend/tf/scheme"
	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/manager"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

func init() {
	manager.Global.Provide(
		"tf-step/collapse-nesting-visitor",
		manager.Ptr(&tfscheme.RegisterStep[*tfscheme.VisitorStep[CollapseNestingVisitor]]{Kind: "CollapseNestingVisitor"}),
	)
}

// Deletes child spans with a traceSource and injects them as logs in the nesting span.
//
// Multiple logs of the same span are aggregated into one log, flattening them into a field.
//
// Must be followed by PruneTagsVisitor in the last step.
type CollapseNestingVisitor struct {
	ShouldCollapse   StringFilter                  `json:"shouldCollapse"`   // tests traceSource
	TagMappings      map[string][]TagMapping       `json:"tagMappings"`      // key = traceSource
	AuditDiffClasses AuditDiffClassification       `json:"auditDiffClasses"` // key = prefix
	LogTypeMapping   map[zconstants.LogType]string `json:"logTypeMapping"`   // key = log type, value = log field
}

type TagMapping struct {
	FromSpanTag string `json:"fromSpanTag"`
	ToLogField  string `json:"toLogField"`
}

type AuditDiffClassification struct {
	SpecificFields AuditDiffFieldClasses `json:"fields"`
	DefaultClass   AuditDiffClass        `json:"default"`
}

type AuditDiffFieldClasses struct {
	fieldMap map[string]*AuditDiffClass // key = field in the form `metadata.resourceVersion`
}

func (c *AuditDiffFieldClasses) UnmarshalJSON(buf []byte) error {
	type FieldClass struct {
		Fields []string       `json:"fields"`
		Class  AuditDiffClass `json:"class"`
	}

	var raw []FieldClass

	if err := json.Unmarshal(buf, &raw); err != nil {
		return err
	}

	specific := make(map[string]*AuditDiffClass)
	for _, fc := range raw {
		class := fc.Class
		for _, field := range fc.Fields {
			specific[field] = &class
		}
	}

	c.fieldMap = specific
	return nil
}

type AuditDiffClass struct {
	ShouldDisplay bool   `json:"shouldDisplay"`
	Name          string `json:"name"`
	Priority      int    `json:"priority"`
}

func (classes *AuditDiffClassification) Get(prefix string) *AuditDiffClass {
	if class, hasSpecific := classes.SpecificFields.fieldMap[prefix]; hasSpecific {
		return class
	}

	return &classes.DefaultClass
}

func (visitor CollapseNestingVisitor) Enter(tree *tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	if _, hasTag := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel); !hasTag {
		return visitor
	}

	for childId := range tree.Children(span.SpanID) {
		visitor.processChild(tree, span, childId)
	}

	return visitor
}

func (visitor CollapseNestingVisitor) Exit(tree *tftree.SpanTree, span *model.Span) {}

func (visitor CollapseNestingVisitor) processChild(tree *tftree.SpanTree, span *model.Span, childId model.SpanID) {
	childSpan := tree.Span(childId)
	if _, childHasTag := model.KeyValues(childSpan.Tags).FindByKey(zconstants.NestLevel); childHasTag {
		return
	}
	traceSourceKv, hasTraceSource := model.KeyValues(childSpan.Tags).FindByKey(zconstants.TraceSource)
	if !hasTraceSource {
		return
	}
	traceSource := traceSourceKv.VStr
	if !visitor.ShouldCollapse.Test(traceSource) {
		return
	}

	// move information in childSpan into fields

	fields := []model.KeyValue{
		model.String(zconstants.TraceSource, traceSource),
		model.String(traceSource, childSpan.OperationName),
	}

	for _, mapping := range visitor.TagMappings[traceSource] {
		if kv, hasKey := model.KeyValues(childSpan.Tags).FindByKey(mapping.FromSpanTag); hasKey {
			// kv is a copy, so this does not mutate childSpan.Tags (which is deleted anyway)
			kv.Key = mapping.ToLogField
			if kv.AsStringLossy() != "" {
				fields = append(fields, kv)
			}
		}
	}

	diffCollector := &auditDiffCollector{
		visitor:         visitor,
		classMap:        map[string][]string{},
		classPriorities: map[string]int{},
	}
	otherLogs := []model.KeyValue{}

	for _, childLog := range childSpan.Logs {
		logTypeKv, _ := model.KeyValues(childLog.Fields).FindByKey(zconstants.LogTypeAttr)
		logType := zconstants.LogType(logTypeKv.VStr)

		// "event" is the default log key emitted by otel sdk
		eventKv, _ := model.KeyValues(childLog.Fields).FindByKey("event")
		event := eventKv.VStr

		if logType == zconstants.LogTypeObjectDiff {
			// this is an audit diff, process specially for better UX
			// TODO can this fit in a separate step instead?
			diffCollector.process(event)
		} else if fieldName, hasMapping := visitor.LogTypeMapping[logType]; hasMapping {
			otherLogs = append(otherLogs, model.String(fieldName, event))
		}
	}

	diffClassSlice := []model.KeyValue{}
	for className, class := range diffCollector.classMap {
		diffClassSlice = append(diffClassSlice, model.String(className, strings.Join(class, "\n")))
	}
	sort.Slice(diffClassSlice, func(i, j int) bool {
		iKey := diffClassSlice[i].Key
		jKey := diffClassSlice[j].Key
		return diffCollector.classPriorities[iKey] < diffCollector.classPriorities[jKey]
	})
	fields = append(fields, diffClassSlice...)

	fields = append(fields, otherLogs...)

	span.Logs = append(span.Logs, model.Log{
		Timestamp: childSpan.StartTime,
		Fields:    fields,
	})

	tree.Delete(childId)
}

type auditDiffCollector struct {
	visitor         CollapseNestingVisitor
	classMap        map[string][]string
	classPriorities map[string]int
}

func (collector *auditDiffCollector) process(message string) {
	diffLines := strings.Split(message, "\n")

	for _, diffLine := range diffLines {
		prefixLength := strings.IndexRune(diffLine, ' ')
		if prefixLength > 0 {
			class := collector.visitor.AuditDiffClasses.Get(diffLine[:prefixLength])
			if class.ShouldDisplay {
				collector.classMap[class.Name] = append(collector.classMap[class.Name], diffLine)
				collector.classPriorities[class.Name] = class.Priority
			}
		}
	}
}
