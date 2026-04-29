// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/pb33f/ranch/plank/pkg/routing"
)

// MiddlewareManager manages route-specific and global HTTP middleware.
type MiddlewareManager interface {
	SetGlobalMiddleware(middleware []routing.MiddlewareFunc) error
	SetNewMiddleware(route *routing.Route, middleware []routing.MiddlewareFunc) error
	RemoveMiddleware(route *routing.Route) error
	GetRouteByUriAndMethod(uri, method string) (*routing.Route, error)
	GetRouteByUri(uri string) (*routing.Route, error)
	GetStaticRoute(prefix string) (*routing.Route, error)
}

// Middleware names a routing middleware interceptor.
type Middleware interface {
	// Intercept(h http.Handler) http.Handler
	Interceptor() routing.MiddlewareFunc
	Name() string
}

type middlewareManager struct {
	endpointHandlerMap  *map[string]http.HandlerFunc
	originalHandlersMap map[string]http.HandlerFunc
	router              *routing.Router
	mu                  sync.Mutex
	logger              *slog.Logger
}

func (m *middlewareManager) SetGlobalMiddleware(middleware []routing.MiddlewareFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.router.Use(middleware...)
	return nil
}

func (m *middlewareManager) SetNewMiddleware(route *routing.Route, middleware []routing.MiddlewareFunc) error {
	if route == nil {
		return fmt.Errorf("failed to set a new middleware. route does not exist")
	}

	var key string
	// expection is that a route's name ending with '*' means it's a prefix route
	routeName := route.GetName()
	isPrefixRoute := routeName != "" && routeName[len(routeName)-1] == '*'

	if !isPrefixRoute {
		uri, method := m.extractUriVerbFromRoute(route)
		// for REST-bridge service a key is in the format of {uri}-{verb}
		key = uri + "-" + method
	} else {
		// if the route instance is a prefix route use the route name as-is
		key = routeName
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// find if base handler exists first. if not, error out
	original, exists := (*m.endpointHandlerMap)[key]
	if !exists {
		return fmt.Errorf("cannot set middleware. handler does not exist at %s", key)
	}

	// make a backup of the original handler that has no other middleware attached to it
	if _, exists := m.originalHandlersMap[key]; !exists {
		m.originalHandlersMap[key] = original
	}

	// build a new middleware chain and apply it
	handler := m.buildMiddlewareChain(middleware, original).(http.HandlerFunc)
	(*m.endpointHandlerMap)[key] = handler
	route.Handler(handler)

	for _, mw := range middleware {
		m.logger.Debug("middleware registered", "name", mw, "key", key)
	}

	m.logger.Info("New middleware configured for REST bridge", "key", key)

	return nil
}

func (m *middlewareManager) RemoveMiddleware(route *routing.Route) error {
	if route == nil {
		return fmt.Errorf("failed to remove middleware. route does not exist")
	}
	uri, method := m.extractUriVerbFromRoute(route)
	m.mu.Lock()
	defer m.mu.Unlock()
	key := uri + "-" + method
	if _, found := (*m.endpointHandlerMap)[key]; !found {
		return fmt.Errorf("failed to remove handler. REST bridge handler does not exist at %s (%s)", uri, method)
	}
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error(fmt.Sprint(r))
		}
	}()

	(*m.endpointHandlerMap)[key] = m.originalHandlersMap[key]
	route.Handler(m.originalHandlersMap[key])
	m.logger.Debug("All middleware have been stripped", "url", uri, "method", method)

	return nil
}

func (m *middlewareManager) GetRouteByUriAndMethod(uri, method string) (*routing.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	route := m.router.Get(fmt.Sprintf("%s-%s", uri, method))
	if route == nil {
		return nil, fmt.Errorf("no route found at %s (%s)", uri, method)
	}
	return route, nil
}

func (m *middlewareManager) GetStaticRoute(prefix string) (*routing.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	routeName := prefix + "*"
	route := m.router.Get(routeName)
	if route == nil {
		return nil, fmt.Errorf("no route found at static prefix %s", routeName)
	}
	return route, nil
}

func (m *middlewareManager) GetRouteByUri(uri string) (*routing.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	route := m.router.Get(uri)
	if route == nil {
		return nil, fmt.Errorf("no route found at %s", uri)
	}
	return route, nil
}

func (m *middlewareManager) buildMiddlewareChain(handlers []routing.MiddlewareFunc, originalHandler http.Handler) http.Handler {
	var idx = len(handlers) - 1
	var finalHandler http.Handler

	for idx >= 0 {
		var currHandler http.Handler
		if idx == len(handlers)-1 {
			currHandler = originalHandler
		} else {
			currHandler = finalHandler
		}
		middlewareFn := handlers[idx]
		finalHandler = middlewareFn(currHandler)
		idx--
	}

	return finalHandler
}

// extractUriVerbFromRoute takes *routing.Route and returns URI and verb as string values
func (m *middlewareManager) extractUriVerbFromRoute(route *routing.Route) (string, string) {
	opRawString := route.GetName()
	delimiterIdx := strings.LastIndex(opRawString, "-")
	return opRawString[:delimiterIdx], opRawString[delimiterIdx+1:]
}

func (m *middlewareManager) SetRouter(router *routing.Router) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.router = router
}

// NewMiddlewareManager sets up a new middleware manager singleton instance
func NewMiddlewareManager(endpointHandlerMapPtr *map[string]http.HandlerFunc, router *routing.Router, logger *slog.Logger) MiddlewareManager {
	return &middlewareManager{
		endpointHandlerMap:  endpointHandlerMapPtr,
		originalHandlersMap: make(map[string]http.HandlerFunc),
		router:              router,
		logger:              logger,
	}
}
