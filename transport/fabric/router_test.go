// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package fabric

import (
	"sync"
	"testing"

	"github.com/pb33f/ranch/model"
	"github.com/stretchr/testify/assert"
)

func TestRouter_DeliversWithoutSourceSubscription(t *testing.T) {
	bus := newTestEventBus()
	bus.GetChannelManager().CreateChannel("wallet-service")

	fe, mockServer := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/topic"})
	handle, err := fe.Router().RegisterRoute(RouteSpec{
		BusChannel:    "wallet-service",
		DefaultDest:   TopicDestination(&fe.config, "wallet-service"),
		AllowOverride: true,
		Direction:     model.ResponseDir,
	})
	assert.NoError(t, err)
	defer func() { _ = handle.Close() }()

	mockServer.wg = &sync.WaitGroup{}
	mockServer.wg.Add(1)

	assert.NoError(t, bus.SendResponseMessage("wallet-service", "balance-updated", nil))
	mockServer.wg.Wait()

	assert.Len(t, mockServer.sentMessages, 1)
	assert.Equal(t, "/topic/wallet-service", mockServer.sentMessages[0].Destination)
	assert.Equal(t, "balance-updated", string(mockServer.sentMessages[0].Payload))
	assert.Len(t, fe.chanMappings, 0)
}

func TestRouter_DefaultDestinationUsesEndpointTopicPrefix(t *testing.T) {
	bus := newTestEventBus()
	bus.GetChannelManager().CreateChannel("events-service")

	fe, mockServer := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/events"})
	handle, err := fe.Router().RegisterRoute(RouteSpec{
		BusChannel: "events-service",
		Direction:  model.ResponseDir,
	})
	assert.NoError(t, err)
	defer func() { _ = handle.Close() }()

	assert.NoError(t, bus.SendResponseMessage("events-service", "custom-prefix", nil))
	assert.NoError(t, bus.GetChannelManager().WaitForChannel("events-service"))
	assert.Len(t, mockServer.sentMessages, 1)
	assert.Equal(t, "/events/events-service", mockServer.sentMessages[0].Destination)
	assert.Equal(t, "custom-prefix", string(mockServer.sentMessages[0].Payload))
}

func TestRouter_HonorsRequestDirection(t *testing.T) {
	bus := newTestEventBus()
	bus.GetChannelManager().CreateChannel("request-service")

	fe, mockServer := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/topic"})
	handle, err := fe.Router().RegisterRoute(RouteSpec{
		BusChannel:    "request-service",
		DefaultDest:   TopicDestination(&fe.config, "request-service"),
		AllowOverride: true,
		Direction:     model.RequestDir,
	})
	assert.NoError(t, err)
	defer func() { _ = handle.Close() }()

	assert.NoError(t, bus.SendRequestMessage("request-service", "request-forwarded", nil))
	assert.NoError(t, bus.GetChannelManager().WaitForChannel("request-service"))
	assert.Len(t, mockServer.sentMessages, 1)
	assert.Equal(t, "/topic/request-service", mockServer.sentMessages[0].Destination)
	assert.Equal(t, "request-forwarded", string(mockServer.sentMessages[0].Payload))

	assert.NoError(t, bus.SendResponseMessage("request-service", "response-ignored", nil))
	assert.NoError(t, bus.GetChannelManager().WaitForChannel("request-service"))
	assert.Len(t, mockServer.sentMessages, 1)
}

func TestRouter_RejectsUnsupportedDirection(t *testing.T) {
	bus := newTestEventBus()
	fe, _ := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/topic"})

	handle, err := fe.Router().RegisterRoute(RouteSpec{
		BusChannel: "error-service",
		Direction:  model.ErrorDir,
	})

	assert.Nil(t, handle)
	assert.EqualError(t, err, "unsupported route direction 2")
}

func TestRouter_RegisterRouteReplacesSubscriptionForwarder(t *testing.T) {
	bus := newTestEventBus()
	fe, mockServer := newTestFabricEndpoint(bus, EndpointConfig{TopicPrefix: "/topic"})

	mockServer.subscribeHandlerFunction("con1", "sub1", "/topic/test-service", nil)

	fe.chanLock.RLock()
	chanMap := fe.chanMappings["test-service"]
	assert.NotNil(t, chanMap)
	assert.NotNil(t, chanMap.handler)
	assert.True(t, chanMap.autoCreated)
	assert.True(t, chanMap.subs["con1#sub1"])
	fe.chanLock.RUnlock()

	assert.NoError(t, bus.SendResponseMessage("test-service", "before-route", nil))
	assert.NoError(t, bus.GetChannelManager().WaitForChannel("test-service"))
	assert.Len(t, mockServer.sentMessages, 1)
	assert.Equal(t, "before-route", string(mockServer.sentMessages[0].Payload))

	handle, err := fe.Router().RegisterRoute(RouteSpec{
		BusChannel:    "test-service",
		DefaultDest:   TopicDestination(&fe.config, "test-service"),
		AllowOverride: true,
		Direction:     model.ResponseDir,
	})
	assert.NoError(t, err)
	defer func() { _ = handle.Close() }()

	fe.chanLock.RLock()
	chanMap = fe.chanMappings["test-service"]
	assert.NotNil(t, chanMap)
	assert.Nil(t, chanMap.handler)
	assert.False(t, chanMap.autoCreated)
	assert.True(t, chanMap.subs["con1#sub1"])
	fe.chanLock.RUnlock()

	assert.NoError(t, bus.SendResponseMessage("test-service", "after-route", nil))
	assert.NoError(t, bus.GetChannelManager().WaitForChannel("test-service"))
	assert.Len(t, mockServer.sentMessages, 2)
	assert.Equal(t, "after-route", string(mockServer.sentMessages[1].Payload))
}
