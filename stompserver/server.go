// Copyright 2019 VMware, Inc. All rights reserved. -- VMware Confidential

package stompserver

import (
    "github.com/go-stomp/stomp/frame"
    "log"
    "strconv"
    "sync"
)

type SubscribeHandlerFunction func(conId string, subId string, destination string, frame *frame.Frame)

type UnsubscribeHandlerFunction func(conId string, subId string, destination string)

type ApplicationRequestHandlerFunction func(destination string, message []byte, connectionId string)

type StompServer interface {
    // starts the server
    Start()
    // stops the server
    Stop()
    // sends a message to a given stomp topic destination
    SendMessage(destination string, messageBody []byte)
    // sends a message to a single connection client
    SendMessageToClient(connectionId string, destination string, messageBody []byte)
    // registers a callback for stomp subscribe events
    OnSubscribeEvent(callback SubscribeHandlerFunction)
    // registers a callback for stomp unsubscribe events
    OnUnsubscribeEvent(callback UnsubscribeHandlerFunction)
    // registers a callback for application requests
    OnApplicationRequest(callback ApplicationRequestHandlerFunction)
}

type eventType int
const (
    connectionStarting eventType = iota
    connectionEstablished
    connectionClosed
    subscribeToTopic
    unsubscribeFromTopic
    incomingMessage
)

type connEvent struct {
    eventType eventType
    conn StompConn
    destination string
    sub *subscription
    frame *frame.Frame
}

type apiEventType int
const (
    closeServer apiEventType = iota
    sendMessage
    sendPrivateMessage
)

type apiEvent struct {
    eventType   apiEventType
    connId      string
    frame       *frame.Frame
    destination string
}

type connSubscriptions struct {
    conn StompConn
    subscriptions map[string]*subscription
}

func newConnSubscriptions(conn StompConn) *connSubscriptions{
    return &connSubscriptions{
        conn: conn,
        subscriptions: make(map[string]*subscription),
    }
}

type stompServer struct {
    connectionListener RawConnectionListener
    connectionEvents chan *connEvent
    apiEvents chan *apiEvent
    running bool
    connectionsMap map[string]StompConn
    subscriptionsMap map[string] map[string]*connSubscriptions
    config StompConfig
    callbackLock sync.RWMutex
    subscribeCallbacks []SubscribeHandlerFunction
    unsubscribeCallbacks []UnsubscribeHandlerFunction
    applicationRequestCallbacks []ApplicationRequestHandlerFunction
}

func NewStompServer(listener RawConnectionListener, config StompConfig) StompServer {
    server := &stompServer{
        config:                      config,
        connectionListener:          listener,
        apiEvents:                   make(chan *apiEvent, 32),
        connectionsMap:              make(map[string]StompConn),
        connectionEvents:            make(chan *connEvent, 64),
        subscriptionsMap:            make(map[string]map[string]*connSubscriptions),
        subscribeCallbacks:          make([]SubscribeHandlerFunction, 0),
        unsubscribeCallbacks:        make([]UnsubscribeHandlerFunction, 0),
        applicationRequestCallbacks: make([]ApplicationRequestHandlerFunction, 0),
    }

    return server
}

func (s *stompServer) OnSubscribeEvent(callback SubscribeHandlerFunction) {
    s.callbackLock.Lock()
    defer s.callbackLock.Unlock()

    s.subscribeCallbacks = append(s.subscribeCallbacks, callback)
}

func (s *stompServer) OnUnsubscribeEvent(callback UnsubscribeHandlerFunction) {
    s.callbackLock.Lock()
    defer s.callbackLock.Unlock()

    s.unsubscribeCallbacks = append(s.unsubscribeCallbacks, callback)
}

func (s *stompServer) OnApplicationRequest(callback ApplicationRequestHandlerFunction) {
    s.callbackLock.Lock()
    defer s.callbackLock.Unlock()

    s.applicationRequestCallbacks = append(s.applicationRequestCallbacks, callback)
}

func (s *stompServer) SendMessage(destination string, messageBody []byte) {

    // create send frame.
    f := frame.New(frame.MESSAGE,
        frame.Destination, destination,
        frame.ContentLength, strconv.Itoa(len(messageBody)),
        frame.ContentType, "application/json;charset=UTF-8")

    f.Body = messageBody

    s.apiEvents <- &apiEvent{
        eventType: sendMessage,
        destination: destination,
        frame: f,
    }
}

func (s *stompServer) SendMessageToClient(connectionId string, destination string, messageBody []byte) {

    // create send frame.
    f := frame.New(frame.MESSAGE,
        frame.Destination, destination,
        frame.ContentLength, strconv.Itoa(len(messageBody)),
        frame.ContentType, "application/json;charset=UTF-8")

    f.Body = messageBody

    s.apiEvents <- &apiEvent{
        eventType: sendPrivateMessage,
        destination: destination,
        frame: f,
        connId: connectionId,
    }
}

func (s *stompServer) Start() {
    if s.running {
        return
    }

    s.running = true
    go s.waitForConnections()
    s.run()
}

func (s *stompServer) Stop() {
    if s.running {
        s.running = false
        s.apiEvents <- &apiEvent{
            eventType: closeServer,
        }
    }
}

func (s *stompServer) waitForConnections() {
    for {
         rawConn, err :=  s.connectionListener.Accept()
         if err != nil {
             log.Println("Failed to establish client connection:", err)
         } else {
             c := NewStompConn(rawConn, s.config, s.connectionEvents)

             s.connectionEvents <- &connEvent{
                 conn:      c,
                 eventType: connectionStarting,
             }
         }
    }
}

func (s *stompServer) run() {
    for {
        select {

        case apiEvent, _ := <- s.apiEvents:
            if apiEvent.eventType == closeServer {
                s.connectionListener.Close()
                // close all open connections
                for _, c := range s.connectionsMap {
                    c.Close()
                }
                s.connectionsMap = make(map[string]StompConn)
                return
            } else if apiEvent.eventType == sendMessage {
                s.sendFrame(apiEvent.destination, apiEvent.frame)
            } else if apiEvent.eventType == sendPrivateMessage {
                s.sendFrameToClient(apiEvent.connId, apiEvent.destination, apiEvent.frame)
            }

        case e, _ := <- s.connectionEvents:
            s.handleConnectionEvent(e)
        }
    }
}

func (s *stompServer) handleConnectionEvent(e *connEvent) {

    s.callbackLock.RLock()
    defer s.callbackLock.RUnlock()

    switch e.eventType {
    case connectionStarting:
        s.connectionsMap[e.conn.GetId()] = e.conn

    case connectionClosed:
        delete(s.connectionsMap, e.conn.GetId())
        for _, connSubscriptions := range s.subscriptionsMap {
            conSub, ok := connSubscriptions[e.conn.GetId()]
            if ok {
                delete(connSubscriptions, e.conn.GetId())
                for _, sub := range conSub.subscriptions {
                    for _, callback := range s.unsubscribeCallbacks {
                        callback(e.conn.GetId(), sub.id, sub.destination)
                    }
                }
            }
        }

    case subscribeToTopic:
        subsMap, ok := s.subscriptionsMap[e.destination]
        if !ok {
            subsMap = make(map[string]*connSubscriptions)
            s.subscriptionsMap[e.destination] = subsMap
        }
        var conSub *connSubscriptions
        conSub, ok = subsMap[e.conn.GetId()]
        if !ok {
            conSub = newConnSubscriptions(e.conn)
            subsMap[e.conn.GetId()] = conSub
        }
        conSub.subscriptions[e.sub.id] = e.sub

        // notify listeners
        for _, callback := range s.subscribeCallbacks {
            callback(e.conn.GetId(), e.sub.id, e.destination, e.frame)
        }

    case unsubscribeFromTopic:
        subs, ok := s.subscriptionsMap[e.destination]
        if ok {
            var conSub *connSubscriptions
            conSub, ok = subs[e.conn.GetId()]
            if ok {
                _, ok = conSub.subscriptions[e.sub.id]
                if ok {
                    delete(conSub.subscriptions, e.sub.id)
                    // notify listeners
                    for _, callback := range s.unsubscribeCallbacks {
                        callback(e.conn.GetId(), e.sub.id, e.destination)
                    }
                }
            }
        }

    case incomingMessage:
        s.sendFrame(e.destination, e.frame)

        if s.config.IsAppRequestDestination(e.destination) && e.conn != nil {
            // notify app listeners
            for _, callback := range s.applicationRequestCallbacks {
                callback(e.destination, e.frame.Body, e.conn.GetId())
            }
        }
    }
}

func (s *stompServer) sendFrame(dest string, f *frame.Frame) {
    subsMap, ok := s.subscriptionsMap[dest]
    if ok {
        for _, connSub := range subsMap {
            for _, sub := range connSub.subscriptions {
                connSub.conn.SendFrameToSubscription(f.Clone(), sub)
            }
        }
    }
}

func (s *stompServer) sendFrameToClient(conId string, dest string, f *frame.Frame) {
    subsMap, ok := s.subscriptionsMap[dest]
    if ok {
        connSubscriptions, ok := subsMap[conId]
        if ok {
            for _, sub := range connSubscriptions.subscriptions {
                connSubscriptions.conn.SendFrameToSubscription(f.Clone(), sub)
            }
        }
    }
}
