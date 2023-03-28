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

package mq

import (
	"context"

	"github.com/sirupsen/logrus"

	"github.com/kubewharf/kelemetry/pkg/manager"
)

func init() {
	manager.Global.Provide("mq", newMux)
}

type (
	ConsumerGroup string
	PartitionId   int32
)

// Queue is the abstraction of a partitioned message queue.
type Queue interface {
	CreateProducer() (Producer, error)
	CreateConsumer(group ConsumerGroup, partition PartitionId, handler MessageHandler) (Consumer, error)
}

type Producer interface {
	Send(partitionKey []byte, value []byte) error
}

type Consumer any

type mux struct {
	*manager.Mux
}

func newMux() Queue {
	return &mux{
		Mux: manager.NewMux("mq", false),
	}
}

func (mux *mux) CreateProducer() (Producer, error) {
	return mux.Impl().(Queue).CreateProducer()
}

func (mux *mux) CreateConsumer(group ConsumerGroup, partition PartitionId, handler MessageHandler) (Consumer, error) {
	return mux.Impl().(Queue).CreateConsumer(group, partition, handler)
}

type MessageHandler func(ctx context.Context, fieldLogger logrus.FieldLogger, key []byte, value []byte)
