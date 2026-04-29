// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

// MonitorEventType names a bus monitor event.
type MonitorEventType string

const (
	// ChannelCreatedEvt is emitted when a bus channel is created.
	ChannelCreatedEvt MonitorEventType = "bus.channel.created"
	// ChannelDestroyedEvt is emitted when a bus channel is destroyed.
	ChannelDestroyedEvt MonitorEventType = "bus.channel.destroyed"
	// ChannelSubscriberJoinedEvt is emitted when a handler subscribes to a channel.
	ChannelSubscriberJoinedEvt MonitorEventType = "bus.channel.subscriber.joined"
	// ChannelSubscriberLeftEvt is emitted when a handler unsubscribes from a channel.
	ChannelSubscriberLeftEvt MonitorEventType = "bus.channel.subscriber.left"
	// BrokerSubscribedEvt is emitted when a broker subscription is created.
	BrokerSubscribedEvt MonitorEventType = "bus.broker.subscribed"
	// BrokerUnsubscribedEvt is emitted when a broker subscription is removed.
	BrokerUnsubscribedEvt MonitorEventType = "bus.broker.unsubscribed"
)

// MonitorEventHandler receives monitor events.
type MonitorEventHandler func(event *MonitorEvent)

// MonitorEvent describes a lifecycle event published by bus components.
type MonitorEvent struct {
	// Type of the event
	EventType MonitorEventType
	// The name of the channel or the store related to this event
	EntityName string
	// Optional event data
	Data any
}

// NewMonitorEvent creates a monitor event for an entity and optional data payload.
func NewMonitorEvent(evtType MonitorEventType, entityName string, data any) *MonitorEvent {
	return &MonitorEvent{EventType: evtType, Data: data, EntityName: entityName}
}
