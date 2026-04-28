// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package store

import (
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/transport/fabric"
	"log/slog"
	"strings"
	"sync"
)

const (
	openStoreRequest        = "openStore"
	updateStoreRequest      = "updateStore"
	closeStoreRequest       = "closeStore"
	galacticStoreSyncUpdate = "galacticStoreSyncUpdate"
	galacticStoreSyncRemove = "galacticStoreSyncRemove"
)

type storeSyncService struct {
	bus                bus.EventBus
	manager            Manager
	logger             *slog.Logger
	lock               sync.Mutex
	syncClients        map[string]*syncClientChannel
	syncStoreListeners map[string]*syncStoreListener
}

type syncStoreListener struct {
	storeStream        StoreStream
	clientSyncChannels map[string]bool
	logger             *slog.Logger
	lock               sync.RWMutex
}

type syncClientChannel struct {
	channelName           string
	clientRequestListener bus.MessageHandler
	openStores            map[string]bool
}

func newStoreSyncService(eventBus bus.EventBus, manager Manager, logger *slog.Logger) *storeSyncService {
	if logger == nil {
		logger = slog.Default()
	}
	syncService := &storeSyncService{
		bus:                eventBus,
		manager:            manager,
		logger:             logger,
		syncClients:        make(map[string]*syncClientChannel),
		syncStoreListeners: make(map[string]*syncStoreListener),
	}
	syncService.init()
	return syncService
}

func (syncService *storeSyncService) log() *slog.Logger {
	if syncService.logger == nil {
		return slog.Default()
	}
	return syncService.logger
}

func (syncService *storeSyncService) init() {
	syncService.bus.AddMonitorEventListener(
		func(monitorEvt *bus.MonitorEvent) {
			if !strings.HasPrefix(monitorEvt.EntityName, "transport-store-sync.") {
				// not a store sync channel, ignore the message
				return
			}

			switch monitorEvt.EventType {
			case fabric.FabricEndpointSubscribeEvt:
				syncService.openNewClientSyncChannel(monitorEvt.EntityName)
			case bus.ChannelDestroyedEvt:
				syncService.closeClientSyncChannel(monitorEvt.EntityName)
			}
		},
		fabric.FabricEndpointSubscribeEvt, bus.ChannelDestroyedEvt)
}

func (syncService *storeSyncService) openNewClientSyncChannel(channelName string) {
	syncService.lock.Lock()
	defer syncService.lock.Unlock()

	if _, ok := syncService.syncClients[channelName]; ok {
		// channel already opened.
		return
	}

	syncClient := &syncClientChannel{
		channelName: channelName,
		openStores:  make(map[string]bool),
	}
	syncClient.clientRequestListener, _ = syncService.bus.ListenRequestStream(channelName)
	if syncClient.clientRequestListener != nil {
		syncClient.clientRequestListener.Handle(
			func(message *model.Message) {
				request, reqOk := message.Payload.(*model.Request)
				if !reqOk || request.Payload == nil {
					return
				}
				var storeRequest map[string]any
				storeRequest, ok := request.Payload.(map[string]any)
				if !ok {
					return
				}

				switch request.RequestCommand {
				case openStoreRequest:
					syncService.openStore(syncClient, storeRequest, request.Id)
				case closeStoreRequest:
					syncService.closeStore(syncClient, storeRequest, request.Id)
				case updateStoreRequest:
					syncService.updateStore(syncClient, storeRequest, request.Id)
				}
			}, func(e error) {})
	}
	syncService.syncClients[channelName] = syncClient
}

func (syncService *storeSyncService) closeClientSyncChannel(channelName string) {
	syncService.lock.Lock()
	defer syncService.lock.Unlock()

	syncClient, ok := syncService.syncClients[channelName]
	if !ok || syncClient == nil {
		// client is already closed
		return
	}

	for storeId := range syncClient.openStores {
		listener := syncService.syncStoreListeners[storeId]
		if listener != nil {
			listener.removeChannel(channelName)
			if listener.isEmpty() {
				listener.unsubscribe()
				delete(syncService.syncStoreListeners, storeId)
			}
		}
	}

	delete(syncService.syncClients, channelName)
}

func (syncService *storeSyncService) openStore(
	syncClient *syncClientChannel, request map[string]any, reqId *uuid.UUID) {

	storeId, ok := getStringProperty("storeId", request)
	if !ok || storeId == "" {
		syncService.sendErrorResponse(syncClient.channelName, "Invalid OpenStoreRequest", reqId)
		return
	}

	store := syncService.manager.GetStore(storeId)
	if store == nil {
		syncService.sendErrorResponse(
			syncClient.channelName, "Cannot open non-existing store: "+storeId, reqId)
		return
	}

	syncService.lock.Lock()
	defer syncService.lock.Unlock()

	syncClient.openStores[storeId] = true

	storeListener, ok := syncService.syncStoreListeners[storeId]
	if !ok {
		storeListener = newSyncStoreListener(syncService.bus, store, syncService.logger)
		syncService.syncStoreListeners[storeId] = storeListener
	}
	storeListener.addChannel(syncClient.channelName)

	store.WhenReady(func() {
		items, version := store.AllValuesAndVersion()

		if err := syncService.bus.SendResponseMessage(syncClient.channelName,
			NewStoreContentResponse(storeId, items, version), nil); err != nil {
			syncService.log().Warn("failed to send store content response", "err", err, "channel", syncClient.channelName)
		}
	})
}

func (syncService *storeSyncService) closeStore(
	syncClient *syncClientChannel, request map[string]any, reqId *uuid.UUID) {

	storeId, ok := getStringProperty("storeId", request)
	if !ok || storeId == "" {
		syncService.sendErrorResponse(syncClient.channelName, "Invalid CloseStoreRequest", reqId)
		return
	}

	syncService.lock.Lock()
	defer syncService.lock.Unlock()

	delete(syncClient.openStores, storeId)

	storeListener, ok := syncService.syncStoreListeners[storeId]
	if ok && storeListener != nil {
		storeListener.removeChannel(syncClient.channelName)
		if storeListener.isEmpty() {
			storeListener.unsubscribe()
			delete(syncService.syncStoreListeners, storeId)
		}
	}
}

func (syncService *storeSyncService) updateStore(
	syncClient *syncClientChannel, request map[string]any, reqId *uuid.UUID) {

	storeId, ok := getStringProperty("storeId", request)
	if !ok || storeId == "" {
		syncService.sendErrorResponse(
			syncClient.channelName, "Invalid UpdateStoreRequest: missing storeId", reqId)
		return
	}
	itemId, ok := getStringProperty("itemId", request)
	if !ok || itemId == "" {
		syncService.sendErrorResponse(
			syncClient.channelName, "Invalid UpdateStoreRequest: missing itemId", reqId)
		return
	}

	store := syncService.manager.GetStore(storeId)
	if store == nil {
		syncService.sendErrorResponse(
			syncClient.channelName, "Cannot update non-existing store: "+storeId, reqId)
		return
	}

	rawValue := request["newItemValue"]
	if rawValue == nil {
		store.Remove(itemId, galacticStoreSyncRemove)
	} else {
		deserializedValue, err := model.ConvertValueToType(rawValue, store.GetItemType())
		if err != nil || deserializedValue == nil {
			errMsg := "Cannot deserialize UpdateStoreRequest item value"
			if err != nil {
				errMsg = "Cannot deserialize UpdateStoreRequest item value: " + err.Error()
			}
			syncService.sendErrorResponse(syncClient.channelName, errMsg, reqId)
			return
		}
		store.Put(itemId, deserializedValue, galacticStoreSyncUpdate)
	}
}

func getStringProperty(id string, request map[string]any) (string, bool) {
	propValue, ok := request[id]
	if !ok || propValue == nil {
		return "", false
	}
	stringValue, ok := propValue.(string)
	return stringValue, ok
}

func (syncService *storeSyncService) sendErrorResponse(
	clientChannel string, errorMsg string, reqId *uuid.UUID) {

	if err := syncService.bus.SendResponseMessage(clientChannel, &model.Response{
		Id:           reqId,
		Error:        true,
		ErrorCode:    1,
		ErrorMessage: errorMsg,
	}, nil); err != nil {
		syncService.log().Warn("failed to send store sync error response", "err", err, "channel", clientChannel)
	}
}

func newSyncStoreListener(eventBus bus.EventBus, store BusStore, logger *slog.Logger) *syncStoreListener {
	if logger == nil {
		logger = slog.Default()
	}

	listener := &syncStoreListener{
		storeStream:        store.OnAllChanges(),
		clientSyncChannels: make(map[string]bool),
		logger:             logger,
	}

	if err := listener.storeStream.Subscribe(func(change *StoreChange) {
		updateStoreResp := NewUpdateStoreResponse(
			store.GetName(), change.Id, change.Value, change.StoreVersion)
		if change.IsDeleteChange {
			updateStoreResp.NewItemValue = nil
		}

		listener.lock.RLock()
		defer listener.lock.RUnlock()

		for chName := range listener.clientSyncChannels {
			if err := eventBus.SendResponseMessage(chName, updateStoreResp, nil); err != nil {
				listener.log().Warn("failed to send store update response", "err", err, "channel", chName)
			}
		}
	}); err != nil {
		listener.log().Warn("failed to subscribe store sync listener", "err", err, "store", store.GetName())
	}

	return listener
}

func (l *syncStoreListener) log() *slog.Logger {
	if l.logger == nil {
		return slog.Default()
	}
	return l.logger
}

func (l *syncStoreListener) unsubscribe() {

	if err := l.storeStream.Unsubscribe(); err != nil {
		l.log().Warn("failed to unsubscribe store sync listener", "err", err)
	}
}

func (l *syncStoreListener) addChannel(clientChannel string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.clientSyncChannels[clientChannel] = true
}

func (l *syncStoreListener) removeChannel(clientChannel string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	delete(l.clientSyncChannels, clientChannel)
}

func (l *syncStoreListener) isEmpty() bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return len(l.clientSyncChannels) == 0
}
