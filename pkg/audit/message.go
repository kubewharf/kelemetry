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

package audit

import auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

type Message struct {
	Cluster    string `json:"cluster"`
	SourceAddr string `json:"sourceAddr"`
	auditv1.Event
}

type RawMessage struct {
	Cluster    string `json:"cluster"`
	SourceAddr string `json:"sourceAddr"`
	*auditv1.EventList
}

// ClusterOnlyMessage is a trimmed version of Message/RawMessage that only deserializes the `cluster` field.
type ClusterOnlyMessage struct {
	Cluster string `json:"cluster"`
}

const (
	VerbCreate = "create"
	VerbUpdate = "update"
	VerbDelete = "delete"
	VerbPatch  = "patch"
)
