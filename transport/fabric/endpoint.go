// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package fabric

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/go-stomp/stomp/v3/frame"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/stompserver"
)

const (
	// STOMP_SESSION_NOTIFY_CHANNEL carries STOMP session lifecycle events on the event bus.
	STOMP_SESSION_NOTIFY_CHANNEL = bus.RANCH_INTERNAL_CHANNEL_PREFIX + "stomp-session-notify"
)

// Endpoint exposes the lifecycle and routing API for a Fabric STOMP endpoint.
type Endpoint interface {
	Start(ctx context.Context) error
	Stop() error
	Router() MessageRouter
	Ready() <-chan struct{}
	StompServer() stompserver.StompServer
}

type channelMapping struct {
	subs        map[string]bool
	handler     bus.MessageHandler
	autoCreated bool
}

// StompSessionEvent describes a STOMP connection lifecycle event.
type StompSessionEvent struct {
	Id        string
	EventType stompserver.StompSessionEventType
}

type fabricEndpoint struct {
	server       stompserver.StompServer
	bus          bus.EventBus
	router       *messageRouter
	config       EndpointConfig
	chanLock     sync.RWMutex
	chanMappings map[string]*channelMapping
	logger       *slog.Logger
	readyCh      chan struct{}
	readyOnce    sync.Once
	startOnce    sync.Once
	stopOnce     sync.Once
}

func ensureTrailingSlash(s string) string {
	if s != "" && !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

// New creates a Fabric endpoint backed by a STOMP connection listener.
func New(bus bus.EventBus,
	conListener stompserver.RawConnectionListener, config EndpointConfig) (Endpoint, error) {

	if configErr := config.validate(); configErr != nil {
		return nil, configErr
	}
	return newFabricEndpoint(bus, conListener, config), nil
}

func newFabricEndpoint(bus bus.EventBus,
	conListener stompserver.RawConnectionListener, config EndpointConfig) *fabricEndpoint {

	config.TopicPrefix = ensureTrailingSlash(config.TopicPrefix)
	config.AppRequestPrefix = ensureTrailingSlash(config.AppRequestPrefix)
	config.AppRequestQueuePrefix = ensureTrailingSlash(config.AppRequestQueuePrefix)
	config.UserQueuePrefix = ensureTrailingSlash(config.UserQueuePrefix)

	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	config.Logger = logger

	stompConf := stompserver.NewStompConfig(config.Heartbeat,
		[]string{config.AppRequestPrefix, config.AppRequestQueuePrefix})
	stompConf.SetLogger(logger)

	// if configured, set the stomp broker middleware registry.
	if config.MiddlewareRegistry != nil {
		stompConf.SetMiddlewareRegistry(config.MiddlewareRegistry)
	}

	fep := &fabricEndpoint{
		server:       stompserver.NewStompServer(conListener, stompConf),
		config:       config,
		bus:          bus,
		chanMappings: make(map[string]*channelMapping),
		logger:       logger,
		readyCh:      make(chan struct{}),
	}

	fep.router = newMessageRouter(fep, bus, fep.logger)
	fep.initHandlers()
	return fep
}

func (fe *fabricEndpoint) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-fe.readyCh:
		return nil
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	fe.startOnce.Do(func() {
		fe.server.SetConnectionEventCallback(stompserver.ConnectionStarting, func(connEvent *stompserver.ConnEvent) {
			fe.sendSessionEvent(connEvent.ConnId, stompserver.ConnectionStarting)
		})
		fe.server.SetConnectionEventCallback(stompserver.ConnectionClosed, func(connEvent *stompserver.ConnEvent) {
			fe.sendSessionEvent(connEvent.ConnId, stompserver.ConnectionClosed)
		})
		fe.server.SetConnectionEventCallback(stompserver.UnsubscribeFromTopic, func(connEvent *stompserver.ConnEvent) {
			fe.sendSessionEvent(connEvent.ConnId, stompserver.UnsubscribeFromTopic)
		})

		go fe.server.Start()

		<-fe.server.Ready()

		fe.readyOnce.Do(func() {
			close(fe.readyCh)
		})

	})
	return nil
}

func (fe *fabricEndpoint) sendSessionEvent(connId string, eventType stompserver.StompSessionEventType) {
	if fe.bus == nil {
		return
	}
	_ = fe.bus.SendResponseMessage(STOMP_SESSION_NOTIFY_CHANNEL, &StompSessionEvent{
		Id:        connId,
		EventType: eventType,
	}, nil)
}

func (fe *fabricEndpoint) Stop() error {
	fe.stopOnce.Do(func() {
		if fe.router != nil {
			_ = fe.router.Close()
		}
		fe.server.Stop()
	})
	return nil
}

func (fe *fabricEndpoint) Router() MessageRouter {
	return fe.router
}

func (fe *fabricEndpoint) Ready() <-chan struct{} {
	return fe.readyCh
}

func (fe *fabricEndpoint) StompServer() stompserver.StompServer {
	return fe.server
}

func (fe *fabricEndpoint) initHandlers() {
	fe.server.OnApplicationRequest(fe.bridgeMessage)
	fe.server.OnSubscribeEvent(fe.addSubscription)
	fe.server.OnUnsubscribeEvent(fe.removeSubscription)
}

func (fe *fabricEndpoint) addSubscription(
	conId string, subId string, destination string, frame *frame.Frame) {

	channelName, ok := fe.getChannelNameFromSubscription(destination)
	if !ok {
		return
	}

	// if destination is a protected channel do not establish a subscription
	// (we don't want any clients to be sending messages to internal channels)
	if isProtectedDestination(channelName) {
		return
	}

	fe.chanLock.Lock()
	defer fe.chanLock.Unlock()

	chanMap, ok := fe.chanMappings[channelName]
	if !ok {
		var messageHandler bus.MessageHandler
		var autoCreated bool

		if fe.router == nil || !fe.router.hasRoute(channelName) {
			if fe.bus == nil {
				return
			}

			// Before a service route exists, topic subscriptions need a fallback
			// forwarder so clients can subscribe first and receive later responses.
			var err error
			messageHandler, err = fe.bus.ListenStream(channelName)
			if messageHandler == nil || err != nil {
				fe.bus.GetChannelManager().CreateChannel(channelName)
				messageHandler, err = fe.bus.ListenStream(channelName)
				if messageHandler == nil || err != nil {
					fe.logger.Warn("unable to auto-create channel for destination", "destination", destination)
					return
				}
				autoCreated = true
			}
			messageHandler.Handle(
				func(message *model.Message) {
					fe.forwardMessage(channelName, fe.config.TopicPrefix+channelName, true, message)
				},
				func(e error) {
					fe.server.SendMessage(destination, []byte(e.Error()))
				})
		}

		chanMap = &channelMapping{
			subs:        make(map[string]bool),
			handler:     messageHandler,
			autoCreated: autoCreated,
		}

		fe.chanMappings[channelName] = chanMap
	}
	chanMap.subs[conId+"#"+subId] = true
	if fe.bus != nil {
		fe.bus.SendMonitorEvent(FabricEndpointSubscribeEvt, channelName, nil)
	}
}

func (fe *fabricEndpoint) detachSubscriptionForwarderLocked(channelName string) bus.MessageHandler {
	if chanMap, ok := fe.chanMappings[channelName]; ok {
		// Route registration replaces fallback forwarding. Keep the subscriber
		// mapping, but return the handler so the router can close it after the
		// route is visible to concurrent subscribers.
		handler := chanMap.handler
		chanMap.handler = nil
		chanMap.autoCreated = false
		return handler
	}
	return nil
}

func (fe *fabricEndpoint) routeConfig() *EndpointConfig {
	cfg := fe.config
	return &cfg
}

func (fe *fabricEndpoint) detachSubscriptionForwarder(channelName string) bus.MessageHandler {
	fe.chanLock.Lock()
	defer fe.chanLock.Unlock()
	return fe.detachSubscriptionForwarderLocked(channelName)
}

func (fe *fabricEndpoint) sendRouteError(destination string, err error) {
	fe.logger.Error("fabric route handler returned an error", "destination", destination, "err", err)
	payload, marshalErr := json.Marshal(&model.Response{
		Error:        true,
		ErrorCode:    500,
		ErrorMessage: "fabric route error",
	})
	if marshalErr != nil {
		fe.logger.Error("failed to marshal fabric route error response", "destination", destination, "err", marshalErr)
		return
	}
	fe.server.SendMessage(destination, payload)
}

func (fe *fabricEndpoint) forwardRouteMessage(
	channelName, defaultDestination string, allowOverride bool, message *model.Message) {
	fe.forwardMessage(channelName, defaultDestination, allowOverride, message)
}

func (fe *fabricEndpoint) forwardMessage(channelName, defaultDestination string, allowOverride bool, message *model.Message) {
	data, err := marshalMessagePayload(message)
	if err != nil {
		fe.logger.Error("failed to marshal message payload", "channel", channelName, "err", err)
		return
	}

	resp, ok := fe.convertPayloadToResponseObj(message)
	if allowOverride && ok && resp != nil && resp.BrokerDestination != nil {
		fe.logger.Debug("routing message to specific broker",
			"connectionId", resp.BrokerDestination.ConnectionId,
			"destination", resp.BrokerDestination.Destination,
			"channel", channelName)
		fe.server.SendMessageToClient(
			resp.BrokerDestination.ConnectionId,
			resp.BrokerDestination.Destination,
			data)
		return
	}

	// BrokerDestination overrides only work when the service returns model.Response directly.
	// A nested response has already lost that routing context, so warn before broadcasting.
	if respPayload, isResp := message.Payload.(*model.Response); isResp && respPayload.BrokerDestination != nil {
		fe.logger.Warn("double-wrapped Response detected",
			"channel", channelName,
			"intendedDestination", respPayload.BrokerDestination.Destination,
			"connectionId", respPayload.BrokerDestination.ConnectionId,
			"payloadType", fmt.Sprintf("%T", respPayload.Payload))
	} else if message.Payload != nil && !ok {
		fe.logger.Debug("message payload is not model.Response; broadcasting to topic",
			"payloadType", fmt.Sprintf("%T", message.Payload),
			"channel", channelName)
	}
	fe.server.SendMessage(defaultDestination, data)
}

func (fe *fabricEndpoint) convertPayloadToResponseObj(message *model.Message) (*model.Response, bool) {
	var resp model.Response
	var ok bool

	resp, ok = message.Payload.(model.Response)
	if ok {
		return &resp, true
	}

	var respPtr *model.Response
	respPtr, ok = message.Payload.(*model.Response)
	if ok {
		return respPtr, true
	}

	if message.Payload != nil {
		fe.logger.Debug("failed to convert message payload to Response",
			"payloadType", fmt.Sprintf("%T", message.Payload),
			"channel", message.Channel)
	}

	return nil, false
}

func marshalMessagePayload(message *model.Message) ([]byte, error) {
	stringPayload, ok := message.Payload.(string)
	if ok {
		return []byte(stringPayload), nil
	}
	bytePayload, ok := message.Payload.([]byte)
	if ok {
		return bytePayload, nil
	}
	return json.Marshal(message.Payload)
}

func (fe *fabricEndpoint) removeSubscription(conId string, subId string, destination string) {

	channelName, ok := fe.getChannelNameFromSubscription(destination)
	if !ok {
		return
	}

	fe.chanLock.Lock()
	defer fe.chanLock.Unlock()

	chanMap, ok := fe.chanMappings[channelName]
	if ok {
		mappingId := conId + "#" + subId
		if chanMap.subs[mappingId] {
			delete(chanMap.subs, mappingId)
			if len(chanMap.subs) == 0 {
				// if this was the last subscription to the channel,
				// close the message handler and remove the channel mapping
				if chanMap.handler != nil {
					chanMap.handler.Close()
				}
				delete(fe.chanMappings, channelName)
				if chanMap.autoCreated {
					fe.bus.GetChannelManager().DestroyChannel(channelName)
				}
			}
			if fe.bus != nil {
				fe.bus.SendMonitorEvent(FabricEndpointUnsubscribeEvt, channelName, nil)
			}
		}
	}
}

func (fe *fabricEndpoint) bridgeMessage(destination string, message []byte, connectionId string) {
	var channelName string
	isPrivateRequest := false

	switch {
	case fe.config.AppRequestQueuePrefix != "" && strings.HasPrefix(destination, fe.config.AppRequestQueuePrefix):
		channelName = destination[len(fe.config.AppRequestQueuePrefix):]
		isPrivateRequest = true
	case fe.config.AppRequestPrefix != "" && strings.HasPrefix(destination, fe.config.AppRequestPrefix):
		channelName = destination[len(fe.config.AppRequestPrefix):]
	default:
		return
	}

	var req model.Request
	err := json.Unmarshal(message, &req)
	if err != nil {
		fe.logger.Warn("failed to deserialize request for channel", "channel", channelName)
		return
	}

	if isPrivateRequest {
		// Private app requests should answer only the originating connection.
		req.BrokerDestination = &model.BrokerDestinationConfig{
			Destination:  fe.config.UserQueuePrefix + channelName,
			ConnectionId: connectionId,
		}
	}

	_ = fe.bus.SendRequestMessage(channelName, &req, nil)
}

func (fe *fabricEndpoint) getChannelNameFromSubscription(destination string) (channelName string, ok bool) {
	if strings.HasPrefix(destination, fe.config.TopicPrefix) {
		return destination[len(fe.config.TopicPrefix):], true
	}

	if fe.config.UserQueuePrefix != "" && strings.HasPrefix(destination, fe.config.UserQueuePrefix) {
		return destination[len(fe.config.UserQueuePrefix):], true
	}
	return "", false
}

// isProtectedDestination checks if the destination is protected. this utility function is used to
// prevent messages being from clients to the protected destinations. such examples would be
// internal bus channels prefixed with _ranchInternal/
func isProtectedDestination(destination string) bool {
	return strings.HasPrefix(destination, bus.RANCH_INTERNAL_CHANNEL_PREFIX)
}
