// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bridge"
	"github.com/pb33f/ranch/model"
	"log/slog"
	"sync"
	"sync/atomic"
)

const RANCH_INTERNAL_CHANNEL_PREFIX = "_ranchInternal/"

type Publisher interface {
	SendRequestMessage(channelName string, payload any, destinationId *uuid.UUID) error
	SendRequestMessageContext(
		ctx context.Context, channelName string, payload any, destinationId *uuid.UUID) error
	SendResponseMessage(channelName string, payload any, destinationId *uuid.UUID) error
	SendResponseMessageContext(
		ctx context.Context, channelName string, payload any, destinationId *uuid.UUID) error
	SendBroadcastMessage(channelName string, payload any) error
	SendBroadcastMessageContext(ctx context.Context, channelName string, payload any) error
	SendErrorMessage(channelName string, err error, destinationId *uuid.UUID) error
	SendErrorMessageContext(ctx context.Context, channelName string, err error, destinationId *uuid.UUID) error
}

type Subscriber interface {
	ListenStream(channelName string) (MessageHandler, error)
	ListenStreamForDestination(channelName string, destinationId *uuid.UUID) (MessageHandler, error)
	ListenFirehose(channelName string) (MessageHandler, error)
	ListenRequestStream(channelName string) (MessageHandler, error)
	ListenRequestStreamForDestination(channelName string, destinationId *uuid.UUID) (MessageHandler, error)
	ListenRequestOnce(channelName string) (MessageHandler, error)
	ListenRequestOnceForDestination(channelName string, destinationId *uuid.UUID) (MessageHandler, error)
	ListenOnce(channelName string) (MessageHandler, error)
	ListenOnceForDestination(channelName string, destId *uuid.UUID) (MessageHandler, error)
	RequestOnce(channelName string, payload any) (MessageHandler, error)
	RequestOnceForDestination(channelName string, payload any, destId *uuid.UUID) (MessageHandler, error)
	RequestStream(channelName string, payload any) (MessageHandler, error)
	RequestStreamForDestination(channelName string, payload any, destId *uuid.UUID) (MessageHandler, error)
}

type ChannelControl interface {
	GetChannelManager() ChannelManager
}

type BrokerControl interface {
	ConnectBroker(config *bridge.BrokerConnectorConfig) (conn bridge.Connection, err error)
}

type MonitorControl interface {
	AddMonitorEventListener(listener MonitorEventHandler, eventTypes ...MonitorEventType) MonitorEventListenerId
	RemoveMonitorEventListener(listenerId MonitorEventListenerId)
	SendMonitorEvent(evtType MonitorEventType, entityName string, data any)
}

type EventBus interface {
	Publisher
	Subscriber
	ChannelControl
	BrokerControl
	MonitorControl
	GetId() *uuid.UUID
}

func NewEventBus() EventBus {
	return NewEventBusWithLogger(nil)
}

func NewEventBusWithLogger(logger *slog.Logger) EventBus {
	bf := new(transportEventBus)
	bf.init(logger)
	return bf
}

type transportEventBus struct {
	ChannelManager      ChannelManager
	Id                  uuid.UUID
	brokerConnections   map[uuid.UUID]bridge.Connection
	brokerConnectionsMu sync.RWMutex
	bc                  bridge.BrokerConnector
	monitor             *transportMonitor
	logger              *slog.Logger
}

type MonitorEventListenerId int

type transportMonitor struct {
	lock                  sync.RWMutex
	listenersByType       map[MonitorEventType]map[MonitorEventListenerId]MonitorEventHandler
	listenersForAllEvents map[MonitorEventListenerId]MonitorEventHandler
	subId                 MonitorEventListenerId
}

type sendMessageOptions struct {
	direction     model.Direction
	channelName   string
	payload       any
	err           error
	destinationId *uuid.UUID
}

type messageHandlerOptions struct {
	direction           model.Direction
	destinationId       *uuid.UUID
	ignoreId            bool
	allTraffic          bool
	runOnce             bool
	requireDestination  bool
	generateDestination bool
	requestPayload      any
	withRequest         bool
}

func newMonitor() *transportMonitor {
	return &transportMonitor{
		listenersByType:       make(map[MonitorEventType]map[MonitorEventListenerId]MonitorEventHandler),
		listenersForAllEvents: make(map[MonitorEventListenerId]MonitorEventHandler),
	}
}

func (m *transportMonitor) addListener(listener MonitorEventHandler, eventTypes []MonitorEventType) MonitorEventListenerId {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.subId++
	if len(eventTypes) == 0 {
		m.listenersForAllEvents[m.subId] = listener
	} else {
		for _, eventType := range eventTypes {
			listeners, ok := m.listenersByType[eventType]
			if !ok {
				listeners = make(map[MonitorEventListenerId]MonitorEventHandler)
				m.listenersByType[eventType] = listeners
			}
			listeners[m.subId] = listener
		}
	}

	return m.subId
}

func (m *transportMonitor) removeListener(listenerId MonitorEventListenerId) {
	m.lock.Lock()
	defer m.lock.Unlock()

	delete(m.listenersForAllEvents, listenerId)
	for _, listeners := range m.listenersByType {
		delete(listeners, listenerId)
	}
}

func (m *transportMonitor) sendEvent(event *MonitorEvent) {
	m.lock.RLock()
	defer m.lock.RUnlock()

	for _, l := range m.listenersForAllEvents {
		l(event)
	}

	for _, l := range m.listenersByType[event.EventType] {
		l(event)
	}
}

func (bus *transportEventBus) GetId() *uuid.UUID {
	return &bus.Id
}

func (bus *transportEventBus) init(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	bus.logger = logger
	bus.Id = uuid.New()
	bus.ChannelManager = NewBusChannelManager(bus)
	bus.brokerConnections = make(map[uuid.UUID]bridge.Connection)
	bus.bc = bridge.NewBrokerConnector()
	bus.monitor = newMonitor()
	bus.logger.Debug("ranch booted", "id", bus.Id.String())
}

func (bus *transportEventBus) GetChannelManager() ChannelManager {
	return bus.ChannelManager
}

func (bus *transportEventBus) SendResponseMessage(channelName string, payload any, destId *uuid.UUID) error {
	return bus.SendResponseMessageContext(context.Background(), channelName, payload, destId)
}

func (bus *transportEventBus) SendResponseMessageContext(
	ctx context.Context, channelName string, payload any, destId *uuid.UUID) error {
	return bus.sendMessageContext(ctx, sendMessageOptions{
		direction:     model.ResponseDir,
		channelName:   channelName,
		payload:       payload,
		destinationId: destId,
	})
}

func (bus *transportEventBus) SendBroadcastMessage(channelName string, payload any) error {
	return bus.SendBroadcastMessageContext(context.Background(), channelName, payload)
}

func (bus *transportEventBus) SendBroadcastMessageContext(ctx context.Context, channelName string, payload any) error {
	return bus.sendMessageContext(ctx, sendMessageOptions{
		direction:   model.ResponseDir,
		channelName: channelName,
		payload:     payload,
	})
}

func (bus *transportEventBus) SendRequestMessage(channelName string, payload any, destId *uuid.UUID) error {
	return bus.SendRequestMessageContext(context.Background(), channelName, payload, destId)
}

func (bus *transportEventBus) SendRequestMessageContext(
	ctx context.Context, channelName string, payload any, destId *uuid.UUID) error {
	return bus.sendMessageContext(ctx, sendMessageOptions{
		direction:     model.RequestDir,
		channelName:   channelName,
		payload:       payload,
		destinationId: destId,
	})
}

func (bus *transportEventBus) SendErrorMessage(channelName string, err error, destId *uuid.UUID) error {
	return bus.SendErrorMessageContext(context.Background(), channelName, err, destId)
}

func (bus *transportEventBus) SendErrorMessageContext(
	ctx context.Context, channelName string, err error, destId *uuid.UUID) error {
	return bus.sendMessageContext(ctx, sendMessageOptions{
		direction:     model.ErrorDir,
		channelName:   channelName,
		err:           err,
		destinationId: destId,
	})
}

func (bus *transportEventBus) ListenStream(channelName string) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction: model.ResponseDir,
		ignoreId:  true,
	})
}

func (bus *transportEventBus) AddMonitorEventListener(
	listener MonitorEventHandler, eventTypes ...MonitorEventType) MonitorEventListenerId {

	return bus.monitor.addListener(listener, eventTypes)
}

func (bus *transportEventBus) RemoveMonitorEventListener(listenerId MonitorEventListenerId) {
	bus.monitor.removeListener(listenerId)
}

func (bus *transportEventBus) ListenStreamForDestination(channelName string, destId *uuid.UUID) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:          model.ResponseDir,
		destinationId:      destId,
		requireDestination: true,
	})
}

func (bus *transportEventBus) ListenRequestStream(channelName string) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction: model.RequestDir,
		ignoreId:  true,
	})
}

func (bus *transportEventBus) ListenRequestStreamForDestination(
	channelName string, destId *uuid.UUID) (MessageHandler, error) {

	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:          model.RequestDir,
		destinationId:      destId,
		requireDestination: true,
	})
}

func (bus *transportEventBus) ListenRequestOnce(channelName string) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:           model.RequestDir,
		ignoreId:            true,
		runOnce:             true,
		generateDestination: true,
	})
}

func (bus *transportEventBus) ListenRequestOnceForDestination(
	channelName string, destId *uuid.UUID) (MessageHandler, error) {

	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:          model.RequestDir,
		destinationId:      destId,
		runOnce:            true,
		requireDestination: true,
	})
}

func (bus *transportEventBus) ListenFirehose(channelName string) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:  model.RequestDir,
		ignoreId:   true,
		allTraffic: true,
	})
}

func (bus *transportEventBus) ListenOnce(channelName string) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:           model.ResponseDir,
		ignoreId:            true,
		runOnce:             true,
		generateDestination: true,
	})
}

func (bus *transportEventBus) ListenOnceForDestination(channelName string, destId *uuid.UUID) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:          model.ResponseDir,
		destinationId:      destId,
		runOnce:            true,
		requireDestination: true,
	})
}

func (bus *transportEventBus) RequestOnce(channelName string, payload any) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:           model.ResponseDir,
		ignoreId:            true,
		runOnce:             true,
		generateDestination: true,
		requestPayload:      payload,
		withRequest:         true,
	})
}

func (bus *transportEventBus) RequestOnceForDestination(
	channelName string, payload any, destId *uuid.UUID) (MessageHandler, error) {

	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:          model.ResponseDir,
		destinationId:      destId,
		runOnce:            true,
		requireDestination: true,
		requestPayload:     payload,
		withRequest:        true,
	})
}

func (bus *transportEventBus) RequestStream(channelName string, payload any) (MessageHandler, error) {
	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:           model.ResponseDir,
		ignoreId:            true,
		generateDestination: true,
		requestPayload:      payload,
		withRequest:         true,
	})
}

func (bus *transportEventBus) RequestStreamForDestination(
	channelName string, payload any, destId *uuid.UUID) (MessageHandler, error) {

	return bus.newMessageHandler(channelName, messageHandlerOptions{
		direction:          model.ResponseDir,
		destinationId:      destId,
		requireDestination: true,
		requestPayload:     payload,
		withRequest:        true,
	})
}

func (bus *transportEventBus) ConnectBroker(config *bridge.BrokerConnectorConfig) (conn bridge.Connection, err error) {
	conn, err = bus.bc.Connect(config, bus.logger.Enabled(context.Background(), slog.LevelDebug))
	if conn != nil {
		bus.brokerConnectionsMu.Lock()
		bus.brokerConnections[*conn.GetId()] = conn
		bus.brokerConnectionsMu.Unlock()
	}
	return
}

func (bus *transportEventBus) SendMonitorEvent(
	evtType MonitorEventType, entityName string, payload any) {

	bus.monitor.sendEvent(NewMonitorEvent(evtType, entityName, payload))
}

func (bus *transportEventBus) wrapMessageHandler(
	channel *Channel, direction model.Direction, ignoreId bool, allTraffic bool, destId *uuid.UUID,
	runOnce bool) *messageHandler {

	messageHandler := createMessageHandler(channel, destId, bus.ChannelManager)
	messageHandler.ignoreId = ignoreId

	if runOnce {
		messageHandler.invokeOnce = &sync.Once{}
	}

	errorHandler := func(ctx context.Context, err error) {
		if messageHandler.errorHandler != nil {
			if messageHandler.handlerContext != nil && messageHandler.handlerContext.Err() != nil {
				return
			}
			if runOnce {
				messageHandler.invokeOnce.Do(func() {
					atomic.AddInt64(&messageHandler.runCount, 1)
					messageHandler.errorHandler(ctx, err)
				})
			} else {
				atomic.AddInt64(&messageHandler.runCount, 1)
				messageHandler.errorHandler(ctx, err)
			}
		}
	}
	successHandler := func(ctx context.Context, msg *model.Message) {
		if messageHandler.successHandler != nil {
			if messageHandler.handlerContext != nil && messageHandler.handlerContext.Err() != nil {
				return
			}
			if runOnce {
				messageHandler.invokeOnce.Do(func() {
					atomic.AddInt64(&messageHandler.runCount, 1)
					messageHandler.successHandler(ctx, msg)
				})
			} else {
				atomic.AddInt64(&messageHandler.runCount, 1)
				messageHandler.successHandler(ctx, msg)
			}
		}
	}

	handlerWrapper := func(ctx context.Context, msg *model.Message) {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return
		}
		dir := direction
		id := messageHandler.destination
		if allTraffic {
			if msg.Direction == model.ErrorDir {
				errorHandler(ctx, msg.Error)
			} else {
				successHandler(ctx, msg)
			}
		} else {
			if msg.Direction == dir {
				// if we're checking for specific traffic, check a DestinationId match is required.
				if !messageHandler.ignoreId &&
					(msg.DestinationId != nil && id != nil) && (*id == *msg.DestinationId) {
					successHandler(ctx, msg)
				}
				if messageHandler.ignoreId {
					successHandler(ctx, msg)
				}
			}
			if msg.Direction == model.ErrorDir {
				errorHandler(ctx, msg.Error)
			}
		}
	}

	messageHandler.wrapperFunction = handlerWrapper
	return messageHandler
}

func (bus *transportEventBus) sendMessageContext(ctx context.Context, opts sendMessageOptions) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}

	channelObject, err := bus.ChannelManager.GetChannel(opts.channelName)
	if err != nil {
		return err
	}

	var message *model.Message
	switch opts.direction {
	case model.RequestDir:
		message = model.GenerateRequest(buildConfig(opts.channelName, opts.payload, opts.destinationId))
	case model.ResponseDir:
		message = model.GenerateResponse(buildConfig(opts.channelName, opts.payload, opts.destinationId))
	case model.ErrorDir:
		message = model.GenerateError(buildError(opts.channelName, opts.err, opts.destinationId))
	default:
		return fmt.Errorf("unsupported message direction %d", opts.direction)
	}

	channelObject.SendContext(ctx, message)
	return nil
}

func (bus *transportEventBus) newMessageHandler(channelName string, opts messageHandlerOptions) (MessageHandler, error) {
	channel, err := bus.ChannelManager.GetChannel(channelName)
	if err != nil {
		return nil, err
	}
	if opts.requireDestination && opts.destinationId == nil {
		return nil, fmt.Errorf("DestinationId cannot be nil")
	}
	if opts.generateDestination {
		opts.destinationId = getOrNewId(opts.destinationId)
	}

	messageHandler := bus.wrapMessageHandler(
		channel,
		opts.direction,
		opts.ignoreId,
		opts.allTraffic,
		opts.destinationId,
		opts.runOnce)
	if opts.withRequest {
		messageHandler.requestMessage = model.GenerateRequest(
			buildConfig(channelName, opts.requestPayload, opts.destinationId))
	}
	return messageHandler, nil
}

func getOrNewId(id *uuid.UUID) *uuid.UUID {
	if id == nil {
		i := uuid.New()
		id = &i
	}
	return id
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func buildConfig(channelName string, payload any, destinationId *uuid.UUID) *model.MessageConfig {
	config := new(model.MessageConfig)
	id := uuid.New()
	config.Id = &id
	config.DestinationId = destinationId
	config.Channel = channelName
	config.Payload = payload
	return config
}

func buildError(channelName string, err error, destinationId *uuid.UUID) *model.MessageConfig {
	config := new(model.MessageConfig)
	id := uuid.New()
	config.Id = &id
	config.DestinationId = destinationId
	config.Channel = channelName
	config.Err = err
	return config
}

func createMessageHandler(channel *Channel, destinationId *uuid.UUID, channelMgr ChannelManager) *messageHandler {
	messageHandler := new(messageHandler)
	messageHandler.channel = channel
	messageHandler.handlerContext = context.Background()
	id := uuid.New()
	messageHandler.id = &id
	messageHandler.destination = destinationId
	messageHandler.channelManager = channelMgr
	return messageHandler
}
