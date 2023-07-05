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

// zconstants are special span/event tags that provide context
// for span transformation in the frontend storage plugin.
package zconstants

import "time"

// All tags with this prefix are not rendered.
const Prefix = "zzz-"

// The value replaces the OperationName.
const SpanName = Prefix + "kelemetryName"

// Indicates that the current span is a pseudospan that can be folded or flattened.
// The value is the folding type.
const NestLevel = Prefix + "nestingLevel"

const (
	NestLevelObject = "object"
)

// Identifies that the span represents an actual event (rather than as a pseudospan).
const TraceSource = Prefix + "traceSource"

const (
	TraceSourceObject = "object"
	TraceSourceAudit  = "audit"
	TraceSourceEvent  = "event"
)

func KnownTraceSources(withPseudo bool) []string {
	traceSources := []string{
		// pseudo
		TraceSourceObject,

		// real
		TraceSourceAudit,
		TraceSourceEvent,
	}

	if !withPseudo {
		traceSources = traceSources[1:]
	}

	return traceSources
}

// Classifies the type of a log line.
// Logs without this attribute will not have special treatment.
const LogTypeAttr = Prefix + "logType"

type LogType string

const (
	LogTypeRealError      LogType = "realError"
	LogTypeRealVerbose    LogType = "realVerbose"
	LogTypeKelemetryError LogType = "kelemetryError"
	LogTypeObjectSnapshot LogType = "audit/objectSnapshot"
	LogTypeObjectDiff     LogType = "audit/objectDiff"
	LogTypeEventMessage   LogType = "event/message"
)

// DummyDuration is the span duration used when the span is instantaneous.
const DummyDuration = time.Second
