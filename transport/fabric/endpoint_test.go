// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	ranchbus "github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/stompserver"
	"github.com/stretchr/testify/assert"
	"sync"
	"testing"
	"time"
)

type MockStompServerMessage struct {
	Destination string `json:"destination"`
	Payload     []byte `json:"payload"`
	conId       string
}

type MockStompServer struct {
	started                           bool
	ready                             chan struct{}
	readyOnce                         sync.Once
	sentMessages                      []MockStompServerMessage
	subscribeHandlerFunction          stompserver.SubscribeHandlerFunction
	connectionEventCallbacks          map[stompserver.StompSessionEventType]func(event *stompserver.ConnEvent)
	unsubscribeHandlerFunction        stompserver.UnsubscribeHandlerFunction
	applicationRequestHandlerFunction stompserver.ApplicationRequestHandlerFunction
	wg                                *sync.WaitGroup
	onStop                            func()
}

func (s *MockStompServer) Start() {
	s.started = true
	s.readyOnce.Do(func() {
		close(s.ready)
	})
}

func (s *MockStompServer) Stop() {
	s.started = false
	if s.onStop != nil {
		s.onStop()
	}
}

func (s *MockStompServer) Ready() <-chan struct{} {
	return s.ready
}

func (s *MockStompServer) SendMessage(destination string, messageBody []byte) {
	s.sentMessages = append(s.sentMessages,
		MockStompServerMessage{Destination: destination, Payload: messageBody})

	if s.wg != nil {
		s.wg.Done()
	}
}

func (s *MockStompServer) SendMessageToClient(conId string, destination string, messageBody []byte) {
	s.sentMessages = append(s.sentMessages,
		MockStompServerMessage{Destination: destination, Payload: messageBody, conId: conId})

	if s.wg != nil {
		s.wg.Done()
	}
}

func (s *MockStompServer) OnUnsubscribeEvent(callback stompserver.UnsubscribeHandlerFunction) {
	s.unsubscribeHandlerFunction = callback
}

func (s *MockStompServer) OnApplicationRequest(callback stompserver.ApplicationRequestHandlerFunction) {
	s.applicationRequestHandlerFunction = callback
}

func (s *MockStompServer) OnSubscribeEvent(callback stompserver.SubscribeHandlerFunction) {
	s.subscribeHandlerFunction = callback
}

func (s *MockStompServer) SetConnectionEventCallback(connEventType stompserver.StompSessionEventType, cb func(connEvent *stompserver.ConnEvent)) {
	s.connectionEventCallbacks[connEventType] = cb
	cb(&stompserver.ConnEvent{ConnId: "id"})
}

func (s *MockStompServer) CloseConnectionsByIP(ip string, errorMessage string) {
	// Mock implementation - no-op
}

func (s *MockStompServer) SetIPBlockingChecker(checker stompserver.IPBlockingChecker) {
	// mock implementation - no-op
}

func newTestEventBus() ranchbus.EventBus {
	return ranchbus.NewEventBus()
}

func newTestFabricEndpoint(bus ranchbus.EventBus, config EndpointConfig) (*fabricEndpoint, *MockStompServer) {

	fe := newFabricEndpoint(bus, nil, config)
	ms := &MockStompServer{
		ready:                    make(chan struct{}),
		connectionEventCallbacks: make(map[stompserver.StompSessionEventType]func(event *stompserver.ConnEvent)),
	}

	fe.server = ms
	fe.initHandlers()

	return fe, ms
}

func TestFabricEndpoint_newFabricEndpoint(t *testing.T) {
	fe, _ := newTestFabricEndpoint(nil, EndpointConfig{
		TopicPrefix:      "/topic",
		AppRequestPrefix: "/pub",
		Heartbeat:        0,
	})

	assert.NotNil(t, fe)
	assert.Equal(t, fe.config.TopicPrefix, "/topic/")
	assert.Equal(t, fe.config.AppRequestPrefix, "/pub/")

	fe, _ = newTestFabricEndpoint(nil, EndpointConfig{
		TopicPrefix:      "/topic/",
		AppRequestPrefix: "",
		Heartbeat:        0,
	})

	assert.Equal(t, fe.config.TopicPrefix, "/topic/")
	assert.Equal(t, fe.config.AppRequestPrefix, "")
}

func TestNew_ValidatesEndpointConfig(t *testing.T) {
	_, err := New(newTestEventBus(), nil, EndpointConfig{})
	assert.EqualError(t, err, "invalid TopicPrefix")

	_, err = New(newTestEventBus(), nil, EndpointConfig{TopicPrefix: "asd"})
	assert.EqualError(t, err, "invalid TopicPrefix")

	_, err = New(newTestEventBus(), nil, EndpointConfig{
		TopicPrefix:           "/topic",
		AppRequestQueuePrefix: "/pub",
	})
	assert.EqualError(t, err, "missing UserQueuePrefix")

	ep, err := New(newTestEventBus(), nil, EndpointConfig{TopicPrefix: "/topic"})
	assert.NoError(t, err)
	assert.NotNil(t, ep)
}

func TestFabricEndpoint_StartAndStop(t *testing.T) {
	fe, mockServer := newTestFabricEndpoint(nil, EndpointConfig{})
	assert.Equal(t, mockServer.started, false)
	assert.NoError(t, fe.Start(context.Background()))
	assert.Equal(t, mockServer.started, true)
	assert.NoError(t, fe.Stop())
	assert.Equal(t, mockServer.started, false)
}

func TestFabricEndpoint_StartCanceledBeforeStartCanRetry(t *testing.T) {
	fe, mockServer := newTestFabricEndpoint(nil, EndpointConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, fe.Start(ctx), context.Canceled)
	assert.False(t, mockServer.started)
	select {
	case <-fe.Ready():
		t.Fatal("endpoint became ready after canceled start")
	default:
	}

	assert.NoError(t, fe.Start(context.Background()))
	assert.True(t, mockServer.started)
	select {
	case <-fe.Ready():
	case <-time.After(time.Second):
		t.Fatal("endpoint did not become ready after retry")
	}
}

func TestFabricEndpoint_StartContextCancelDoesNotStopReadyServer(t *testing.T) {
	fe, mockServer := newTestFabricEndpoint(nil, EndpointConfig{})
	stopped := make(chan struct{})
	var stopOnce sync.Once
	mockServer.onStop = func() {
		stopOnce.Do(func() {
			close(stopped)
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	assert.NoError(t, fe.Start(ctx))
	cancel()

	select {
	case <-stopped:
		t.Fatal("ready endpoint stopped when start context was canceled")
	case <-time.After(50 * time.Millisecond):
	}
	assert.True(t, mockServer.started)

	assert.NoError(t, fe.Stop())
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("endpoint stop did not stop server")
	}
}

func TestFabricEndpoint_SubscribeEvent(t *testing.T) {

	bus := newTestEventBus()
	bus.GetChannelManager().CreateChannel(STOMP_SESSION_NOTIFY_CHANNEL) // used for internal channel protection test
	fe, mockServer := newTestFabricEndpoint(bus,
		EndpointConfig{TopicPrefix: "/topic", UserQueuePrefix: "/user/queue"})

	bus.GetChannelManager().CreateChannel("test-service")

	monitorWg := sync.WaitGroup{}
	var monitorEvents []*ranchbus.MonitorEvent
	bus.AddMonitorEventListener(func(monitorEvt *ranchbus.MonitorEvent) {
		monitorEvents = append(monitorEvents, monitorEvt)
		monitorWg.Done()
	}, FabricEndpointSubscribeEvt)

	// subscribe to invalid topic
	mockServer.subscribeHandlerFunction("con1", "sub1", "/topic2/test-service", nil)
	assert.Equal(t, len(fe.chanMappings), 0)

	assert.NoError(t, bus.SendResponseMessage("test-service", "test-message", nil))
	assert.Equal(t, len(mockServer.sentMessages), 0)

	// subscribe to valid channel
	monitorWg.Add(1)
	mockServer.subscribeHandlerFunction("con1", "sub1", "/topic/test-service", nil)
	monitorWg.Wait()
	assert.Equal(t, len(monitorEvents), 1)
	assert.Equal(t, monitorEvents[0].EventType, FabricEndpointSubscribeEvt)
	assert.Equal(t, monitorEvents[0].EntityName, "test-service")

	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 1)
	assert.Equal(t, fe.chanMappings["test-service"].subs["con1#sub1"], true)

	// subscribe again to the same channel
	monitorWg.Add(1)
	mockServer.subscribeHandlerFunction("con1", "sub2", "/topic/test-service", nil)
	monitorWg.Wait()

	assert.Equal(t, len(monitorEvents), 2)
	assert.Equal(t, monitorEvents[1].EventType, FabricEndpointSubscribeEvt)
	assert.Equal(t, monitorEvents[1].EntityName, "test-service")

	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 2)
	assert.Equal(t, fe.chanMappings["test-service"].subs["con1#sub2"], true)

	// subscribe to queue channel
	monitorWg.Add(1)
	mockServer.subscribeHandlerFunction("con1", "sub3", "/user/queue/test-service", nil)
	monitorWg.Wait()
	assert.Equal(t, len(monitorEvents), 3)
	assert.Equal(t, monitorEvents[2].EventType, FabricEndpointSubscribeEvt)
	assert.Equal(t, monitorEvents[2].EntityName, "test-service")

	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 3)
	assert.Equal(t, fe.chanMappings["test-service"].subs["con1#sub3"], true)

	// attempt to subscribe to a protected destination
	mockServer.subscribeHandlerFunction("con1", "sub4", "/topic/"+STOMP_SESSION_NOTIFY_CHANNEL, nil)
	_, chanMapCreated := fe.chanMappings[STOMP_SESSION_NOTIFY_CHANNEL]
	assert.False(t, chanMapCreated)

	mockServer.wg = &sync.WaitGroup{}
	mockServer.wg.Add(1)

	assert.NoError(t, bus.SendResponseMessage("test-service", "test-message", nil))

	mockServer.wg.Wait()

	mockServer.wg.Add(1)
	assert.NoError(t, bus.SendResponseMessage("test-service", []byte{1, 2, 3}, nil))
	mockServer.wg.Wait()

	mockServer.wg.Add(1)
	msg := MockStompServerMessage{Destination: "test", Payload: []byte("test-message")}
	assert.NoError(t, bus.SendResponseMessage("test-service", msg, nil))
	mockServer.wg.Wait()

	mockServer.wg.Add(1)
	assert.NoError(t, bus.SendErrorMessage("test-service", errors.New("test-error"), nil))
	mockServer.wg.Wait()

	assert.Equal(t, len(mockServer.sentMessages), 4)
	assert.Equal(t, mockServer.sentMessages[0].Destination, "/topic/test-service")
	assert.Equal(t, string(mockServer.sentMessages[0].Payload), "test-message")
	assert.Equal(t, mockServer.sentMessages[1].Payload, []byte{1, 2, 3})

	var sentMsg MockStompServerMessage
	assert.NoError(t, json.Unmarshal(mockServer.sentMessages[2].Payload, &sentMsg))
	assert.Equal(t, msg, sentMsg)

	assert.Equal(t, string(mockServer.sentMessages[3].Payload), "test-error")

	mockServer.wg.Add(1)
	assert.NoError(t, bus.SendResponseMessage("test-service", model.Response{
		BrokerDestination: &model.BrokerDestinationConfig{
			Destination:  "/user/queue/test-service",
			ConnectionId: "con1",
		},
		Payload: "test-private-message",
	}, nil))

	mockServer.wg.Wait()

	assert.Equal(t, len(mockServer.sentMessages), 5)
	assert.Equal(t, mockServer.sentMessages[4].Destination, "/user/queue/test-service")
	var sentResponse model.Response
	assert.NoError(t, json.Unmarshal(mockServer.sentMessages[4].Payload, &sentResponse))
	assert.Equal(t, sentResponse.Payload, "test-private-message")

	mockServer.wg.Add(1)
	assert.NoError(t, bus.SendResponseMessage("test-service", &model.Response{
		BrokerDestination: &model.BrokerDestinationConfig{
			Destination:  "/user/queue/test-service",
			ConnectionId: "con1",
		},
		Payload: "test-private-message-ptr",
	}, nil))

	mockServer.wg.Wait()

	assert.Equal(t, len(mockServer.sentMessages), 6)
	assert.Equal(t, mockServer.sentMessages[5].Destination, "/user/queue/test-service")
	assert.NoError(t, json.Unmarshal(mockServer.sentMessages[5].Payload, &sentResponse))
	assert.Equal(t, sentResponse.Payload, "test-private-message-ptr")
}

func TestFabricEndpoint_UnsubscribeEvent(t *testing.T) {
	bus := newTestEventBus()
	fe, mockServer := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/topic"})

	bus.GetChannelManager().CreateChannel("test-service")

	monitorWg := sync.WaitGroup{}
	var monitorEvents []*ranchbus.MonitorEvent
	bus.AddMonitorEventListener(func(monitorEvt *ranchbus.MonitorEvent) {
		monitorEvents = append(monitorEvents, monitorEvt)
		monitorWg.Done()
	}, FabricEndpointUnsubscribeEvt)

	// subscribe to valid channel
	mockServer.subscribeHandlerFunction("con1", "sub1", "/topic/test-service", nil)
	mockServer.subscribeHandlerFunction("con1", "sub2", "/topic/test-service", nil)

	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 2)

	mockServer.wg = &sync.WaitGroup{}
	mockServer.wg.Add(1)
	assert.NoError(t, bus.SendResponseMessage("test-service", "test-message", nil))
	mockServer.wg.Wait()
	assert.Equal(t, len(mockServer.sentMessages), 1)

	mockServer.unsubscribeHandlerFunction("con1", "sub2", "/invalid-topic/test-service")
	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 2)

	mockServer.unsubscribeHandlerFunction("invalid-con1", "sub2", "/topic/test-service")
	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 2)

	monitorWg.Add(1)
	mockServer.unsubscribeHandlerFunction("con1", "sub2", "/topic/test-service")
	monitorWg.Wait()

	assert.Equal(t, len(monitorEvents), 1)
	assert.Equal(t, monitorEvents[0].EventType, FabricEndpointUnsubscribeEvt)
	assert.Equal(t, monitorEvents[0].EntityName, "test-service")

	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["test-service"].subs), 1)

	mockServer.wg = &sync.WaitGroup{}
	mockServer.wg.Add(1)
	assert.NoError(t, bus.SendResponseMessage("test-service", "test-message", nil))
	mockServer.wg.Wait()
	assert.Equal(t, len(mockServer.sentMessages), 2)

	monitorWg.Add(1)
	mockServer.unsubscribeHandlerFunction("con1", "sub1", "/topic/test-service")
	monitorWg.Wait()

	assert.Equal(t, len(monitorEvents), 2)
	assert.Equal(t, monitorEvents[1].EventType, FabricEndpointUnsubscribeEvt)
	assert.Equal(t, monitorEvents[1].EntityName, "test-service")

	assert.Equal(t, len(fe.chanMappings), 0)
	assert.NoError(t, bus.SendResponseMessage("test-service", "test-message", nil))

	// subscribe to non-existing channel
	mockServer.subscribeHandlerFunction("con3", "sub1", "/topic/non-existing-channel", nil)
	assert.Equal(t, len(fe.chanMappings), 1)
	assert.Equal(t, len(fe.chanMappings["non-existing-channel"].subs), 1)
	assert.Equal(t, fe.chanMappings["non-existing-channel"].autoCreated, true)
	assert.True(t, bus.GetChannelManager().CheckChannelExists("non-existing-channel"))

	monitorWg.Add(1)
	mockServer.unsubscribeHandlerFunction("con3", "sub1", "/topic/non-existing-channel")
	monitorWg.Wait()

	assert.Equal(t, len(monitorEvents), 3)
	assert.Equal(t, monitorEvents[2].EventType, FabricEndpointUnsubscribeEvt)
	assert.Equal(t, monitorEvents[2].EntityName, "non-existing-channel")

	assert.Equal(t, len(fe.chanMappings), 0)
	assert.False(t, bus.GetChannelManager().CheckChannelExists("non-existing-channel"))
}

func TestFabricEndpoint_BridgeMessage(t *testing.T) {
	bus := newTestEventBus()
	_, mockServer := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/topic", AppRequestPrefix: "/pub",
		AppRequestQueuePrefix: "/pub/queue", UserQueuePrefix: "/user/queue"})

	bus.GetChannelManager().CreateChannel("request-channel")
	mh, _ := bus.ListenRequestStream("request-channel")
	assert.NotNil(t, mh)

	wg := sync.WaitGroup{}

	var messages []*model.Message

	mh.Handle(func(message *model.Message) {
		messages = append(messages, message)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "unexpected error")
	})

	id1 := uuid.New()
	req1, _ := json.Marshal(model.Request{
		RequestCommand: "test-request",
		Payload:        "test-rq",
		Id:             &id1,
	})

	wg.Add(1)

	mockServer.applicationRequestHandlerFunction("/pub/request-channel", req1, "con1")

	mockServer.applicationRequestHandlerFunction("/pub2/request-channel", req1, "con1")
	mockServer.applicationRequestHandlerFunction("/pub/request-channel-2", req1, "con1")

	mockServer.applicationRequestHandlerFunction("/pub/request-channel", []byte("invalid-request-json"), "con1")

	id2 := uuid.New()
	req2, _ := json.Marshal(model.Request{
		RequestCommand: "test-request2",
		Payload:        "test-rq2",
		Id:             &id2,
	})

	wg.Wait()

	wg.Add(1)
	mockServer.applicationRequestHandlerFunction("/pub/queue/request-channel", req2, "con2")
	wg.Wait()

	assert.Equal(t, len(messages), 2)

	receivedReq := messages[0].Payload.(*model.Request)

	assert.Equal(t, receivedReq.RequestCommand, "test-request")
	assert.Equal(t, receivedReq.Payload, "test-rq")
	assert.Equal(t, *receivedReq.Id, id1)
	assert.Nil(t, receivedReq.BrokerDestination)

	receivedReq2 := messages[1].Payload.(*model.Request)

	assert.Equal(t, receivedReq2.RequestCommand, "test-request2")
	assert.Equal(t, receivedReq2.Payload, "test-rq2")
	assert.Equal(t, *receivedReq2.Id, id2)
	assert.Equal(t, receivedReq2.BrokerDestination.ConnectionId, "con2")
	assert.Equal(t, receivedReq2.BrokerDestination.Destination, "/user/queue/request-channel")
}
