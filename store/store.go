// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"log/slog"
	"reflect"
	"sync"
)

// Describes a single store item change
type StoreChange struct {
	Id             string // the id of the updated item
	Value          any    // the updated value of the item
	State          any    // state associated with this change
	IsDeleteChange bool   // true if the item was removed from the store
	StoreVersion   int64  // the store's version when this change was made
}

// BusStore is a stateful in memory cache for objects. All state changes (any time the cache is modified)
// will broadcast that updated object to any subscribers of the BusStore for those specific objects
// or all objects of a certain type and state changes.
type BusStore interface {
	// Get the name (the id) of the store.
	GetName() string
	// Add new or updates existing item in the store.
	Put(id string, value any, state any)
	PutContext(ctx context.Context, id string, value any, state any)
	// Returns an item from the store and a boolean flag
	// indicating whether the item exists
	Get(id string) (any, bool)
	// Shorten version of the Get() method, returns only the item value.
	GetValue(id string) any
	// Remove an item from the store. Returns true if the remove operation was successful.
	Remove(id string, state any) bool
	RemoveContext(ctx context.Context, id string, state any) bool
	// Return a slice containing all store items.
	AllValues() []any
	// Return a map with all items from the store.
	AllValuesAsMap() map[string]any
	// Return a map with all items from the store with the current store version.
	AllValuesAndVersion() (map[string]any, int64)
	// Subscribe to state changes for a specific object.
	OnChange(id string, state ...any) StoreStream
	// Subscribe to state changes for all objects
	OnAllChanges(state ...any) StoreStream
	// Notify when the store has been initialize (via populate() or initialize()
	WhenReady(readyFunction func())
	// Populate the store with a map of items and their ID's.
	Populate(items map[string]any) error
	// Mark the store as initialized and notify all watchers.
	Initialize()
	// Subscribe to mutation requests made via mutate() method.
	OnMutationRequest(mutationType ...any) MutationStoreStream
	// Send a mutation request to any subscribers handling mutations.
	Mutate(request any, requestType any,
		successHandler func(any), errorHandler func(any))
	MutateContext(ctx context.Context, request any, requestType any,
		successHandler func(any), errorHandler func(any))
	// Removes all items from the store and change its state to uninitialized".
	Reset()
	// Returns true if this is galactic store.
	IsGalactic() bool
	// Get the item type if such is specified during the creation of the
	// store
	GetItemType() reflect.Type
}

// Internal BusStore implementation
type busStore struct {
	name              string
	itemsMu           sync.RWMutex
	items             map[string]any
	storeVersion      int64
	storeStreamsMu    sync.RWMutex
	storeStreams      []*storeStream
	mutationStreamsMu sync.RWMutex
	mutationStreams   []*mutationStoreStream
	lifecycleMu       sync.Mutex
	initializer       sync.Once
	readyC            chan struct{}
	isGalactic        bool
	galacticConf      *galacticStoreConfig
	bus               bus.EventBus
	itemType          reflect.Type
	storeSynHandler   bus.MessageHandler
	logger            *slog.Logger
}

type galacticStoreConfig struct {
	syncChannelConfig *storeSyncChannelConfig
}

func newBusStore(
	name string, eventBus bus.EventBus, itemType reflect.Type, galacticConf *galacticStoreConfig, loggers ...*slog.Logger) BusStore {

	store := new(busStore)
	store.name = name
	store.bus = eventBus
	store.itemType = itemType
	store.galacticConf = galacticConf
	if len(loggers) > 0 {
		store.logger = loggers[0]
	}

	initStore(store)

	store.isGalactic = galacticConf != nil

	if store.isGalactic {
		initGalacticStore(store)
	}

	return store
}

func (store *busStore) log() *slog.Logger {
	if store.logger == nil {
		return slog.Default()
	}
	return store.logger
}

func initStore(store *busStore) {
	store.readyC = make(chan struct{})
	store.storeStreams = []*storeStream{}
	store.mutationStreams = []*mutationStoreStream{}
	store.items = make(map[string]any)
	store.storeVersion = 1
	store.initializer = sync.Once{}
}

func initGalacticStore(store *busStore) {

	syncChannelConf := store.galacticConf.syncChannelConfig

	var err error
	store.storeSynHandler, err = store.bus.ListenStream(syncChannelConf.syncChannelName)
	if err != nil {
		return
	}

	store.storeSynHandler.Handle(
		func(msg *model.Message) {
			d := msg.Payload.([]byte)
			var storeResponse map[string]any

			err := json.Unmarshal(d, &storeResponse)
			if err != nil {
				store.log().Warn("failed to unmarshal storeResponse", "err", err)
				return
			}

			if storeResponse["storeId"] != store.GetName() {
				// the response is for another store
				return
			}

			responseType := storeResponse["responseType"].(string)

			switch responseType {
			case "storeContentResponse":

				store.itemsMu.Lock()

				store.updateVersionFromResponse(storeResponse)
				items := storeResponse["items"].(map[string]any)
				store.items = make(map[string]any)
				for key, val := range items {
					deserializedValue, err := store.deserializeRawValue(val)
					if err != nil {
						store.log().Warn("failed to deserialize store item value", "err", err)
						continue
					} else {
						store.items[key] = deserializedValue
					}
				}
				store.itemsMu.Unlock()
				store.Initialize()
			case "updateStoreResponse":

				store.itemsMu.Lock()

				store.updateVersionFromResponse(storeResponse)
				newItemRaw, ok := storeResponse["newItemValue"]
				itemId := storeResponse["itemId"].(string)
				var change *StoreChange
				if !ok || newItemRaw == nil {
					change, _ = store.removeInternal(itemId, "galacticSyncRemove")
				} else {
					newItemValue, err := store.deserializeRawValue(newItemRaw)
					if err != nil {
						store.itemsMu.Unlock()
						store.log().Warn("failed to deserialize store item value", "err", err)
						return
					}
					change = store.putInternal(itemId, newItemValue, "galacticSyncUpdate")
				}
				store.itemsMu.Unlock()
				store.onStoreChange(context.Background(), change)
			}
		},
		func(e error) {
		})

	store.sendOpenStoreRequest()
}

func (store *busStore) updateVersionFromResponse(storeResponse map[string]any) {
	version := storeResponse["storeVersion"]
	switch version := version.(type) {
	case float64:
		store.storeVersion = int64(version)
	case int64:
		store.storeVersion = version
	default:
		store.log().Warn("failed to deserialize store version", "value", version)
		store.storeVersion = 1
	}
}

func (store *busStore) deserializeRawValue(rawValue any) (any, error) {
	return model.ConvertValueToType(rawValue, store.itemType)
}

func (store *busStore) sendOpenStoreRequest() {
	openStoreReq := map[string]string{
		"storeId": store.GetName(),
	}
	store.sendGalacticRequestContext(context.Background(), "openStore", openStoreReq)
}

func (store *busStore) sendGalacticRequestContext(ctx context.Context, requestCmd string, requestPayload any) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}

	// create request
	id := uuid.New()
	r := &model.Request{}
	r.RequestCommand = requestCmd
	r.Payload = requestPayload
	r.Id = &id
	jsonReq, err := json.Marshal(r)
	if err != nil {
		store.log().Warn("failed to marshal store sync request", "err", err)
		return
	}

	syncChannelConfig := store.galacticConf.syncChannelConfig

	// send request.
	if err := syncChannelConfig.conn.SendJSONMessage(
		syncChannelConfig.pubPrefix+syncChannelConfig.syncChannelName,
		jsonReq); err != nil {
		store.log().Warn("failed to send store sync request", "err", err)
	}
}

func (store *busStore) sendCloseStoreRequest() {
	closeStoreReq := map[string]string{
		"storeId": store.GetName(),
	}
	store.sendGalacticRequestContext(context.Background(), "closeStore", closeStoreReq)
}

func (store *busStore) OnDestroy() {
	if store.IsGalactic() {
		store.sendCloseStoreRequest()
		if store.storeSynHandler != nil {
			store.storeSynHandler.Close()
		}
	}
}

func (store *busStore) IsGalactic() bool {
	return store.isGalactic
}

func (store *busStore) GetItemType() reflect.Type {
	return store.itemType
}

func (store *busStore) GetName() string {
	return store.name
}

func (store *busStore) Populate(items map[string]any) error {
	if store.IsGalactic() {
		return fmt.Errorf("populate() API is not supported for galactic stores")
	}

	store.itemsMu.Lock()
	defer store.itemsMu.Unlock()

	if len(store.items) > 0 {
		return fmt.Errorf("store items already initialized")
	}

	for k, v := range items {
		store.items[k] = v
	}
	store.Initialize()
	return nil
}

func (store *busStore) Put(id string, value any, state any) {
	store.PutContext(context.Background(), id, value, state)
}

func (store *busStore) PutContext(ctx context.Context, id string, value any, state any) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}
	if store.IsGalactic() {
		store.putGalactic(ctx, id, value)
	} else {
		store.itemsMu.Lock()
		change := store.putInternalContext(id, value, state)
		store.itemsMu.Unlock()
		store.onStoreChange(context.WithoutCancel(ctx), change)
	}
}

func (store *busStore) putGalactic(ctx context.Context, id string, value any) {
	store.itemsMu.RLock()
	clientStoreVersion := store.storeVersion
	store.itemsMu.RUnlock()

	store.sendUpdateStoreRequest(ctx, id, value, clientStoreVersion)
}

func (store *busStore) sendUpdateStoreRequest(ctx context.Context, id string, value any, storeVersion int64) {
	updateReq := map[string]any{
		"storeId":            store.GetName(),
		"clientStoreVersion": storeVersion,
		"itemId":             id,
		"newItemValue":       value,
	}

	store.sendGalacticRequestContext(ctx, "updateStore", updateReq)
}

func (store *busStore) putInternal(id string, value any, state any) *StoreChange {
	return store.putInternalContext(id, value, state)
}

func (store *busStore) putInternalContext(id string, value any, state any) *StoreChange {
	if !store.IsGalactic() {
		store.storeVersion++
	}
	store.items[id] = value

	return &StoreChange{
		Id:           id,
		State:        state,
		Value:        value,
		StoreVersion: store.storeVersion,
	}
}

func (store *busStore) Get(id string) (any, bool) {
	store.itemsMu.RLock()
	defer store.itemsMu.RUnlock()

	val, ok := store.items[id]

	return val, ok
}

func (store *busStore) GetValue(id string) any {
	val, _ := store.Get(id)
	return val
}

func (store *busStore) Remove(id string, state any) bool {
	return store.RemoveContext(context.Background(), id, state)
}

func (store *busStore) RemoveContext(ctx context.Context, id string, state any) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	if store.IsGalactic() {
		return store.removeGalactic(ctx, id)
	} else {
		store.itemsMu.Lock()
		change, removed := store.removeInternalContext(id, state)
		store.itemsMu.Unlock()
		if removed {
			store.onStoreChange(context.WithoutCancel(ctx), change)
		}
		return removed
	}
}

func (store *busStore) removeGalactic(ctx context.Context, id string) bool {
	store.itemsMu.RLock()
	_, ok := store.items[id]
	storeVersion := store.storeVersion
	store.itemsMu.RUnlock()

	if ok {
		store.sendUpdateStoreRequest(ctx, id, nil, storeVersion)
		return true
	}
	return false
}

func (store *busStore) removeInternal(id string, state any) (*StoreChange, bool) {
	return store.removeInternalContext(id, state)
}

func (store *busStore) removeInternalContext(id string, state any) (*StoreChange, bool) {
	value, ok := store.items[id]
	if !ok {
		return nil, false
	}

	if !store.IsGalactic() {
		store.storeVersion++
	}
	delete(store.items, id)

	return &StoreChange{
		Id:             id,
		State:          state,
		Value:          value,
		StoreVersion:   store.storeVersion,
		IsDeleteChange: true,
	}, true
}

func (store *busStore) AllValues() []any {

	store.itemsMu.RLock()
	defer store.itemsMu.RUnlock()

	values := make([]any, 0, len(store.items))
	for _, value := range store.items {
		values = append(values, value)
	}

	return values
}

func (store *busStore) AllValuesAsMap() map[string]any {
	store.itemsMu.RLock()
	defer store.itemsMu.RUnlock()

	values := make(map[string]any)

	for key, value := range store.items {
		values[key] = value
	}

	return values
}

func (store *busStore) AllValuesAndVersion() (map[string]any, int64) {
	store.itemsMu.RLock()
	defer store.itemsMu.RUnlock()

	values := make(map[string]any)

	for key, value := range store.items {
		values[key] = value
	}

	return values, store.storeVersion
}

func (store *busStore) OnMutationRequest(requestType ...any) MutationStoreStream {
	return newMutationStoreStream(store, &mutationStreamFilter{
		requestTypes: requestType,
	})
}

func (store *busStore) Mutate(request any, requestType any,
	successHandler func(any), errorHandler func(any)) {
	store.MutateContext(context.Background(), request, requestType, successHandler, errorHandler)
}

func (store *busStore) MutateContext(ctx context.Context, request any, requestType any,
	successHandler func(any), errorHandler func(any)) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}

	store.mutationStreamsMu.RLock()
	streams := append([]*mutationStoreStream(nil), store.mutationStreams...)
	store.mutationStreamsMu.RUnlock()

	mutationRequest := &MutationRequest{
		Request:        request,
		RequestType:    requestType,
		SuccessHandler: successHandler,
		ErrorHandler:   errorHandler,
	}
	for _, ms := range streams {
		ms.onMutationRequest(ctx, mutationRequest)
	}
}

func (store *busStore) onStoreChange(ctx context.Context, change *StoreChange) {
	if change == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}
	store.storeStreamsMu.RLock()
	streams := append([]*storeStream(nil), store.storeStreams...)
	store.storeStreamsMu.RUnlock()

	for _, storeStream := range streams {
		storeStream.onStoreChange(ctx, change)
	}
}

func (store *busStore) Initialize() {
	store.lifecycleMu.Lock()
	defer store.lifecycleMu.Unlock()

	store.initializer.Do(func() {
		close(store.readyC)
		store.bus.SendMonitorEvent(StoreInitializedEvt, store.name, nil)
	})
}

func (store *busStore) Reset() {
	store.itemsMu.Lock()
	defer store.itemsMu.Unlock()

	store.mutationStreamsMu.Lock()
	defer store.mutationStreamsMu.Unlock()

	store.storeStreamsMu.Lock()
	defer store.storeStreamsMu.Unlock()

	store.lifecycleMu.Lock()
	defer store.lifecycleMu.Unlock()

	initStore(store)

	if store.IsGalactic() {
		store.sendOpenStoreRequest()
	}
}

func (store *busStore) WhenReady(readyFunc func()) {
	store.lifecycleMu.Lock()
	readyC := store.readyC
	store.lifecycleMu.Unlock()

	go func() {
		<-readyC
		readyFunc()
	}()
}

func (store *busStore) OnChange(id string, state ...any) StoreStream {
	return newStoreStream(store, &streamFilter{
		itemId: id,
		states: state,
	})
}

func (store *busStore) OnAllChanges(state ...any) StoreStream {
	return newStoreStream(store, &streamFilter{
		states:        state,
		matchAllItems: true,
	})
}

func (store *busStore) onStreamSubscribe(stream *storeStream) {
	store.storeStreamsMu.Lock()
	defer store.storeStreamsMu.Unlock()

	store.storeStreams = append(store.storeStreams, stream)
}

func (store *busStore) onMutationStreamSubscribe(stream *mutationStoreStream) {
	store.mutationStreamsMu.Lock()
	defer store.mutationStreamsMu.Unlock()

	store.mutationStreams = append(store.mutationStreams, stream)
}

func (store *busStore) onStreamUnsubscribe(stream *storeStream) {
	store.storeStreamsMu.Lock()
	defer store.storeStreamsMu.Unlock()

	var i int
	var s *storeStream
	for i, s = range store.storeStreams {
		if s == stream {
			break
		}
	}

	if s == stream {
		n := len(store.storeStreams)
		store.storeStreams[i] = store.storeStreams[n-1]
		store.storeStreams = store.storeStreams[:n-1]
	}
}

func (store *busStore) onMutationStreamUnsubscribe(stream *mutationStoreStream) {
	store.mutationStreamsMu.Lock()
	defer store.mutationStreamsMu.Unlock()

	var i int
	var s *mutationStoreStream
	for i, s = range store.mutationStreams {
		if s == stream {
			break
		}
	}

	if s == stream {
		n := len(store.mutationStreams)
		store.mutationStreams[i] = store.mutationStreams[n-1]
		store.mutationStreams = store.mutationStreams[:n-1]
	}
}
