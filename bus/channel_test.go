// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"context"
	"errors"
	"github.com/go-stomp/stomp/v3/frame"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bridge"
	"github.com/pb33f/ranch/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"testing"
	"time"
)

var testChannelName string = "testing"

func TestChannel_CheckChannelCreation(t *testing.T) {

	channel := NewChannel(testChannelName)
	assert.Empty(t, channel.handlersSnapshot())

}

func TestChannel_SubscribeHandler(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	handler := func(*model.Message) {}
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	assert.Equal(t, 1, len(channel.handlersSnapshot()))

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	assert.Equal(t, 2, len(channel.handlersSnapshot()))
}

func TestChannel_HandlerCheck(t *testing.T) {
	channel := NewChannel(testChannelName)
	id := uuid.New()
	assert.False(t, channel.ContainsHandlers())

	handler := func(*model.Message) {}
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	assert.True(t, channel.ContainsHandlers())
}

func TestChannel_SendMessage(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	handler := func(message *model.Message) {
		assert.Equal(t, message.Payload.(string), "pickled eggs")
		assert.Equal(t, message.Channel, testChannelName)
	}

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	var message = &model.Message{
		Id:        &id,
		Payload:   "pickled eggs",
		Channel:   testChannelName,
		Direction: model.RequestDir}

	channel.Send(message)
	channel.wg.Wait()
}

func TestChannel_SendContextPassesContext(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	type contextKey string
	key := contextKey("trace")

	var got string
	handler := func(ctx context.Context, message *model.Message) {
		got = ctx.Value(key).(string)
	}

	channel.subscribeHandler(&channelEventHandler{contextCallBackFunction: handler, runOnce: false, uuid: &id})

	message := &model.Message{Id: &id, Channel: testChannelName, Direction: model.RequestDir}
	channel.SendContext(context.WithValue(context.Background(), key, "request-1"), message)
	channel.wg.Wait()

	assert.Equal(t, "request-1", got)
}

func TestChannel_SendContextCancelled(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	called := false

	handler := func(ctx context.Context, message *model.Message) {
		called = true
	}
	channel.subscribeHandler(&channelEventHandler{contextCallBackFunction: handler, runOnce: false, uuid: &id})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	channel.SendContext(ctx, &model.Message{Id: &id, Channel: testChannelName, Direction: model.RequestDir})
	channel.wg.Wait()

	assert.False(t, called)
}

func TestMessageHandler_FireContextIgnoresUnrelatedChannelWaits(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	channel.wg.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	handler := &messageHandler{
		channel:        channel,
		requestMessage: &model.Message{Id: &id, Channel: testChannelName, Direction: model.RequestDir},
	}
	err := handler.FireContext(ctx)
	channel.wg.Done()

	assert.NoError(t, err)
}

func TestChannel_SendMessageRunOnceHasRun(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	count := 0
	handler := func(message *model.Message) {
		assert.Equal(t, message.Payload.(string), "pickled eggs")
		assert.Equal(t, message.Channel, testChannelName)
		count++
	}

	h := &channelEventHandler{callBackFunction: handler, runOnce: true, uuid: &id}
	channel.subscribeHandler(h)

	var message = &model.Message{
		Id:        &id,
		Payload:   "pickled eggs",
		Channel:   testChannelName,
		Direction: model.RequestDir}

	channel.Send(message)
	channel.wg.Wait()
	assert.False(t, channel.ContainsHandlers())
	channel.Send(message)
	assert.Equal(t, 1, count)

	secondID := uuid.New()
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &secondID})
	assert.Len(t, channel.handlersSnapshot(), 1)
}

func TestChannel_SendMultipleMessages(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	var counter int32 = 0
	handler := func(message *model.Message) {
		assert.Equal(t, message.Payload.(string), "chewy louie")
		assert.Equal(t, message.Channel, testChannelName)
		inc(&counter)
	}

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})
	var message = &model.Message{
		Id:        &id,
		Payload:   "chewy louie",
		Channel:   testChannelName,
		Direction: model.RequestDir}

	channel.Send(message)
	channel.Send(message)
	channel.Send(message)
	channel.wg.Wait()
	assert.Equal(t, int32(3), counter)
}

func TestChannel_SendContextDeliversAfterCallerCancel(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	var counter int32
	releaseHandler := make(chan struct{})
	deadline := time.Now().Add(time.Hour)
	var handlerDeadline time.Time
	var handlerHasDeadline bool
	var handlerErr error

	channel.subscribeHandler(&channelEventHandler{
		contextCallBackFunction: func(ctx context.Context, message *model.Message) {
			<-releaseHandler
			handlerDeadline, handlerHasDeadline = ctx.Deadline()
			handlerErr = ctx.Err()
			assert.Equal(t, "accepted-message", message.Payload)
			inc(&counter)
		},
		runOnce: false,
		uuid:    &id,
	})

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	channel.SendContext(ctx, &model.Message{
		Id:        &id,
		Payload:   "accepted-message",
		Channel:   testChannelName,
		Direction: model.RequestDir,
	})
	cancel()
	close(releaseHandler)

	channel.wg.Wait()
	assert.Equal(t, int32(1), counter)
	assert.True(t, handlerHasDeadline)
	assert.Equal(t, deadline, handlerDeadline)
	assert.ErrorIs(t, handlerErr, context.Canceled)
}

func TestChannel_MultiHandlerSingleMessage(t *testing.T) {
	id := uuid.New()
	channel := NewChannel(testChannelName)
	var counterA, counterB, counterC int32 = 0, 0, 0

	handlerA := func(message *model.Message) {
		inc(&counterA)
	}
	handlerB := func(message *model.Message) {
		inc(&counterB)
	}
	handlerC := func(message *model.Message) {
		inc(&counterC)
	}

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handlerA, runOnce: false, uuid: &id})

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handlerB, runOnce: false, uuid: &id})

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handlerC, runOnce: false, uuid: &id})

	var message = &model.Message{
		Id:        &id,
		Payload:   "late night munchies",
		Channel:   testChannelName,
		Direction: model.RequestDir}

	channel.Send(message)
	channel.Send(message)
	channel.Send(message)
	channel.wg.Wait()
	value := counterA + counterB + counterC

	assert.Equal(t, int32(9), value)
}

func TestChannel_Privacy(t *testing.T) {
	channel := NewChannel(testChannelName)
	assert.False(t, channel.private)
	channel.SetPrivate(true)
	assert.True(t, channel.IsPrivate())
}

func TestChannel_ChannelGalactic(t *testing.T) {
	channel := NewChannel(testChannelName)
	assert.False(t, channel.galactic)
	channel.SetGalactic("somewhere")
	assert.True(t, channel.IsGalactic())
}

func TestChannel_RemoveEventHandler(t *testing.T) {
	channel := NewChannel(testChannelName)
	handlerA := func(message *model.Message) {}
	handlerB := func(message *model.Message) {}

	idA := uuid.New()
	idB := uuid.New()

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handlerA, runOnce: false, uuid: &idA})

	channel.subscribeHandler(&channelEventHandler{callBackFunction: handlerB, runOnce: false, uuid: &idB})

	assert.Len(t, channel.handlersSnapshot(), 2)

	// remove the first handler (A)
	channel.removeEventHandler(0)
	assert.Len(t, channel.handlersSnapshot(), 1)
	assert.Equal(t, idB.String(), channel.handlersSnapshot()[0].uuid.String())

	// remove the second handler B)
	channel.removeEventHandler(0)
	assert.True(t, len(channel.handlersSnapshot()) == 0)

}

func TestChannel_RemoveEventHandlerNoHandlers(t *testing.T) {
	channel := NewChannel(testChannelName)

	channel.removeEventHandler(0)
	assert.Len(t, channel.handlersSnapshot(), 0)
}

func TestChannel_RemoveEventHandlerOOBIndex(t *testing.T) {
	channel := NewChannel(testChannelName)
	id := uuid.New()
	handler := func(*model.Message) {}
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	channel.removeEventHandler(999)
	assert.Len(t, channel.handlersSnapshot(), 1)
}

func TestChannel_AddRemoveBrokerSubscription(t *testing.T) {
	channel := NewChannel(testChannelName)
	id := uuid.New()
	sub := &MockBridgeSubscription{Id: &id}
	c := &MockBridgeConnection{Id: &id}
	channel.addBrokerSubscription(c, sub)
	assert.Len(t, channel.brokerSubs, 1)
	channel.removeBrokerSubscription(sub)
	assert.Len(t, channel.brokerSubs, 0)
}

func TestChannel_CheckIfBrokerSubscribed(t *testing.T) {

	cId := uuid.New()
	sId := uuid.New()
	sId2 := uuid.New()

	c := &MockBridgeConnection{
		Id: &cId,
	}
	s := &MockBridgeSubscription{Id: &sId}
	s2 := &MockBridgeSubscription{Id: &sId2}

	cm := NewBusChannelManager(NewEventBus())
	ch := cm.CreateChannel("testing-broker-subs")
	ch.addBrokerSubscription(c, s)
	assert.True(t, ch.isBrokerSubscribed(s))
	assert.False(t, ch.isBrokerSubscribed(s2))

	ch.removeBrokerSubscription(s)
	assert.False(t, ch.isBrokerSubscribed(s))
}

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
	Id              *uuid.UUID
	Destination     string
	Channel         chan *model.Message
	Unsubscribed    bool
	UnsubscribeFunc func() error
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
	m.Unsubscribed = true
	if m.UnsubscribeFunc != nil {
		return m.UnsubscribeFunc()
	}
	return nil
}
