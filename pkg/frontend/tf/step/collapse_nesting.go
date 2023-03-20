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
	"sort"
	"strings"

	"github.com/jaegertracing/jaeger/model"

	tftree "github.com/kubewharf/kelemetry/pkg/frontend/tf/tree"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

// Deletes child spans with a traceSource and injects them as logs in the nesting span.
//
// Multiple logs of the same span are aggregated into one log, flattening them into a field.
//
// Must be followed by PruneTagsVisitor in the last step.
type CollapseNestingVisitor struct {
	ShouldCollapse   func(traceSource string) bool
	TagMappings      map[string][]TagMapping  // key = traceSource
	AuditDiffClasses *AuditDiffClassification // key = prefix
	LogClassifier    func(traceSource string, attrs model.KeyValues) (key string, value string, shouldDisplay bool)
	LogTypeMapping   map[zconstants.LogType]string // key = log type, value = log field
}

type TagMapping struct {
	FromSpanTag string
	ToLogField  string
}

type AuditDiffClassification struct {
	SpecificFields map[string]AuditDiffClass // key = field in the form `metadata.resourceVersion`
	DefaultClass   AuditDiffClass
}

type AuditDiffClass struct {
	ShouldDisplay bool
	Name          string
	Priority      int
}

func NewAuditDiffClassification(defaultClass AuditDiffClass) *AuditDiffClassification {
	return &AuditDiffClassification{
		SpecificFields: map[string]AuditDiffClass{},
		DefaultClass:   defaultClass,
	}
}

func (classes *AuditDiffClassification) AddClass(class AuditDiffClass, fields []string) *AuditDiffClassification {
	for _, field := range fields {
		classes.SpecificFields[field] = class
	}
	return classes
}

func (classes *AuditDiffClassification) Get(prefix string) AuditDiffClass {
	if class, hasSpecific := classes.SpecificFields[prefix]; hasSpecific {
		return class
	}

	return classes.DefaultClass
}

func (visitor CollapseNestingVisitor) Enter(tree tftree.SpanTree, span *model.Span) tftree.TreeVisitor {
	if _, hasTag := model.KeyValues(span.Tags).FindByKey(zconstants.NestLevel); !hasTag {
		return visitor
	}

	for childId := range tree.Children(span.SpanID) {
		visitor.processChild(tree, span, childId)
	}

	return visitor
}

func (visitor CollapseNestingVisitor) Exit(tree tftree.SpanTree, span *model.Span) {}

func (visitor CollapseNestingVisitor) processChild(tree tftree.SpanTree, span *model.Span, childId model.SpanID) {
	childSpan := tree.Span(childId)
	if _, childHasTag := model.KeyValues(childSpan.Tags).FindByKey(zconstants.NestLevel); childHasTag {
		return
	}
	traceSourceKv, hasTraceSource := model.KeyValues(childSpan.Tags).FindByKey(zconstants.TraceSource)
	if !hasTraceSource {
		return
	}
	traceSource := traceSourceKv.VStr
	if !visitor.ShouldCollapse(traceSource) {
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
