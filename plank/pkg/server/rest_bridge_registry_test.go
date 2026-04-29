// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/plank/pkg/routing"
	"github.com/pb33f/ranch/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestBridgeRegistrySetExact(t *testing.T) {
	registry, handlers, eventBus := newTestRestBridgeRegistry(t)

	cfg := restBridgeConfig("registry-exact", "/exact", http.MethodGet)
	createBridgeChannel(eventBus, cfg)
	registry.SetExact(cfg)

	_, exists := handlers["/exact-GET"]
	require.True(t, exists)

	rsp := httptest.NewRecorder()
	registry.router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/exact", nil))
	assert.Equal(t, http.StatusAccepted, rsp.Code)
	assert.Equal(t, "registry-exact", rsp.Body.String())
}

func TestRestBridgeRegistrySetPrefix(t *testing.T) {
	registry, handlers, eventBus := newTestRestBridgeRegistry(t)

	cfg := restBridgeConfig("registry-prefix", "/prefix", http.MethodGet)
	createBridgeChannel(eventBus, cfg)
	registry.SetPrefix(cfg)

	_, exists := handlers["/prefix-*"]
	require.True(t, exists)

	for _, path := range []string{"/prefix", "/prefix/child"} {
		rsp := httptest.NewRecorder()
		registry.router.ServeHTTP(rsp, httptest.NewRequest(http.MethodPost, path, nil))
		assert.Equal(t, http.StatusAccepted, rsp.Code)
		assert.Equal(t, "registry-prefix", rsp.Body.String())
	}
}

func TestRestBridgeRegistryClearForServicePreservesOtherRoutes(t *testing.T) {
	registry, handlers, eventBus := newTestRestBridgeRegistry(t)

	cfgA := restBridgeConfig("registry-clear-a", "/clear-a", http.MethodGet)
	cfgB := restBridgeConfig("registry-clear-b", "/clear-b", http.MethodGet)
	createBridgeChannel(eventBus, cfgA)
	createBridgeChannel(eventBus, cfgB)
	registry.SetExact(cfgA)
	registry.SetExact(cfgB)

	newRouter := registry.ClearForService("registry-clear-a")

	_, exists := handlers["/clear-a-GET"]
	assert.False(t, exists)
	_, exists = handlers["/clear-b-GET"]
	assert.True(t, exists)

	rsp := httptest.NewRecorder()
	newRouter.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/clear-a", nil))
	assert.Equal(t, http.StatusNotFound, rsp.Code)

	rsp = httptest.NewRecorder()
	newRouter.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/clear-b", nil))
	assert.Equal(t, http.StatusAccepted, rsp.Code)
	assert.Equal(t, "registry-clear-b", rsp.Body.String())
}

func TestRestBridgeRegistryMessageBridgeFanOut(t *testing.T) {
	registry, _, eventBus := newTestRestBridgeRegistry(t)

	cfg := restBridgeConfig("registry-fanout", "/fanout", http.MethodGet)
	createBridgeChannel(eventBus, cfg)
	registry.SetExact(cfg)

	bridge := registry.messageBridgeMap[cfg.ServiceChannel]
	require.NotNil(t, bridge)

	err := eventBus.SendResponseMessage(cfg.ServiceChannel, &model.Response{Payload: "ok"}, nil)
	require.NoError(t, err)

	select {
	case msg := <-bridge.payloadChannel:
		assert.Equal(t, cfg.ServiceChannel, msg.Channel)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bridge payload")
	}
}

func TestRestBridgeRegistryDuplicateBridgeIsIdempotent(t *testing.T) {
	registry, handlers, eventBus := newTestRestBridgeRegistry(t)

	cfg := restBridgeConfig("registry-duplicate", "/duplicate", http.MethodGet)
	createBridgeChannel(eventBus, cfg)
	registry.SetExact(cfg)
	registry.SetExact(cfg)

	assert.Len(t, handlers, 1)
	assert.Len(t, registry.messageBridgeMap, 1)
	assert.Len(t, registry.serviceChanToBridgeEndpoints[cfg.ServiceChannel], 1)
}

func newTestRestBridgeRegistry(t *testing.T) (*RestBridgeRegistry, map[string]http.HandlerFunc, bus.EventBus) {
	t.Helper()

	eventBus := bus.NewEventBus()
	router := routing.NewRouter()
	handlers := make(map[string]http.HandlerFunc)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	buildHandler := func(
		svcChannel string,
		_ service.RequestBuilder,
		_ time.Duration,
		_ chan *model.Message,
	) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(svcChannel))
		}
	}

	registry := NewRestBridgeRegistry(router, &handlers, eventBus, logger, time.Second, buildHandler)
	return registry, handlers, eventBus
}

func restBridgeConfig(serviceChannel, uri, method string) *service.RESTBridgeConfig {
	return &service.RESTBridgeConfig{
		ServiceChannel: serviceChannel,
		Uri:            uri,
		Method:         method,
		FabricRequestBuilder: func(http.ResponseWriter, *http.Request) model.Request {
			return model.Request{Id: &uuid.UUID{}}
		},
	}
}

func createBridgeChannel(eventBus bus.EventBus, cfg *service.RESTBridgeConfig) {
	eventBus.GetChannelManager().CreateChannel(cfg.ServiceChannel)
}
