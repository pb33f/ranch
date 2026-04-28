// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package store

import (
	"github.com/google/uuid"
	buspkg "github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/transport/fabric"
	"github.com/stretchr/testify/assert"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testStoreSyncService() (*storeSyncService, buspkg.EventBus, Manager) {
	b := buspkg.NewEventBus()
	manager := NewManager(b).(*storeManager)
	return manager.syncService, b, manager
}

func TestStoreSyncService_NewConnection(t *testing.T) {
	service, bus, _ := testStoreSyncService()

	// verify that the service ignores non transport-store-sync events
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, "galactic-channel", nil)
	assert.Equal(t, len(service.syncClients), 0)

	syncChan := "transport-store-sync.1"

	bus.GetChannelManager().CreateChannel(syncChan)

	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)
	assert.Equal(t, len(service.syncClients), 1)
}

func TestStoreSyncService_OpenStoreErrors(t *testing.T) {
	_, bus, _ := testStoreSyncService()

	syncChan := "transport-store-sync.1"
	bus.GetChannelManager().CreateChannel(syncChan)

	mh, _ := bus.ListenStream(syncChan)
	wg := sync.WaitGroup{}
	var errors []*model.Response
	mh.Handle(func(message *model.Message) {
		errors = append(errors, message.Payload.(*model.Response))
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)
	id := uuid.New()
	_ = bus.SendRequestMessage(syncChan, "invalid-request", nil)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        "invalid-payload",
		Id:             &id,
	}, nil)

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        make(map[string]any),
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, errors[0].Id, &id)
	assert.True(t, errors[0].Error)
	assert.Equal(t, errors[0].ErrorMessage, "Invalid OpenStoreRequest")

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "non-existing-store"},
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, errors[1].Id, &id)
	assert.True(t, errors[1].Error)
	assert.Equal(t, errors[1].ErrorMessage, "Cannot open non-existing store: non-existing-store")
}

func TestStoreSyncService_OpenStore(t *testing.T) {
	service, bus, storeManager := testStoreSyncService()

	store := storeManager.CreateStoreWithType(
		"test-store", reflect.TypeOf(&MockStoreItem{}))
	_ = store.Populate(map[string]any{
		"item1": &MockStoreItem{From: "test", Message: "test-message"},
		"item2": &MockStoreItem{From: "test2", Message: uuid.New().String()},
	})

	syncChan := "transport-store-sync.1"
	bus.GetChannelManager().CreateChannel(syncChan)

	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)

	wg := sync.WaitGroup{}
	var syncResp []any

	mh, _ := bus.ListenStream(syncChan)
	mh.Handle(func(message *model.Message) {
		syncResp = append(syncResp, message.Payload)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	wg.Wait()

	assert.Equal(t, len(service.syncClients[syncChan].openStores), 1)
	assert.Equal(t, len(service.syncStoreListeners), 1)
	assert.Equal(t, service.syncStoreListeners["test-store"].clientSyncChannels[syncChan], true)

	resp := syncResp[0].(*StoreContentResponse)

	assert.Equal(t, resp.StoreId, "test-store")
	items, version := store.AllValuesAndVersion()

	assert.Equal(t, resp.StoreVersion, version)
	assert.Equal(t, resp.Items, items)
	assert.Equal(t, resp.ResponseType, "storeContentResponse")

	// try subscribing to the same sync channel again
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)
	assert.Equal(t, len(service.syncClients[syncChan].openStores), 1)

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	wg.Wait()

	assert.Equal(t, len(syncResp), 2)
	assert.Equal(t, syncResp[1].(*StoreContentResponse).ResponseType, "storeContentResponse")

	syncChan2 := "transport-store-sync.2"
	bus.GetChannelManager().CreateChannel(syncChan2)
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan2, nil)

	mh2, _ := bus.ListenStream(syncChan2)
	mh2.Handle(func(message *model.Message) {
		syncResp = append(syncResp, message.Payload)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan2, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	wg.Wait()

	assert.Equal(t, len(syncResp), 3)
	assert.Equal(t, syncResp[2].(*StoreContentResponse).ResponseType, "storeContentResponse")

	assert.Equal(t, len(service.syncClients), 2)
	assert.Equal(t, len(service.syncClients[syncChan].openStores), 1)
	assert.Equal(t, len(service.syncClients[syncChan2].openStores), 1)
	assert.Equal(t, service.syncClients[syncChan2].openStores["test-store"], true)

	assert.Equal(t, len(service.syncStoreListeners["test-store"].clientSyncChannels), 2)
	assert.Equal(t, service.syncStoreListeners["test-store"].clientSyncChannels[syncChan2], true)

	bus.SendMonitorEvent(buspkg.ChannelDestroyedEvt, syncChan, nil)

	assert.Equal(t, len(service.syncClients), 1)
	assert.Equal(t, len(service.syncClients[syncChan2].openStores), 1)
	assert.Equal(t, service.syncClients[syncChan2].openStores["test-store"], true)
	assert.Equal(t, len(service.syncStoreListeners["test-store"].clientSyncChannels), 1)
	assert.Equal(t, service.syncStoreListeners["test-store"].clientSyncChannels[syncChan2], true)

	bus.SendMonitorEvent(buspkg.ChannelDestroyedEvt, syncChan2, nil)

	assert.Equal(t, len(service.syncClients), 0)
	assert.Equal(t, len(service.syncStoreListeners), 0)

	// try closing the syncChan2 again
	bus.SendMonitorEvent(buspkg.ChannelDestroyedEvt, syncChan2, nil)
}

func TestStoreSyncService_CloseStore(t *testing.T) {
	service, bus, storeManager := testStoreSyncService()

	store := storeManager.CreateStoreWithType(
		"test-store", reflect.TypeOf(&MockStoreItem{}))
	_ = store.Populate(map[string]any{
		"item1": &MockStoreItem{From: "test", Message: "test-message"},
		"item2": &MockStoreItem{From: "test2", Message: uuid.New().String()},
	})

	syncChan := "transport-store-sync.1"
	bus.GetChannelManager().CreateChannel(syncChan)
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)

	syncChan2 := "transport-store-sync.2"
	bus.GetChannelManager().CreateChannel(syncChan2)
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan2, nil)

	wg := sync.WaitGroup{}
	var syncResp1 []any

	mh, _ := bus.ListenStream(syncChan)
	mh.Handle(func(message *model.Message) {
		syncResp1 = append(syncResp1, message.Payload)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	mh2, _ := bus.ListenStream(syncChan2)
	mh2.Handle(func(message *model.Message) {
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	id := uuid.New()

	wg.Add(2)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	_ = bus.SendRequestMessage(syncChan2, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	wg.Wait()

	assert.Equal(t, len(service.syncStoreListeners["test-store"].clientSyncChannels), 2)

	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: closeStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
		Id:             &id,
	}, nil)

	wg.Add(2)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: closeStoreRequest,
		Payload:        make(map[string]any),
		Id:             &id,
	}, nil)
	_ = bus.SendRequestMessage(syncChan2, &model.Request{
		RequestCommand: closeStoreRequest,
		Payload:        map[string]any{"storeId": ""},
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, syncResp1[1].(*model.Response).ErrorMessage, "Invalid CloseStoreRequest")
	assert.Equal(t, syncResp1[1].(*model.Response).Id, &id)
	assert.Equal(t, syncResp1[1].(*model.Response).Error, true)

	service.lock.Lock()
	assert.Equal(t, len(service.syncStoreListeners["test-store"].clientSyncChannels), 1)
	assert.Equal(t, service.syncStoreListeners["test-store"].clientSyncChannels[syncChan2], true)
	assert.Equal(t, len(service.syncClients[syncChan].openStores), 0)
	assert.Equal(t, len(service.syncClients[syncChan2].openStores), 1)
	service.lock.Unlock()

	_ = bus.SendRequestMessage(syncChan2, &model.Request{
		RequestCommand: closeStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
		Id:             &id,
	}, nil)

	wg.Add(2)
	_ = bus.SendRequestMessage(syncChan2, &model.Request{
		RequestCommand: closeStoreRequest,
		Payload:        make(map[string]any),
		Id:             &id,
	}, nil)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: closeStoreRequest,
		Payload:        map[string]any{"storeId": ""},
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, syncResp1[2].(*model.Response).ErrorMessage, "Invalid CloseStoreRequest")
	assert.Equal(t, syncResp1[2].(*model.Response).Id, &id)
	assert.Equal(t, syncResp1[2].(*model.Response).Error, true)

	service.lock.Lock()
	assert.Equal(t, len(service.syncStoreListeners), 0)
	assert.Equal(t, len(service.syncClients[syncChan].openStores), 0)
	assert.Equal(t, len(service.syncClients[syncChan2].openStores), 0)
	service.lock.Unlock()
}

func TestStoreSyncService_UpdateStoreErrors(t *testing.T) {
	_, bus, _ := testStoreSyncService()

	syncChan := "transport-store-sync.1"
	bus.GetChannelManager().CreateChannel(syncChan)
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)

	wg := sync.WaitGroup{}
	var syncResp []any

	mh, _ := bus.ListenStream(syncChan)
	mh.Handle(func(message *model.Message) {
		syncResp = append(syncResp, message.Payload)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	id := uuid.New()

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload:        map[string]any{},
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, syncResp[0].(*model.Response).ErrorMessage, "Invalid UpdateStoreRequest: missing storeId")

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, syncResp[1].(*model.Response).ErrorMessage, "Invalid UpdateStoreRequest: missing itemId")

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload:        map[string]any{"storeId": "test-store", "itemId": "item1"},
		Id:             &id,
	}, nil)
	wg.Wait()

	assert.Equal(t, syncResp[2].(*model.Response).ErrorMessage, "Cannot update non-existing store: test-store")
}

func TestStoreSyncService_UpdateStore(t *testing.T) {
	_, bus, storeManager := testStoreSyncService()

	store := storeManager.CreateStoreWithType(
		"test-store", reflect.TypeOf(&MockStoreItem{}))
	_ = store.Populate(map[string]any{
		"item1": &MockStoreItem{From: "test", Message: "test-message"},
		"item2": &MockStoreItem{From: "test2", Message: uuid.New().String()},
	})

	syncChan := "transport-store-sync.1"
	bus.GetChannelManager().CreateChannel(syncChan)
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan, nil)

	syncChan2 := "transport-store-sync.2"
	bus.GetChannelManager().CreateChannel(syncChan2)
	bus.SendMonitorEvent(fabric.FabricEndpointSubscribeEvt, syncChan2, nil)

	wg := sync.WaitGroup{}
	var syncResp1 []any
	var syncResp2 []any

	mh, _ := bus.ListenStream(syncChan)
	mh.Handle(func(message *model.Message) {
		syncResp1 = append(syncResp1, message.Payload)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	mh2, _ := bus.ListenStream(syncChan2)
	mh2.Handle(func(message *model.Message) {
		syncResp2 = append(syncResp2, message.Payload)
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "Unexpected error")
	})

	wg.Add(2)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	_ = bus.SendRequestMessage(syncChan2, &model.Request{
		RequestCommand: openStoreRequest,
		Payload:        map[string]any{"storeId": "test-store"},
	}, nil)
	wg.Wait()

	assert.Equal(t, len(syncResp1), 1)
	assert.Equal(t, len(syncResp2), 1)

	wg.Add(2)

	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload: map[string]any{
			"storeId": "test-store",
			"itemId":  "item3",
			"newItemValue": map[string]any{
				"From":    "test3",
				"Message": "test-message3",
			}},
	}, nil)

	wg.Wait()

	assert.Equal(t, len(syncResp1), 2)
	assert.Equal(t, len(syncResp2), 2)

	assert.Equal(t, syncResp1[1].(*UpdateStoreResponse).ResponseType, "updateStoreResponse")
	assert.Equal(t, syncResp1[1].(*UpdateStoreResponse).StoreId, "test-store")
	assert.Equal(t, syncResp1[1].(*UpdateStoreResponse).StoreVersion, int64(2))
	assert.Equal(t, syncResp1[1].(*UpdateStoreResponse).NewItemValue, &MockStoreItem{
		From:    "test3",
		Message: "test-message3",
	})

	assert.Equal(t, syncResp1[1], syncResp2[1])

	assert.Equal(t, store.GetValue("item3"), &MockStoreItem{
		From:    "test3",
		Message: "test-message3",
	})

	wg.Add(2)
	store.Remove("item2", "test-remove")
	wg.Wait()

	assert.Equal(t, len(syncResp1), 3)
	assert.Equal(t, len(syncResp2), 3)

	assert.Equal(t, syncResp1[2].(*UpdateStoreResponse).ResponseType, "updateStoreResponse")
	assert.Equal(t, syncResp1[2].(*UpdateStoreResponse).StoreId, "test-store")
	assert.Equal(t, syncResp1[2].(*UpdateStoreResponse).ItemId, "item2")
	assert.Equal(t, syncResp1[2].(*UpdateStoreResponse).StoreVersion, int64(3))
	assert.Equal(t, syncResp1[2].(*UpdateStoreResponse).NewItemValue, nil)

	assert.Equal(t, syncResp1[2], syncResp2[2])

	wg.Add(2)
	store.Put("item1", &MockStoreItem{From: "u1", Message: "m1"}, nil)
	wg.Wait()

	assert.Equal(t, len(syncResp1), 4)
	assert.Equal(t, len(syncResp2), 4)

	assert.Equal(t, syncResp1[3].(*UpdateStoreResponse).ResponseType, "updateStoreResponse")
	assert.Equal(t, syncResp1[3].(*UpdateStoreResponse).StoreId, "test-store")
	assert.Equal(t, syncResp1[3].(*UpdateStoreResponse).ItemId, "item1")
	assert.Equal(t, syncResp1[3].(*UpdateStoreResponse).StoreVersion, int64(4))
	assert.Equal(t, syncResp1[3].(*UpdateStoreResponse).NewItemValue,
		&MockStoreItem{From: "u1", Message: "m1"})

	assert.Equal(t, syncResp1[3], syncResp2[3])

	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload: map[string]any{
			"storeId":      "test-store",
			"itemId":       "item4",
			"newItemValue": nil},
	}, nil)

	wg.Add(2)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload: map[string]any{
			"storeId":      "test-store",
			"itemId":       "item3",
			"newItemValue": nil},
	}, nil)
	wg.Wait()

	assert.Equal(t, len(syncResp1), 5)
	assert.Equal(t, len(syncResp2), 5)

	assert.Equal(t, syncResp1[4].(*UpdateStoreResponse).ResponseType, "updateStoreResponse")
	assert.Equal(t, syncResp1[4].(*UpdateStoreResponse).StoreId, "test-store")
	assert.Equal(t, syncResp1[4].(*UpdateStoreResponse).ItemId, "item3")
	assert.Equal(t, syncResp1[4].(*UpdateStoreResponse).StoreVersion, int64(5))
	assert.Equal(t, syncResp1[4].(*UpdateStoreResponse).NewItemValue, nil)

	assert.Equal(t, syncResp1[4], syncResp2[4])

	assert.Equal(t, store.GetValue("item3"), nil)

	wg.Add(1)
	_ = bus.SendRequestMessage(syncChan, &model.Request{
		RequestCommand: updateStoreRequest,
		Payload: map[string]any{
			"storeId":      "test-store",
			"itemId":       "item3",
			"newItemValue": "test"},
	}, nil)
	wg.Wait()
	assert.Equal(t, len(syncResp1), 6)
	assert.True(t, strings.HasPrefix(syncResp1[5].(*model.Response).ErrorMessage,
		"Cannot deserialize UpdateStoreRequest item value:"))
}
