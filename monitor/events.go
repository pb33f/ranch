// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

// Package monitor defines shared monitor event names without coupling peer packages.
package monitor

// EventType names a monitor event.
type EventType string

const (
	// ChannelCreatedEvt is emitted when a bus channel is created.
	ChannelCreatedEvt EventType = "bus.channel.created"
	// ChannelDestroyedEvt is emitted when a bus channel is destroyed.
	ChannelDestroyedEvt EventType = "bus.channel.destroyed"
	// ChannelSubscriberJoinedEvt is emitted when a handler subscribes to a channel.
	ChannelSubscriberJoinedEvt EventType = "bus.channel.subscriber.joined"
	// ChannelSubscriberLeftEvt is emitted when a handler unsubscribes from a channel.
	ChannelSubscriberLeftEvt EventType = "bus.channel.subscriber.left"
	// BrokerSubscribedEvt is emitted when a broker subscription is created.
	BrokerSubscribedEvt EventType = "bus.broker.subscribed"
	// BrokerUnsubscribedEvt is emitted when a broker subscription is removed.
	BrokerUnsubscribedEvt EventType = "bus.broker.unsubscribed"
)

const (
	// StoreCreatedEvt is emitted when a store is created.
	StoreCreatedEvt EventType = "store.created"
	// StoreDestroyedEvt is emitted when a store is destroyed.
	StoreDestroyedEvt EventType = "store.destroyed"
	// StoreInitializedEvt is emitted when a store is initialized.
	StoreInitializedEvt EventType = "store.initialized"
	// FabricEndpointSubscribeEvt is emitted when a STOMP client subscribes through Fabric.
	FabricEndpointSubscribeEvt EventType = "fabric.endpoint.subscribe"
	// FabricEndpointUnsubscribeEvt is emitted when a STOMP client unsubscribes through Fabric.
	FabricEndpointUnsubscribeEvt EventType = "fabric.endpoint.unsubscribe"
)

// Handler receives monitor events.
type Handler func(event *Event)

// Event describes a lifecycle event published by Ranch components.
type Event struct {
	// Type of the event.
	EventType EventType
	// EntityName is the name of the channel, store, or endpoint entity related to this event.
	EntityName string
	// Data carries optional event-specific payload data.
	Data any
}

// NewEvent creates a monitor event for an entity and optional data payload.
func NewEvent(evtType EventType, entityName string, data any) *Event {
	return &Event{EventType: evtType, Data: data, EntityName: entityName}
}
