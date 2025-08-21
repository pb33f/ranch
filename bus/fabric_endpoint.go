// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
    "encoding/json"
    "fmt"
    "github.com/go-stomp/stomp/v3/frame"
     "log/slog"
    "github.com/pb33f/ranch/model"
    "github.com/pb33f/ranch/stompserver"
    "strings"
    "sync"
)

const (
    STOMP_SESSION_NOTIFY_CHANNEL = RANCH_INTERNAL_CHANNEL_PREFIX + "stomp-session-notify"
)

type EndpointConfig struct {
    // Prefix for public topics e.g. "/topic"
    TopicPrefix string
    // Prefix for user queues e.g. "/user/queue"
    UserQueuePrefix string
    // Prefix used for public application requests e.g. "/pub"
    AppRequestPrefix string
    // Prefix used for "private" application requests e.g. "/pub/queue"
    // Requests sent to destinations prefixed with the AppRequestQueuePrefix
    // should generate responses sent to single client queue.
    // E.g. if a client sends a request to the "/pub/queue/sample-channel" destination
    // the application should sent the response only to this client on the
    // "/user/queue/sample-channel" destination.
    // This behavior will mimic the Spring SimpleMessageBroker implementation.
    AppRequestQueuePrefix string
    Heartbeat             int64

    // Custom middleware for broker commands and destinations.
    MiddlewareRegistry stompserver.MiddlewareRegistry
}

func (ec *EndpointConfig) validate() error {
    if ec.TopicPrefix == "" || !strings.HasPrefix(ec.TopicPrefix, "/") {
        return fmt.Errorf("invalid TopicPrefix")
    }

    if ec.AppRequestQueuePrefix != "" && ec.UserQueuePrefix == "" {
        return fmt.Errorf("missing UserQueuePrefix")
    }

    return nil
}

type FabricEndpoint interface {
    Start()
    Stop()
    GetStompServer() stompserver.StompServer
}

type channelMapping struct {
    subs        map[string]bool
    handler     MessageHandler
    autoCreated bool
}

type StompSessionEvent struct {
    Id        string
    EventType stompserver.StompSessionEventType
}

type fabricEndpoint struct {
    server       stompserver.StompServer
    bus          EventBus
    config       EndpointConfig
    chanLock     sync.RWMutex
    chanMappings map[string]*channelMapping
    logger       *slog.Logger
}

func addPrefixIfNotEmpty(s string, prefix string) string {
    if s != "" && !strings.HasSuffix(s, prefix) {
        return s + prefix
    }
    return s
}

func newFabricEndpoint(bus EventBus,
    conListener stompserver.RawConnectionListener, config EndpointConfig) FabricEndpoint {

    config.TopicPrefix = addPrefixIfNotEmpty(config.TopicPrefix, "/")
    config.AppRequestPrefix = addPrefixIfNotEmpty(config.AppRequestPrefix, "/")
    config.AppRequestQueuePrefix = addPrefixIfNotEmpty(config.AppRequestQueuePrefix, "/")
    config.UserQueuePrefix = addPrefixIfNotEmpty(config.UserQueuePrefix, "/")

    stompConf := stompserver.NewStompConfig(config.Heartbeat,
        []string{config.AppRequestPrefix, config.AppRequestQueuePrefix})

    // if configured, set the stomp broker middleware registry.
    if config.MiddlewareRegistry != nil {
        stompConf.SetMiddlewareRegistry(config.MiddlewareRegistry)
    }

    fep := &fabricEndpoint{
        server:       stompserver.NewStompServer(conListener, stompConf),
        config:       config,
        bus:          bus,
        chanMappings: make(map[string]*channelMapping),
        logger:       slog.Default(),
    }

    fep.initHandlers()
    return fep
}

func (fe *fabricEndpoint) Start() {
    fe.server.SetConnectionEventCallback(stompserver.ConnectionStarting, func(connEvent *stompserver.ConnEvent) {
        busInstance.SendResponseMessage(STOMP_SESSION_NOTIFY_CHANNEL, &StompSessionEvent{
            Id:        connEvent.ConnId,
            EventType: stompserver.ConnectionStarting,
        }, nil)
    })
    fe.server.SetConnectionEventCallback(stompserver.ConnectionClosed, func(connEvent *stompserver.ConnEvent) {
        busInstance.SendResponseMessage(STOMP_SESSION_NOTIFY_CHANNEL, &StompSessionEvent{
            Id:        connEvent.ConnId,
            EventType: stompserver.ConnectionClosed,
        }, nil)
    })
    fe.server.SetConnectionEventCallback(stompserver.UnsubscribeFromTopic, func(connEvent *stompserver.ConnEvent) {
        busInstance.SendResponseMessage(STOMP_SESSION_NOTIFY_CHANNEL, &StompSessionEvent{
            Id:        connEvent.ConnId,
            EventType: stompserver.UnsubscribeFromTopic,
        }, nil)
    })
    fe.server.Start()
}

func (fe *fabricEndpoint) Stop() {
    fe.server.Stop()
}

func (fe *fabricEndpoint) GetStompServer() stompserver.StompServer {
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
        messageHandler, err := fe.bus.ListenStream(channelName)
        var autoCreated = false
        if messageHandler == nil || err != nil {
            fe.bus.GetChannelManager().CreateChannel(channelName)
            messageHandler, err = fe.bus.ListenStream(channelName)
            if messageHandler == nil || err != nil {
                fe.logger.Warn("Unable to auto-create channel for destination: %s", destination)
                return
            }
            autoCreated = true
        }
        messageHandler.Handle(
            func(message *model.Message) {
                data, err := marshalMessagePayload(message)
                if err == nil {
                    resp, ok := convertPayloadToResponseObj(message)
                    if ok && resp != nil && resp.BrokerDestination != nil {
                         fe.logger.Debug("Routing message to specific broker. ConnectionId: %s, Destination: %s, Channel: %s",
                            resp.BrokerDestination.ConnectionId, resp.BrokerDestination.Destination, channelName)
                        fe.server.SendMessageToClient(
                            resp.BrokerDestination.ConnectionId,
                            resp.BrokerDestination.Destination,
                            data)
                    } else {
                        // Check if the payload is a nested Response that indicates double-wrapping
                        if respPayload, isResp := message.Payload.(*model.Response); isResp && respPayload.BrokerDestination != nil {
                            // This is a double-wrapped Response - the developer passed a Response to SendResponse
                             fe.logger.Warn("Double-wrapped Response detected. When using SendResponse with BrokerDestination, "+
                                "pass your data directly as payload, not wrapped in a Response object. "+
                                "Channel: %s, Intended destination: %s, ConnectionId: %s, Actual payload type: %T",
                                channelName, respPayload.BrokerDestination.Destination, 
                                respPayload.BrokerDestination.ConnectionId, respPayload.Payload)
                        } else if message.Payload != nil && !ok {
                            // Log helpful message when payload can't be converted to Response
                             fe.logger.Debug("Message payload is type %T (not model.Response), broadcasting to topic. "+
                                "To route to specific broker connection, SendResponse must receive a model.Response object "+
                                "with BrokerDestination set. Channel: %s", message.Payload, channelName)
                        }
                        fe.server.SendMessage(fe.config.TopicPrefix+channelName, data)
                    }
                } else {
                     fe.logger.Error("Failed to marshal message payload for channel %s: %v", channelName, err)
                }
            },
            func(e error) {
                fe.server.SendMessage(destination, []byte(e.Error()))
            })

        chanMap = &channelMapping{
            subs:        make(map[string]bool),
            handler:     messageHandler,
            autoCreated: autoCreated,
        }

        fe.chanMappings[channelName] = chanMap
    }
    chanMap.subs[conId+"#"+subId] = true
    fe.bus.SendMonitorEvent(FabricEndpointSubscribeEvt, channelName, nil)
}

func convertPayloadToResponseObj(message *model.Message) (*model.Response, bool) {
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

    // Log the actual type for debugging when conversion fails
    if message.Payload != nil {
         slog.Default().Debug("Failed to convert message payload to Response. Actual type: %T, Channel: %s",
            message.Payload, message.Channel)
    }

    return nil, false
}

func marshalMessagePayload(message *model.Message) ([]byte, error) {
    // don't marshal string and []byte payloads
    stringPayload, ok := message.Payload.(string)
    if ok {
        return []byte(stringPayload), nil
    }
    bytePayload, ok := message.Payload.([]byte)
    if ok {
        return bytePayload, nil
    }
    // encode the message payload as JSON
    return json.Marshal(message.Payload)
}

// marshalPayload marshals a payload, respecting the marshal flag
func marshalPayload(payload interface{}, shouldMarshal bool) ([]byte, error) {
    // If marshal flag is false, try to return as string or byte array
    if !shouldMarshal {
        if str, ok := payload.(string); ok {
            return []byte(str), nil
        }
        if bytes, ok := payload.([]byte); ok {
            return bytes, nil
        }
    }
    
    // Otherwise marshal to JSON
    return json.Marshal(payload)
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
                chanMap.handler.Close()
                delete(fe.chanMappings, channelName)
                if chanMap.autoCreated {
                    fe.bus.GetChannelManager().DestroyChannel(channelName)
                }
            }
            fe.bus.SendMonitorEvent(FabricEndpointUnsubscribeEvt, channelName, nil)
        }
    }
}

func (fe *fabricEndpoint) bridgeMessage(destination string, message []byte, connectionId string) {
    var channelName string
    isPrivateRequest := false

    if fe.config.AppRequestQueuePrefix != "" && strings.HasPrefix(destination, fe.config.AppRequestQueuePrefix) {
        channelName = destination[len(fe.config.AppRequestQueuePrefix):]
        isPrivateRequest = true
    } else if fe.config.AppRequestPrefix != "" && strings.HasPrefix(destination, fe.config.AppRequestPrefix) {
        channelName = destination[len(fe.config.AppRequestPrefix):]
    } else {
        return
    }

    var req model.Request
    err := json.Unmarshal(message, &req)
    if err != nil {
         fe.logger.Warn("Failed to deserialize request for channel %s", channelName)
        return
    }

    if isPrivateRequest {
        req.BrokerDestination = &model.BrokerDestinationConfig{
            Destination:  fe.config.UserQueuePrefix + channelName,
            ConnectionId: connectionId,
        }
    }

    fe.bus.SendRequestMessage(channelName, &req, nil)
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
// internal bus channels prefixed with _transportInternal/
func isProtectedDestination(destination string) bool {
    return strings.HasPrefix(destination, RANCH_INTERNAL_CHANNEL_PREFIX)
}
