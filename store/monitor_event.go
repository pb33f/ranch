package store

import "github.com/pb33f/ranch/monitor"

const (
	// StoreCreatedEvt is emitted when a store is created.
	StoreCreatedEvt = monitor.StoreCreatedEvt
	// StoreDestroyedEvt is emitted when a store is destroyed.
	StoreDestroyedEvt = monitor.StoreDestroyedEvt
	// StoreInitializedEvt is emitted when a store is initialized.
	StoreInitializedEvt = monitor.StoreInitializedEvt
)
