// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package store

import (
	"context"
	"errors"

	"github.com/go-stomp/stomp/v3/frame"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bridge"
	"github.com/pb33f/ranch/model"
	"github.com/stretchr/testify/mock"
)

type MockBridgeConnection struct {
	mock.Mock
	Id *uuid.UUID
}

func (c *MockBridgeConnection) Conversation(destination string, payload []byte, opts ...func(*frame.Frame) error) (bridge.Subscription, error) {
	return nil, errors.New("unexpected Conversation call")
}

func (c *MockBridgeConnection) RequestResponse(ctx context.Context, payload []byte, opts ...func(*frame.Frame) error) (*model.Message, error) {
	return nil, errors.New("unexpected RequestResponse call")
}

func (c *MockBridgeConnection) GetId() *uuid.UUID {
	return c.Id
}

func (c *MockBridgeConnection) SubscribeReplyDestination(destination string) (bridge.Subscription, error) {
	args := c.MethodCalled("Subscribe", destination)
	return args.Get(0).(bridge.Subscription), args.Error(1)
}

func (c *MockBridgeConnection) Subscribe(destination string) (bridge.Subscription, error) {
	args := c.MethodCalled("Subscribe", destination)
	return args.Get(0).(bridge.Subscription), args.Error(1)
}

func (c *MockBridgeConnection) Disconnect() (err error) {
	return nil
}

func (c *MockBridgeConnection) SendJSONMessage(destination string, payload []byte, opts ...func(frame *frame.Frame) error) error {
	args := c.MethodCalled("SendJSONMessage", destination, payload)
	return args.Error(0)
}

func (c *MockBridgeConnection) SendMessage(destination, contentType string, payload []byte, opts ...func(frame *frame.Frame) error) error {
	args := c.MethodCalled("SendMessage", destination, contentType, payload)
	return args.Error(0)
}

func (c *MockBridgeConnection) SendMessageWithReplyDestination(destination, reply, contentType string, payload []byte, opts ...func(frame *frame.Frame) error) error {
	args := c.MethodCalled("SendMessage", destination, contentType, payload)
	return args.Error(0)
}

type MockBridgeSubscription struct {
	Id          *uuid.UUID
	Destination string
	Channel     chan *model.Message
}

func (m *MockBridgeSubscription) GetId() *uuid.UUID {
	return m.Id
}

func (m *MockBridgeSubscription) GetDestination() string {
	return m.Destination
}

func (m *MockBridgeSubscription) GetMsgChannel() chan *model.Message {
	return m.Channel
}

func (m *MockBridgeSubscription) Unsubscribe() error {
	return nil
}
