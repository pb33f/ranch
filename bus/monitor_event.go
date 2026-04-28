// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

type MonitorEventType string

const (
	ChannelCreatedEvt          MonitorEventType = "bus.channel.created"
	ChannelDestroyedEvt        MonitorEventType = "bus.channel.destroyed"
	ChannelSubscriberJoinedEvt MonitorEventType = "bus.channel.subscriber.joined"
	ChannelSubscriberLeftEvt   MonitorEventType = "bus.channel.subscriber.left"
	BrokerSubscribedEvt        MonitorEventType = "bus.broker.subscribed"
	BrokerUnsubscribedEvt      MonitorEventType = "bus.broker.unsubscribed"
)

type MonitorEventHandler func(event *MonitorEvent)

type MonitorEvent struct {
	// Type of the event
	EventType MonitorEventType
	// The name of the channel or the store related to this event
	EntityName string
	// Optional event data
	Data any
}

// Create a new monitor event
func NewMonitorEvent(evtType MonitorEventType, entityName string, data any) *MonitorEvent {
	return &MonitorEvent{EventType: evtType, Data: data, EntityName: entityName}
}
