// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bridge"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/stompserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"sync"
	"sync/atomic"
	"testing"
)

var evtBusTest *transportEventBus
var evtbusTestChannelName string = "test-channel"
var evtbusTestManager ChannelManager

type MockBrokerConnector struct {
	mock.Mock
}

func (mock *MockBrokerConnector) Connect(config *bridge.BrokerConnectorConfig, enableLogging bool) (bridge.Connection, error) {
	args := mock.MethodCalled("Connect", config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(bridge.Connection), args.Error(1)
}

func (mock *MockBrokerConnector) StartTCPServer(address string) error {
	args := mock.MethodCalled("StartTCPServer", address)
	return args.Error(0)
}

func newTestEventBus() EventBus {
	return NewEventBusInstance()
}

func init() {
	evtBusTest = GetBus().(*transportEventBus)
}

func createTestChannel() *Channel {

	//create new bus
	//bf := new(transportEventBus)
	//bf.init()
	//evtBusTest = bf
	//busInstance = bf // set GetBus() instance to return new instance also.

	evtbusTestManager = evtBusTest.GetChannelManager()
	return evtbusTestManager.CreateChannel(evtbusTestChannelName)
}

func inc(counter *int32) {
	atomic.AddInt32(counter, 1)
}

func destroyTestChannel() {
	evtbusTestManager.DestroyChannel(evtbusTestChannelName)
}

func TestEventBus_Boot(t *testing.T) {

	bus1 := GetBus()
	bus2 := GetBus()
	bus3 := GetBus()

	assert.EqualValues(t, bus1.GetId(), bus2.GetId())
	assert.EqualValues(t, bus2.GetId(), bus3.GetId())
	assert.NotNil(t, evtBusTest.GetChannelManager())
}

func TestEventBus_SendResponseMessageNoChannel(t *testing.T) {
	err := evtBusTest.SendResponseMessage("Channel-not-here", "hello melody", nil)
	assert.NotNil(t, err)
}

func TestEventBus_SendRequestMessageNoChannel(t *testing.T) {
	err := evtBusTest.SendRequestMessage("Channel-not-here", "hello melody", nil)
	assert.NotNil(t, err)
}

func TestTransportEventBus_SendBroadcastMessageNoChannel(t *testing.T) {
	err := evtBusTest.SendBroadcastMessage("Channel-not-here", "hello melody")
	assert.NotNil(t, err)
}

func TestEventBus_ListenStream(t *testing.T) {
	createTestChannel()
	handler, err := evtBusTest.ListenStream(evtbusTestChannelName)
	assert.Nil(t, err)
	assert.NotNil(t, handler)
	var count int32 = 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	for i := 0; i < 3; i++ {
		evtBusTest.SendResponseMessage(evtbusTestChannelName, "hello melody", nil)

		// send requests to make sure we're only getting requests
		//evtBusTest.SendRequestMessage(evtbusTestChannelName, 0, nil)
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 1, nil)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, int32(3), count)
	destroyTestChannel()
}

func TestTransportEventBus_ListenStreamForBroadcast(t *testing.T) {
	createTestChannel()
	handler, err := evtBusTest.ListenStream(evtbusTestChannelName)
	assert.Nil(t, err)
	assert.NotNil(t, handler)
	var count int32 = 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	for i := 0; i < 3; i++ {
		evtBusTest.SendBroadcastMessage(evtbusTestChannelName, "hello melody")

		// send requests to make sure we're only getting requests
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 1, nil)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, int32(3), count)
	destroyTestChannel()
}

func TestTransportEventBus_ListenStreamForDestination(t *testing.T) {
	createTestChannel()
	id := uuid.New()
	handler, _ := evtBusTest.ListenStreamForDestination(evtbusTestChannelName, &id)
	var count int32 = 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	for i := 0; i < 20; i++ {
		evtBusTest.SendResponseMessage(evtbusTestChannelName, "hello melody", &id)

		// send requests to make sure we're only getting requests
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 0, &id)
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 1, &id)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, int32(20), count)
	destroyTestChannel()
}

func TestEventBus_ListenStreamNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenStream("missing-Channel")
	assert.NotNil(t, err)
}

func TestEventBus_ListenOnce(t *testing.T) {
	createTestChannel()
	handler, _ := evtBusTest.ListenOnce(evtbusTestChannelName)
	count := 0
	handler.Handle(
		func(msg *model.Message) {
			count++
		},
		func(err error) {})

	for i := 0; i < 10; i++ {
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 0, handler.GetDestinationId())
	}

	for i := 0; i < 2; i++ {
		evtBusTest.SendResponseMessage(evtbusTestChannelName, 0, handler.GetDestinationId())

		// send requests to make sure we're only getting requests
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 0, handler.GetDestinationId())
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 1, handler.GetDestinationId())
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_ListenOnceForDestination(t *testing.T) {
	createTestChannel()
	dest := uuid.New()
	handler, _ := evtBusTest.ListenOnceForDestination(evtbusTestChannelName, &dest)
	count := 0
	handler.Handle(
		func(msg *model.Message) {
			count++
		},
		func(err error) {})

	for i := 0; i < 300; i++ {
		evtBusTest.SendResponseMessage(evtbusTestChannelName, 0, &dest)

		// send duplicate
		evtBusTest.SendResponseMessage(evtbusTestChannelName, 0, &dest)

		// send random noise
		evtBusTest.SendResponseMessage(evtbusTestChannelName, 0, nil)

		// send requests to make sure we're only getting requests
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 0, &dest)
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 1, &dest)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_ListenOnceNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenOnce("missing-Channel")
	assert.NotNil(t, err)
}

func TestEventBus_ListenOnceForDestinationNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenOnceForDestination("missing-Channel", nil)
	assert.NotNil(t, err)
}

func TestEventBus_ListenOnceForDestinationNoDestination(t *testing.T) {
	createTestChannel()
	_, err := evtBusTest.ListenOnceForDestination(evtbusTestChannelName, nil)
	assert.NotNil(t, err)
	destroyTestChannel()
}

func TestEventBus_ListenRequestStream(t *testing.T) {
	createTestChannel()
	handler, _ := evtBusTest.ListenRequestStream(evtbusTestChannelName)
	var count int32 = 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	for i := 0; i < 10000; i++ {
		evtBusTest.SendRequestMessage(evtbusTestChannelName, "hello melody", nil)

		// send responses to make sure we're only getting requests
		evtBusTest.SendResponseMessage(evtbusTestChannelName, "will fail assertion if picked up", nil)
		evtBusTest.SendResponseMessage(evtbusTestChannelName, "will fail assertion again", nil)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, count, int32(10000))
	destroyTestChannel()
}

func TestEventBus_ListenRequestStreamForDestination(t *testing.T) {
	createTestChannel()
	id := uuid.New()
	handler, _ := evtBusTest.ListenRequestStreamForDestination(evtbusTestChannelName, &id)
	var count int32 = 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	for i := 0; i < 1000; i++ {
		evtBusTest.SendRequestMessage(evtbusTestChannelName, "hello melody", &id)

		// send responses to make sure we're only getting requests
		evtBusTest.SendResponseMessage(evtbusTestChannelName, "will fail assertion if picked up", &id)
		evtBusTest.SendResponseMessage(evtbusTestChannelName, "will fail assertion again", &id)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, count, int32(1000))
	destroyTestChannel()
}

func TestEventBus_ListenStreamForDestinationNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenStreamForDestination("missing-Channel", nil)
	assert.NotNil(t, err)
}

func TestEventBus_ListenStreamForDestinationNoDestination(t *testing.T) {
	createTestChannel()
	_, err := evtBusTest.ListenStreamForDestination(evtbusTestChannelName, nil)
	assert.NotNil(t, err)
}

func TestEventBus_ListenRequestStreamForDestinationNoDestination(t *testing.T) {
	createTestChannel()
	_, err := evtBusTest.ListenRequestStreamForDestination(evtbusTestChannelName, nil)
	assert.NotNil(t, err)
}

func TestEventBus_ListenRequestStreamForDestinationNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenRequestStreamForDestination("nowhere", nil)
	assert.NotNil(t, err)
}

func TestEventBus_ListenRequestOnce(t *testing.T) {
	createTestChannel()
	handler, _ := evtBusTest.ListenRequestOnce(evtbusTestChannelName)
	count := 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			count++
		},
		func(err error) {})

	for i := 0; i < 5; i++ {
		evtBusTest.SendRequestMessage(evtbusTestChannelName, "hello melody", handler.GetDestinationId())
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_ListenRequestOnceForDestination(t *testing.T) {
	createTestChannel()
	dest := uuid.New()
	handler, _ := evtBusTest.ListenRequestOnceForDestination(evtbusTestChannelName, &dest)
	count := 0
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "hello melody", msg.Payload.(string))
			count++
		},
		func(err error) {})

	for i := 0; i < 5; i++ {
		evtBusTest.SendRequestMessage(evtbusTestChannelName, "hello melody", &dest)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_ListenRequestOnceNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenRequestOnce("missing-Channel")
	assert.NotNil(t, err)
}

func TestEventBus_ListenRequestStreamNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenRequestStream("missing-Channel")
	assert.NotNil(t, err)
}

func TestEventBus_ListenRequestOnceForDestinationNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenRequestOnceForDestination("missing-Channel", nil)
	assert.NotNil(t, err)
}

func TestEventBus_ListenRequestOnceForDestinationNoDestination(t *testing.T) {
	createTestChannel()
	_, err := evtBusTest.ListenRequestOnceForDestination(evtbusTestChannelName, nil)
	assert.NotNil(t, err)
	destroyTestChannel()
}

func TestEventBus_TestErrorMessageHandling(t *testing.T) {
	createTestChannel()

	err := evtBusTest.SendErrorMessage("invalid-Channel", errors.New("something went wrong"), nil)
	assert.NotNil(t, err)

	handler, _ := evtBusTest.ListenStream(evtbusTestChannelName)
	var countError int32 = 0
	handler.Handle(
		func(msg *model.Message) {},
		func(err error) {
			assert.Errorf(t, err, "something went wrong")
			inc(&countError)
		})

	for i := 0; i < 5; i++ {
		err := errors.New("something went wrong")
		evtBusTest.SendErrorMessage(evtbusTestChannelName, err, handler.GetId())
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, int32(5), countError)
	destroyTestChannel()
}

func TestEventBus_ListenFirehose(t *testing.T) {
	createTestChannel()
	var counter int32 = 0

	responseHandler, _ := evtBusTest.ListenFirehose(evtbusTestChannelName)
	responseHandler.Handle(
		func(msg *model.Message) {
			inc(&counter)
		},
		func(err error) {
			inc(&counter)
		})
	for i := 0; i < 5; i++ {
		err := errors.New("something went wrong")
		evtBusTest.SendErrorMessage(evtbusTestChannelName, err, nil)
		evtBusTest.SendRequestMessage(evtbusTestChannelName, 0, nil)
		evtBusTest.SendResponseMessage(evtbusTestChannelName, 1, nil)
	}
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, counter, int32(15))
	destroyTestChannel()
}

func TestEventBus_ListenFirehoseNoChannel(t *testing.T) {
	_, err := evtBusTest.ListenFirehose("missing-Channel")
	assert.NotNil(t, err)
}

func TestEventBus_RequestOnce(t *testing.T) {
	createTestChannel()
	handler, _ := evtBusTest.ListenRequestStream(evtbusTestChannelName)
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "who is a pretty baby?", msg.Payload.(string))
			evtBusTest.SendResponseMessage(evtbusTestChannelName, "why melody is of course", msg.DestinationId)
		},
		func(err error) {})

	count := 0
	responseHandler, _ := evtBusTest.RequestOnce(evtbusTestChannelName, "who is a pretty baby?")
	responseHandler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "why melody is of course", msg.Payload.(string))
			count++
		},
		func(err error) {})

	responseHandler.Fire()
	evtbusTestManager.WaitForChannel(evtbusTestChannelName)
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_RequestOnceForDestination(t *testing.T) {
	createTestChannel()
	dest := uuid.New()
	handler, _ := evtBusTest.ListenRequestStream(evtbusTestChannelName)
	handler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "who is a pretty baby?", msg.Payload.(string))
			evtBusTest.SendResponseMessage(evtbusTestChannelName, "why melody is of course", msg.DestinationId)
		},
		func(err error) {})

	count := 0
	responseHandler, _ := evtBusTest.RequestOnceForDestination(evtbusTestChannelName, "who is a pretty baby?", &dest)
	responseHandler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "why melody is of course", msg.Payload.(string))
			count++
		},
		func(err error) {})

	responseHandler.Fire()
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_RequestOnceForDesintationNoChannel(t *testing.T) {
	_, err := evtBusTest.RequestOnceForDestination("some-chan", nil, nil)
	assert.NotNil(t, err)
}

func TestEventBus_RequestOnceForDesintationNoDestination(t *testing.T) {
	createTestChannel()
	_, err := evtBusTest.RequestOnceForDestination(evtbusTestChannelName, nil, nil)
	assert.NotNil(t, err)
	destroyTestChannel()
}

func TestEventBus_RequestStream(t *testing.T) {
	channel := createTestChannel()
	handler := func(message *model.Message) {
		if message.Direction == model.RequestDir {
			assert.Equal(t, "who has the cutest laugh?", message.Payload.(string))
			config := buildConfig(channel.Name, "why melody does of course", message.DestinationId)

			// fire a few times, ensure that the handler only ever picks up a single response.
			for i := 0; i < 5; i++ {
				channel.Send(model.GenerateResponse(config))
			}
		}
	}
	id := uuid.New()
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	var count int32 = 0
	responseHandler, _ := evtBusTest.RequestStream(evtbusTestChannelName, "who has the cutest laugh?")
	responseHandler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "why melody does of course", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	responseHandler.Fire()
	assert.Equal(t, int32(5), count)
	destroyTestChannel()
}

func TestEventBus_RequestStreamForDesintation(t *testing.T) {
	channel := createTestChannel()
	dest := uuid.New()
	handler := func(message *model.Message) {
		if message.Direction == model.RequestDir {
			assert.Equal(t, "who has the cutest laugh?", message.Payload.(string))
			config := buildConfig(channel.Name, "why melody does of course", message.DestinationId)

			// fire a few times, ensure that the handler only ever picks up a single response.
			for i := 0; i < 5; i++ {
				channel.Send(model.GenerateResponse(config))
			}
		}
	}
	id := uuid.New()
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: false, uuid: &id})

	var count int32 = 0
	responseHandler, _ := evtBusTest.RequestStreamForDestination(evtbusTestChannelName, "who has the cutest laugh?", &dest)
	responseHandler.Handle(
		func(msg *model.Message) {
			assert.Equal(t, "why melody does of course", msg.Payload.(string))
			inc(&count)
		},
		func(err error) {})

	responseHandler.Fire()
	assert.Equal(t, int32(5), count)
	destroyTestChannel()
}

func TestEventBus_RequestStreamForDestinationNoChannel(t *testing.T) {
	_, err := evtBusTest.RequestStreamForDestination("missing-Channel", nil, nil)
	assert.NotNil(t, err)
}

func TestEventBus_RequestStreamForDestinationNoDestination(t *testing.T) {
	createTestChannel()
	_, err := evtBusTest.RequestStreamForDestination(evtbusTestChannelName, nil, nil)
	assert.NotNil(t, err)
	destroyTestChannel()
}

func TestEventBus_RequestStreamNoChannel(t *testing.T) {
	_, err := evtBusTest.RequestStream("missing-Channel", nil)
	assert.NotNil(t, err)
}

func TestEventBus_HandleSingleRunError(t *testing.T) {
	channel := createTestChannel()
	handler := func(message *model.Message) {
		if message.Direction == model.RequestDir {
			config := buildError(channel.Name, fmt.Errorf("whoops!"), message.DestinationId)

			// fire a few times, ensure that the handler only ever picks up a single response.
			for i := 0; i < 5; i++ {
				channel.Send(model.GenerateError(config))
			}
		}
	}
	id := uuid.New()
	channel.subscribeHandler(&channelEventHandler{callBackFunction: handler, runOnce: true, uuid: &id})

	count := 0
	responseHandler, _ := evtBusTest.RequestOnce(evtbusTestChannelName, 0)
	responseHandler.Handle(
		func(msg *model.Message) {},
		func(err error) {
			assert.Error(t, err, "whoops!")
			count++
		})

	responseHandler.Fire()
	assert.Equal(t, 1, count)
	destroyTestChannel()
}

func TestEventBus_RequestOnceNoChannel(t *testing.T) {
	_, err := evtBusTest.RequestOnce("missing-Channel", 0)
	assert.NotNil(t, err)
}

func TestEventBus_HandlerWithoutRequestToFire(t *testing.T) {
	createTestChannel()
	responseHandler, _ := evtBusTest.ListenFirehose(evtbusTestChannelName)
	responseHandler.Handle(
		func(msg *model.Message) {},
		func(err error) {})
	err := responseHandler.Fire()
	assert.Errorf(t, err, "nothing to fire, request is empty")
	destroyTestChannel()
}

func TestEventBus_GetStoreManager(t *testing.T) {
	assert.NotNil(t, evtBusTest.GetStoreManager())
	store := evtBusTest.GetStoreManager().CreateStore("test")
	assert.NotNil(t, store)
	assert.True(t, evtBusTest.GetStoreManager().DestroyStore("test"))
}

func TestChannelManager_TestConnectBroker(t *testing.T) {

	// create new transportEventBus instance and replace the brokerConnector
	// with MockBrokerConnector instance.
	evtBusTest := newTestEventBus().(*transportEventBus)
	evtBusTest.bc = new(MockBrokerConnector)

	// connect to broker
	cf := &bridge.BrokerConnectorConfig{
		Username: "test",
		Password: "test",
		UseWS:    true,
		WebSocketConfig: &bridge.WebSocketConfig{
			WSPath: "/",
		},
		ServerAddr: "broker-url"}

	id := uuid.New()
	mockCon := &MockBridgeConnection{
		Id: &id,
	}
	evtBusTest.bc.(*MockBrokerConnector).On("Connect", cf).Return(mockCon, nil)

	c, _ := evtBusTest.ConnectBroker(cf)

	assert.Equal(t, c, mockCon)
	assert.Equal(t, len(evtBusTest.brokerConnections), 1)
	assert.Equal(t, evtBusTest.brokerConnections[mockCon.Id], mockCon)
}

func TestEventBus_TestCreateSyncTransaction(t *testing.T) {
	tr := evtBusTest.CreateSyncTransaction()
	assert.NotNil(t, tr)
	assert.Equal(t, tr.(*busTransaction).transactionType, syncTransaction)
}

func TestEventBus_TestCreateAsyncTransaction(t *testing.T) {
	tr := evtBusTest.CreateAsyncTransaction()
	assert.NotNil(t, tr)
	assert.Equal(t, tr.(*busTransaction).transactionType, asyncTransaction)
}

type MockRawConnListener struct {
	stopped     bool
	connections chan stompserver.RawConnection
	wg          sync.WaitGroup
}

func (cl *MockRawConnListener) Accept() (stompserver.RawConnection, error) {
	cl.wg.Done()
	con := <-cl.connections
	return con, nil
}

func (cl *MockRawConnListener) Close() error {
	cl.stopped = true
	cl.wg.Done()
	return nil
}

func TestBifrostEventBus_StartFabricEndpoint(t *testing.T) {
	bus := newTestEventBus().(*transportEventBus)

	connListener := &MockRawConnListener{
		connections: make(chan stompserver.RawConnection),
	}

	err := bus.StartFabricEndpoint(connListener, EndpointConfig{})
	assert.EqualError(t, err, "invalid TopicPrefix")

	err = bus.StartFabricEndpoint(connListener, EndpointConfig{TopicPrefix: "asd"})
	assert.EqualError(t, err, "invalid TopicPrefix")

	err = bus.StartFabricEndpoint(connListener, EndpointConfig{TopicPrefix: "/topic",
		AppRequestQueuePrefix: "/pub"})
	assert.EqualError(t, err, "missing UserQueuePrefix")

	connListener.wg.Add(1)
	go bus.StartFabricEndpoint(connListener, EndpointConfig{TopicPrefix: "/topic"})

	connListener.wg.Wait()

	err = bus.StartFabricEndpoint(connListener, EndpointConfig{TopicPrefix: "/topic"})
	assert.EqualError(t, err, "unable to start: fabric endpoint is already running")

	connListener.wg.Add(1)
	bus.StopFabricEndpoint()
	connListener.wg.Wait()

	assert.Nil(t, bus.fabEndpoint)
	assert.True(t, connListener.stopped)

	assert.EqualError(t, bus.StopFabricEndpoint(), "unable to stop: fabric endpoint is not running")
}

func TestBifrostEventBus_AddMonitorEventListener(t *testing.T) {

	bus := newTestEventBus()

	listener1Count := 0
	listener1 := bus.AddMonitorEventListener(func(event *MonitorEvent) {
		listener1Count++
	}, ChannelCreatedEvt)

	listener2Count := 0
	listener2 := bus.AddMonitorEventListener(func(event *MonitorEvent) {
		listener2Count++
	}, ChannelCreatedEvt, ChannelDestroyedEvt)

	listener3Count := 0
	listener3 := bus.AddMonitorEventListener(func(event *MonitorEvent) {
		listener3Count++
	})

	assert.NotEqual(t, listener1, listener2)
	assert.NotEqual(t, listener1, listener3)
	assert.NotEqual(t, listener2, listener3)

	bus.SendMonitorEvent(ChannelCreatedEvt, "test-channel", nil)
	assert.Equal(t, listener1Count, 1)
	assert.Equal(t, listener2Count, 1)
	assert.Equal(t, listener3Count, 1)

	bus.SendMonitorEvent(ChannelDestroyedEvt, "test-channel", nil)
	assert.Equal(t, listener1Count, 1)
	assert.Equal(t, listener2Count, 2)
	assert.Equal(t, listener3Count, 2)

	bus.SendMonitorEvent(StoreInitializedEvt, "store1", nil)
	assert.Equal(t, listener1Count, 1)
	assert.Equal(t, listener2Count, 2)
	assert.Equal(t, listener3Count, 3)

	bus.RemoveMonitorEventListener(listener2)

	bus.SendMonitorEvent(ChannelCreatedEvt, "test-channel", nil)
	assert.Equal(t, listener1Count, 2)
	assert.Equal(t, listener2Count, 2)
	assert.Equal(t, listener3Count, 4)

	bus.SendMonitorEvent(ChannelDestroyedEvt, "test-channel", nil)
	assert.Equal(t, listener1Count, 2)
	assert.Equal(t, listener2Count, 2)
	assert.Equal(t, listener3Count, 5)

	bus.RemoveMonitorEventListener(listener3)
	bus.SendMonitorEvent(ChannelCreatedEvt, "test-channel", nil)
	assert.Equal(t, listener1Count, 3)
	assert.Equal(t, listener2Count, 2)
	assert.Equal(t, listener3Count, 5)
}
