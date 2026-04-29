// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/plank/pkg/routing"
	"github.com/pb33f/ranch/service"
)

// BuildHandlerFunc builds the HTTP handler used by a REST bridge route.
type BuildHandlerFunc func(
	svcChannel string,
	reqBuilder service.RequestBuilder,
	restBridgeTimeout time.Duration,
	msgChan chan *model.Message,
) http.HandlerFunc

// MessageBridge is a conduit used for returning service responses as HTTP responses.
type MessageBridge struct {
	ServiceListenStream bus.MessageHandler  // message handler returned by bus.ListenStream responsible for relaying back messages as HTTP responses
	payloadChannel      chan *model.Message // internal golang channel used for passing bus responses/errors across goroutines
}

// RestBridgeRegistry owns REST bridge route registration and response fan-out state.
type RestBridgeRegistry struct {
	mu                           sync.Mutex
	endpointHandlerMap           *map[string]http.HandlerFunc
	serviceChanToBridgeEndpoints map[string][]string
	messageBridgeMap             map[string]*MessageBridge

	router        *routing.Router
	eventBus      bus.EventBus
	logger        *slog.Logger
	bridgeTimeout time.Duration
	buildHandler  BuildHandlerFunc
}

// NewRestBridgeRegistry creates a registry for REST bridge routes and handlers.
func NewRestBridgeRegistry(
	router *routing.Router,
	endpointHandlerMap *map[string]http.HandlerFunc,
	eventBus bus.EventBus,
	logger *slog.Logger,
	bridgeTimeout time.Duration,
	buildHandler BuildHandlerFunc,
) *RestBridgeRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	if endpointHandlerMap == nil {
		handlerMap := make(map[string]http.HandlerFunc)
		endpointHandlerMap = &handlerMap
	}
	return &RestBridgeRegistry{
		endpointHandlerMap:           endpointHandlerMap,
		serviceChanToBridgeEndpoints: make(map[string][]string),
		messageBridgeMap:             make(map[string]*MessageBridge),
		router:                       router,
		eventBus:                     eventBus,
		logger:                       logger,
		bridgeTimeout:                bridgeTimeout,
		buildHandler:                 buildHandler,
	}
}

// SetExact registers an exact-path REST bridge route.
func (r *RestBridgeRegistry) SetExact(bridgeConfig *service.RESTBridgeConfig) {
	r.set(bridgeConfig, httpBridgeExact)
}

// SetPrefix registers a prefix REST bridge route.
func (r *RestBridgeRegistry) SetPrefix(bridgeConfig *service.RESTBridgeConfig) {
	r.set(bridgeConfig, httpBridgePrefix)
}

func (r *RestBridgeRegistry) set(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) {
	if bridgeConfig == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	endpointHandlerKey := bridgeEndpointHandlerKey(bridgeConfig, mode)
	if _, ok := (*r.endpointHandlerMap)[endpointHandlerKey]; ok {
		r.logDuplicateBridge(bridgeConfig, mode)
		return
	}

	if r.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel] == nil {
		r.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel] = make([]string, 0)
	}

	bridge, ok := r.ensureMessageBridge(bridgeConfig.ServiceChannel)
	if !ok {
		return
	}

	(*r.endpointHandlerMap)[endpointHandlerKey] = r.buildHandler(
		bridgeConfig.ServiceChannel,
		bridgeConfig.FabricRequestBuilder,
		r.bridgeTimeout,
		bridge.payloadChannel)

	r.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel] = append(
		r.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel], endpointHandlerKey)

	r.registerHttpBridgeRoute(bridgeConfig, endpointHandlerKey, mode)
	r.logBridgeRegistration(bridgeConfig, mode)
}

// ClearForService removes REST bridge routes for serviceChannel and returns the rebuilt router.
func (r *RestBridgeRegistry) ClearForService(serviceChannel string) *routing.Router {
	r.mu.Lock()
	defer r.mu.Unlock()

	lookupMap := make(map[string]bool)
	for _, key := range r.serviceChanToBridgeEndpoints[serviceChannel] {
		lookupMap[key] = true
	}
	newRouter := r.router.CloneExcluding(lookupMap)

	existingMappings := r.serviceChanToBridgeEndpoints[serviceChannel]
	r.serviceChanToBridgeEndpoints[serviceChannel] = make([]string, 0)
	for _, handlerKey := range existingMappings {
		r.logger.Info("[ranch] Removing existing service - REST mapping", "key", handlerKey, "channel", serviceChannel)
		delete(*r.endpointHandlerMap, handlerKey)
	}
	return newRouter
}

// SetRouter points future registrations at router after a router rebuild.
func (r *RestBridgeRegistry) SetRouter(router *routing.Router) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.router = router
}

func (r *RestBridgeRegistry) ensureMessageBridge(serviceChannel string) (*MessageBridge, bool) {
	if bridge, exists := r.messageBridgeMap[serviceChannel]; exists {
		return bridge, true
	}

	handler, err := r.eventBus.ListenStream(serviceChannel)
	if handler == nil || err != nil {
		if err != nil {
			r.logger.Error("failed to create REST bridge response listener", "channel", serviceChannel, "err", err)
		}
		return nil, false
	}

	bridge := &MessageBridge{
		ServiceListenStream: handler,
		payloadChannel:      make(chan *model.Message, 100),
	}
	r.messageBridgeMap[serviceChannel] = bridge
	handler.Handle(func(message *model.Message) {
		bridge.payloadChannel <- message
	}, func(error) {})
	return bridge, true
}

func (r *RestBridgeRegistry) registerHttpBridgeRoute(
	bridgeConfig *service.RESTBridgeConfig, endpointHandlerKey string, mode httpBridgeMatchMode) {
	handler := (*r.endpointHandlerMap)[endpointHandlerKey]
	if mode == httpBridgePrefix {
		r.router.
			PathPrefix(bridgeConfig.Uri).
			Name(endpointHandlerKey).
			Handler(handler)
		return
	}

	permittedMethods := []string{bridgeConfig.Method}
	if bridgeConfig.AllowHead {
		permittedMethods = append(permittedMethods, http.MethodHead)
	}
	if bridgeConfig.AllowOptions {
		permittedMethods = append(permittedMethods, http.MethodOptions)
	}

	r.router.
		Path(bridgeConfig.Uri).
		Methods(permittedMethods...).
		Name(fmt.Sprintf("%s-%s", bridgeConfig.Uri, bridgeConfig.Method)).
		Handler(handler)
}

func (r *RestBridgeRegistry) logDuplicateBridge(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) {
	if mode == httpBridgePrefix {
		r.logger.Warn("[ranch] path prefix is already being handled. "+
			"Try another prefix or remove it before assigning a new handler", "uri", bridgeConfig.Uri, "method", bridgeConfig.Method)
		return
	}

	r.logger.Warn("[ranch] endpoint is already associated with a handler, "+
		"Try another endpoint or remove it before assigning a new handler", "uri", bridgeConfig.Uri, "method", bridgeConfig.Method)
}

func (r *RestBridgeRegistry) logBridgeRegistration(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) {
	if mode == httpBridgePrefix {
		r.logger.Info(
			"[ranch] Service channel is now bridged to a REST path prefix",
			"channel", bridgeConfig.ServiceChannel, "url", bridgeConfig.Uri)
		return
	}
	r.logger.Info(
		"[ranch] service channel is bridged to a REST endpoint",
		"channel", bridgeConfig.ServiceChannel, "url", bridgeConfig.Uri, "method", bridgeConfig.Method)
}

func bridgeEndpointHandlerKey(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) string {
	if mode == httpBridgePrefix {
		return bridgeConfig.Uri + "-" + AllMethodsWildcard
	}
	return bridgeConfig.Uri + "-" + bridgeConfig.Method
}
