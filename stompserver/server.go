// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/go-stomp/stomp/v3/frame"
)

// SubscribeHandlerFunction handles STOMP SUBSCRIBE frames.
type SubscribeHandlerFunction func(conId string, subId string, destination string, frame *frame.Frame)

// UnsubscribeHandlerFunction handles STOMP UNSUBSCRIBE frames.
type UnsubscribeHandlerFunction func(conId string, subId string, destination string)

// ApplicationRequestHandlerFunction handles SEND frames targeting application destinations.
type ApplicationRequestHandlerFunction func(destination string, message []byte, connectionId string)

// ipBlockingCheckerSetter is an optional interface for listeners that support IP blocking
type ipBlockingCheckerSetter interface {
	SetIPBlockingChecker(checker IPBlockingChecker)
}

// StompServer accepts STOMP clients and routes frames to registered callbacks.
type StompServer interface {
	// starts the server
	Start()
	// stops the server
	Stop()
	// Ready is closed when the server is about to consume accepted connections.
	Ready() <-chan struct{}
	// sends a message to a given stomp topic destination
	SendMessage(destination string, messageBody []byte)
	// sends a message to a single connection client
	SendMessageToClient(connectionId string, destination string, messageBody []byte)
	// closes all connections from a specific IP address with a custom error message
	CloseConnectionsByIP(ip string, errorMessage string)
	// sets the IP blocking checker that gets called before allowing connections
	SetIPBlockingChecker(checker IPBlockingChecker)
	// registers a callback for stomp subscribe events
	OnSubscribeEvent(callback SubscribeHandlerFunction)
	// registers a callback for stomp unsubscribe events
	OnUnsubscribeEvent(callback UnsubscribeHandlerFunction)
	// registers a callback for application requests
	OnApplicationRequest(callback ApplicationRequestHandlerFunction)
	// SetConnectionEventCallback is used to set up a callback when certain STOMP session events happen
	// such as ConnectionStarting, ConnectionClosed, SubscribeToTopic, UnsubscribeFromTopic and IncomingMessage.
	SetConnectionEventCallback(connEventType StompSessionEventType, cb func(connEvent *ConnEvent))
}

// StompSessionEventType identifies a STOMP connection or subscription event.
type StompSessionEventType int

const (
	// ConnectionStarting is emitted before a new connection starts processing.
	ConnectionStarting StompSessionEventType = iota
	// ConnectionEstablished is emitted after a connection is established.
	ConnectionEstablished
	// ConnectionClosed is emitted after a connection closes.
	ConnectionClosed
	// SubscribeToTopic is emitted after a client subscribes to a destination.
	SubscribeToTopic
	// UnsubscribeFromTopic is emitted after a client unsubscribes from a destination.
	UnsubscribeFromTopic
	// IncomingMessage is emitted when a client sends an application message.
	IncomingMessage
)

// ConnEvent carries STOMP connection event details.
type ConnEvent struct {
	ConnId      string
	eventType   StompSessionEventType
	conn        StompConn
	destination string
	sub         *Subscription
	frame       *frame.Frame
}

type apiEventType int

const (
	closeServer apiEventType = iota
	sendMessage
	sendPrivateMessage
	closeConnectionByIP
)

type apiEvent struct {
	eventType    apiEventType
	connId       string
	targetIP     string
	frame        *frame.Frame
	destination  string
	errorMessage string
}

type connSubscriptions struct {
	conn          StompConn
	subscriptions map[string]*Subscription
}

func newConnSubscriptions(conn StompConn) *connSubscriptions {
	return &connSubscriptions{
		conn:          conn,
		subscriptions: make(map[string]*Subscription),
	}
}

type stompServer struct {
	connectionListener          RawConnectionListener
	connectionEvents            chan *ConnEvent
	connectionEventCallbacks    map[StompSessionEventType]func(event *ConnEvent)
	apiEvents                   chan *apiEvent
	running                     atomic.Bool
	connectionsMap              map[string]StompConn
	subscriptionsMap            map[string]map[string]*connSubscriptions
	config                      StompConfig
	callbackLock                sync.RWMutex
	subscribeCallbacks          []SubscribeHandlerFunction
	unsubscribeCallbacks        []UnsubscribeHandlerFunction
	applicationRequestCallbacks []ApplicationRequestHandlerFunction
	ipBlockingChecker           IPBlockingChecker
	ipBlockingCheckerMu         sync.RWMutex
	readyCh                     chan struct{}
	readyOnce                   sync.Once
	done                        chan struct{}
	doneOnce                    sync.Once
}

// NewStompServer creates a STOMP server around a raw connection listener.
func NewStompServer(listener RawConnectionListener, config StompConfig) StompServer {
	server := &stompServer{
		config:                      config,
		connectionListener:          listener,
		apiEvents:                   make(chan *apiEvent, 32),
		connectionsMap:              make(map[string]StompConn),
		connectionEvents:            make(chan *ConnEvent, 64),
		connectionEventCallbacks:    make(map[StompSessionEventType]func(event *ConnEvent)),
		subscriptionsMap:            make(map[string]map[string]*connSubscriptions),
		subscribeCallbacks:          make([]SubscribeHandlerFunction, 0),
		unsubscribeCallbacks:        make([]UnsubscribeHandlerFunction, 0),
		applicationRequestCallbacks: make([]ApplicationRequestHandlerFunction, 0),
		readyCh:                     make(chan struct{}),
		done:                        make(chan struct{}),
	}

	return server
}

func (s *stompServer) OnSubscribeEvent(callback SubscribeHandlerFunction) {
	registerCallback(&s.callbackLock, &s.subscribeCallbacks, callback)
}

func (s *stompServer) OnUnsubscribeEvent(callback UnsubscribeHandlerFunction) {
	registerCallback(&s.callbackLock, &s.unsubscribeCallbacks, callback)
}

func (s *stompServer) OnApplicationRequest(callback ApplicationRequestHandlerFunction) {
	registerCallback(&s.callbackLock, &s.applicationRequestCallbacks, callback)
}

func registerCallback[T any](lock *sync.RWMutex, callbacks *[]T, callback T) {
	lock.Lock()
	defer lock.Unlock()
	*callbacks = append(*callbacks, callback)
}

func cloneCallbacks[T any](callbacks []T) []T {
	if len(callbacks) == 0 {
		return nil
	}
	cloned := make([]T, len(callbacks))
	copy(cloned, callbacks)
	return cloned
}

func (s *stompServer) snapshotConnectionEventCallback(eventType StompSessionEventType) func(*ConnEvent) {
	s.callbackLock.RLock()
	defer s.callbackLock.RUnlock()
	return s.connectionEventCallbacks[eventType]
}

func (s *stompServer) snapshotSubscribeCallbacks() []SubscribeHandlerFunction {
	s.callbackLock.RLock()
	defer s.callbackLock.RUnlock()
	return cloneCallbacks(s.subscribeCallbacks)
}

func (s *stompServer) snapshotUnsubscribeCallbacks() []UnsubscribeHandlerFunction {
	s.callbackLock.RLock()
	defer s.callbackLock.RUnlock()
	return cloneCallbacks(s.unsubscribeCallbacks)
}

func (s *stompServer) snapshotApplicationRequestCallbacks() []ApplicationRequestHandlerFunction {
	s.callbackLock.RLock()
	defer s.callbackLock.RUnlock()
	return cloneCallbacks(s.applicationRequestCallbacks)
}

func (s *stompServer) SendMessage(destination string, messageBody []byte) {

	f := frame.New(frame.MESSAGE,
		frame.Destination, destination,
		frame.ContentLength, strconv.Itoa(len(messageBody)),
		frame.ContentType, "application/json;charset=UTF-8")

	f.Body = messageBody

	s.sendAPIEvent(&apiEvent{
		eventType:   sendMessage,
		destination: destination,
		frame:       f,
	})
}

func (s *stompServer) SendMessageToClient(connectionId string, destination string, messageBody []byte) {

	f := frame.New(frame.MESSAGE,
		frame.Destination, destination,
		frame.ContentLength, strconv.Itoa(len(messageBody)),
		frame.ContentType, "application/json;charset=UTF-8")

	f.Body = messageBody

	s.sendAPIEvent(&apiEvent{
		eventType:   sendPrivateMessage,
		destination: destination,
		frame:       f,
		connId:      connectionId,
	})
}

// extractIPFromAddress extracts IP address from "IP:port" or "[IPv6]:port" format
func extractIPFromAddress(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// If SplitHostPort fails, return original address (might already be clean IP)
		return address
	}
	return host
}

func (s *stompServer) CloseConnectionsByIP(ip string, errorMessage string) {
	s.sendAPIEvent(&apiEvent{
		eventType:    closeConnectionByIP,
		targetIP:     ip,
		errorMessage: errorMessage,
	})
}

func (s *stompServer) getIPBlockingChecker() IPBlockingChecker {
	s.ipBlockingCheckerMu.RLock()
	defer s.ipBlockingCheckerMu.RUnlock()
	return s.ipBlockingChecker
}

func (s *stompServer) SetIPBlockingChecker(checker IPBlockingChecker) {
	// store on server for generic (non-WebSocket) listener fallback
	s.ipBlockingCheckerMu.Lock()
	s.ipBlockingChecker = checker
	s.ipBlockingCheckerMu.Unlock()

	// set full interface on listener via optional interface (not concrete type)
	if setter, ok := s.connectionListener.(ipBlockingCheckerSetter); ok {
		setter.SetIPBlockingChecker(checker)
	}
}

func (s *stompServer) SetConnectionEventCallback(connEventType StompSessionEventType, cb func(connEvent *ConnEvent)) {
	s.callbackLock.Lock()
	defer s.callbackLock.Unlock()
	s.connectionEventCallbacks[connEventType] = cb
}

func (s *stompServer) Start() {
	select {
	case <-s.done:
		return
	default:
	}
	if !s.running.CompareAndSwap(false, true) {
		return
	}

	go s.waitForConnections()
	s.run()
}

func (s *stompServer) Stop() {
	if s.running.CompareAndSwap(true, false) {
		s.sendCloseEvent()
	}
}

func (s *stompServer) Ready() <-chan struct{} {
	return s.readyCh
}

func (s *stompServer) closeDone() {
	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func (s *stompServer) sendAPIEvent(event *apiEvent) bool {
	if !s.running.Load() {
		return false
	}
	select {
	case s.apiEvents <- event:
		return true
	case <-s.done:
		return false
	}
}

func (s *stompServer) sendCloseEvent() {
	event := &apiEvent{eventType: closeServer}
	select {
	case s.apiEvents <- event:
	case <-s.done:
	default:
		go func() {
			select {
			case s.apiEvents <- event:
			case <-s.done:
			}
		}()
	}
}

func (s *stompServer) sendConnectionEvent(event *ConnEvent) bool {
	if !s.running.Load() {
		return false
	}
	select {
	case s.connectionEvents <- event:
		return true
	case <-s.done:
		return false
	}
}

func (s *stompServer) waitForConnections() {
	s.readyOnce.Do(func() {
		close(s.readyCh)
	})

	for {
		if !s.running.Load() {
			return
		}

		rawConn, err := s.connectionListener.Accept()
		if err != nil {
			if s.running.Load() {
				s.config.Logger().Warn("failed to establish client connection", "err", err)
			}
			continue
		}

		// generic fallback block check for non-WebSocket listeners (e.g. TCP).
		// for WebSocket listeners, blocking and tracking already happened at the HTTP handler level.
		if checker := s.getIPBlockingChecker(); checker != nil {
			// normalize host:port to bare IP; nil headers is correct for TCP (no proxy headers)
			ip := checker.ExtractRealIP(rawConn.GetRemoteAddr(), nil)
			if blocked, errorMessage := checker.IsIPBlocked(ip); blocked {
				errorFrame := frame.New(frame.ERROR, frame.Message, errorMessage)
				if err := rawConn.WriteFrame(errorFrame); err != nil {
					s.config.Logger().Warn("failed to send ERROR frame to blocked IP", "ip", ip, "err", err)
				}
				_ = rawConn.Close()
				continue
			}
			// only track at server level for listeners that don't handle their own tracking
			// (e.g. TCP). WebSocket listeners already call TrackConnection in the HTTP handler,
			// so calling it again here would double-count and halve the effective threshold.
			if _, hasOwnTracking := s.connectionListener.(ipBlockingCheckerSetter); !hasOwnTracking {
				checker.TrackConnection(ip, "")
			}
		}

		c := newStompConn(rawConn, s.config, s.connectionEvents, s.done)

		if !s.sendConnectionEvent(&ConnEvent{
			ConnId:    c.GetId(),
			conn:      c,
			eventType: ConnectionStarting,
		}) {
			c.Close()
			return
		}
	}
}

func (s *stompServer) run() {
	// API and connection events are serialized here; the maps below rely on this single writer.
	defer s.closeDone()
	for {
		select {

		case apiEvent := <-s.apiEvents:
			switch apiEvent.eventType {
			case closeServer:
				s.closeDone()
				_ = s.connectionListener.Close()
				// close all open connections
				for _, c := range s.connectionsMap {
					c.Close()
				}
				s.connectionsMap = make(map[string]StompConn)
				return

			case sendMessage:
				s.sendFrame(apiEvent.destination, apiEvent.frame)

			case sendPrivateMessage:
				s.sendFrameToClient(apiEvent.connId, apiEvent.destination, apiEvent.frame)

			case closeConnectionByIP:
				for _, conn := range s.connectionsMap {
					connIP := extractIPFromAddress(conn.GetIPAddress())
					if connIP == apiEvent.targetIP {
						// Send error frame before closing connection
						conn.SendError(errors.New(apiEvent.errorMessage))
						conn.Close()
					}
				}
				// Connection termination completed

			}

		case e := <-s.connectionEvents:
			s.handleConnectionEvent(e)
		}
	}
}

func (s *stompServer) handleConnectionEvent(e *ConnEvent) {
	switch e.eventType {
	case ConnectionStarting:
		connectionCallback := s.snapshotConnectionEventCallback(e.eventType)
		s.connectionsMap[e.conn.GetId()] = e.conn
		if connectionCallback != nil {
			connectionCallback(e)
		}

	case ConnectionClosed:
		connectionCallback := s.snapshotConnectionEventCallback(e.eventType)
		unsubscribeCallbacks := s.snapshotUnsubscribeCallbacks()
		delete(s.connectionsMap, e.conn.GetId())
		for _, connSubscriptions := range s.subscriptionsMap {
			conSub, ok := connSubscriptions[e.conn.GetId()]
			if ok {
				delete(connSubscriptions, e.conn.GetId())
				for _, sub := range conSub.subscriptions {
					for _, callback := range unsubscribeCallbacks {
						callback(e.conn.GetId(), sub.id, sub.destination)
					}
				}
			}
		}
		if connectionCallback != nil {
			connectionCallback(e)
		}

	case SubscribeToTopic:
		connectionCallback := s.snapshotConnectionEventCallback(e.eventType)
		subscribeCallbacks := s.snapshotSubscribeCallbacks()
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
		for _, callback := range subscribeCallbacks {
			callback(e.conn.GetId(), e.sub.id, e.destination, e.frame)
		}
		if connectionCallback != nil {
			connectionCallback(e)
		}

	case UnsubscribeFromTopic:
		connectionCallback := s.snapshotConnectionEventCallback(e.eventType)
		unsubscribeCallbacks := s.snapshotUnsubscribeCallbacks()
		subs, ok := s.subscriptionsMap[e.destination]
		if ok {
			var conSub *connSubscriptions
			conSub, ok = subs[e.conn.GetId()]
			if ok {
				_, ok = conSub.subscriptions[e.sub.id]
				if ok {
					delete(conSub.subscriptions, e.sub.id)
					// notify listeners
					for _, callback := range unsubscribeCallbacks {
						callback(e.conn.GetId(), e.sub.id, e.destination)
					}
				}
			}
		}
		if connectionCallback != nil {
			connectionCallback(e)
		}

	case IncomingMessage:
		if s.config.IsAppRequestDestination(e.destination) && e.conn != nil {
			applicationRequestCallbacks := s.snapshotApplicationRequestCallbacks()
			// notify app listeners
			for _, callback := range applicationRequestCallbacks {
				callback(e.destination, e.frame.Body, e.conn.GetId())
			}
		}
		connectionCallback := s.snapshotConnectionEventCallback(e.eventType)
		if connectionCallback != nil {
			connectionCallback(e)
		}
	}
}

func (s *stompServer) sendFrame(dest string, f *frame.Frame) {
	subsMap, ok := s.subscriptionsMap[dest]
	if ok {
		for _, connSub := range subsMap {
			for _, sub := range connSub.subscriptions {
				connSub.conn.SendFrameToSubscription(cloneFrameHeaders(f), sub)
			}
		}
	}
}

func (s *stompServer) sendFrameToClient(conId string, dest string, f *frame.Frame) {
	subsMap, ok := s.subscriptionsMap[dest]
	if !ok {
		// No subscriptions exist for this destination at all
		s.config.Logger().Warn("no subscriptions found for destination", "destination", dest)
		return
	}

	connSubscriptions, ok := subsMap[conId]
	if !ok {
		// This connection doesn't have a subscription to this destination
		s.config.Logger().Warn("connection has no subscription to destination",
			"connectionId", conId, "destination", dest, "availableConnections", getConnectionIds(subsMap))
		return
	}

	// Successfully sending to subscriptions
	for _, sub := range connSubscriptions.subscriptions {
		connSubscriptions.conn.SendFrameToSubscription(cloneFrameHeaders(f), sub)
	}
}

func cloneFrameHeaders(f *frame.Frame) *frame.Frame {
	cloned := &frame.Frame{
		Command: f.Command,
		Body:    f.Body,
	}
	if f.Header != nil {
		cloned.Header = f.Header.Clone()
	}
	return cloned
}

// Helper function to get list of connection IDs for debugging
func getConnectionIds(subsMap map[string]*connSubscriptions) []string {
	ids := make([]string, 0, len(subsMap))
	for id := range subsMap {
		ids = append(ids, id)
	}
	return ids
}
