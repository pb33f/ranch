package store

import "github.com/pb33f/ranch/bus"

const (
	StoreCreatedEvt     bus.MonitorEventType = "store.created"
	StoreDestroyedEvt   bus.MonitorEventType = "store.destroyed"
	StoreInitializedEvt bus.MonitorEventType = "store.initialized"
)
