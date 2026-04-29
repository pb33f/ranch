package store

import "github.com/pb33f/ranch/bus"

const (
	// StoreCreatedEvt is emitted when a store is created.
	StoreCreatedEvt bus.MonitorEventType = "store.created"
	// StoreDestroyedEvt is emitted when a store is destroyed.
	StoreDestroyedEvt bus.MonitorEventType = "store.destroyed"
	// StoreInitializedEvt is emitted when a store is initialized.
	StoreInitializedEvt bus.MonitorEventType = "store.initialized"
)
