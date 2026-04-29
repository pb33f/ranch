// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import "github.com/pb33f/ranch/monitor"

// MonitorEventType names a bus monitor event.
type MonitorEventType = monitor.EventType

const (
	// ChannelCreatedEvt is emitted when a bus channel is created.
	ChannelCreatedEvt = monitor.ChannelCreatedEvt
	// ChannelDestroyedEvt is emitted when a bus channel is destroyed.
	ChannelDestroyedEvt = monitor.ChannelDestroyedEvt
	// ChannelSubscriberJoinedEvt is emitted when a handler subscribes to a channel.
	ChannelSubscriberJoinedEvt = monitor.ChannelSubscriberJoinedEvt
	// ChannelSubscriberLeftEvt is emitted when a handler unsubscribes from a channel.
	ChannelSubscriberLeftEvt = monitor.ChannelSubscriberLeftEvt
	// BrokerSubscribedEvt is emitted when a broker subscription is created.
	BrokerSubscribedEvt = monitor.BrokerSubscribedEvt
	// BrokerUnsubscribedEvt is emitted when a broker subscription is removed.
	BrokerUnsubscribedEvt = monitor.BrokerUnsubscribedEvt
)

// MonitorEventHandler receives monitor events.
type MonitorEventHandler = monitor.Handler

// MonitorEvent describes a lifecycle event published by bus components.
type MonitorEvent = monitor.Event

// NewMonitorEvent creates a monitor event for an entity and optional data payload.
func NewMonitorEvent(evtType MonitorEventType, entityName string, data any) *MonitorEvent {
	return monitor.NewEvent(evtType, entityName, data)
}
