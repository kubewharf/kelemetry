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

package aggregator

import (
	"time"

	"github.com/kubewharf/kelemetry/pkg/aggregator/tracer"
	"github.com/kubewharf/kelemetry/pkg/util/zconstants"
)

type Event struct {
	Field       string
	Title       string
	Time        time.Time
	EndTime     *time.Time
	TraceSource string
	Tags        map[string]any
	Logs        []tracer.Log
}

func NewEvent(field string, title string, when time.Time, traceSource string) *Event {
	return &Event{
		Field:       field,
		Title:       title,
		Time:        when,
		TraceSource: traceSource,
		Tags:        map[string]any{},
		Logs:        []tracer.Log{},
	}
}

func (event *Event) getEndTime() time.Time {
	if event.EndTime != nil {
		return *event.EndTime
	}

	return event.Time.Add(zconstants.DummyDuration)
}

func (event *Event) WithEndTime(when time.Time) *Event {
	event.EndTime = &when
	return event
}

func (event *Event) WithDuration(duration time.Duration) *Event {
	return event.WithEndTime(event.Time.Add(duration))
}

func (event *Event) WithTag(key string, value any) *Event {
	event.Tags[key] = value
	return event
}

func (event *Event) Log(ty zconstants.LogType, value string, attrs ...string) *Event {
	if len(attrs)%2 != 0 {
		panic("attrs must be key-value pairs")
	}

	pairs := [][2]string{}
	for i := 0; i < len(attrs); i += 2 {
		pairs = append(pairs, [2]string{attrs[i], attrs[i+1]})
	}

	event.Logs = append(event.Logs, tracer.Log{
		Type:    ty,
		Message: value,
		Attrs:   pairs,
	})
	return event
}
