// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package fabric

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
)

type MessageRouter interface {
	RegisterRoute(spec RouteSpec) (RouteHandle, error)
	RouteMessage(ctx context.Context, channel string, msg *model.Message) error
	Close() error
}

type RouteSpec struct {
	BusChannel    string
	DefaultDest   string
	AllowOverride bool
	Direction     model.Direction
}

type RouteHandle interface {
	Close() error
}

type messageRouter struct {
	bus      bus.EventBus
	endpoint routeEndpoint
	logger   *slog.Logger
	mu       sync.Mutex
	routes   map[string]*routeHandle
	closed   bool
}

type routeEndpoint interface {
	routeConfig() *EndpointConfig
	detachSubscriptionForwarder(channel string) bus.MessageHandler
	sendRouteError(destination string, err error)
	forwardRouteMessage(channel, defaultDestination string, allowOverride bool, message *model.Message)
}

type routeHandle struct {
	router *messageRouter
	spec   RouteSpec
	stream bus.MessageHandler
	once   sync.Once
}

func newMessageRouter(endpoint routeEndpoint, bus bus.EventBus, logger *slog.Logger) *messageRouter {
	if logger == nil {
		logger = slog.Default()
	}
	return &messageRouter{
		bus:      bus,
		endpoint: endpoint,
		logger:   logger,
		routes:   make(map[string]*routeHandle),
	}
}

func TopicDestination(cfg *EndpointConfig, channel string) string {
	prefix := "/topic/"
	if cfg != nil && cfg.TopicPrefix != "" {
		prefix = ensureTrailingSlash(cfg.TopicPrefix)
	}
	return prefix + strings.TrimPrefix(channel, "/")
}

func (r *messageRouter) RegisterRoute(spec RouteSpec) (RouteHandle, error) {
	if spec.BusChannel == "" {
		return nil, fmt.Errorf("route bus channel is required")
	}
	if spec.DefaultDest == "" {
		var cfg *EndpointConfig
		if r.endpoint != nil {
			cfg = r.endpoint.routeConfig()
		}
		spec.DefaultDest = TopicDestination(cfg, spec.BusChannel)
	}
	if r.bus == nil {
		return nil, fmt.Errorf("route bus is nil")
	}
	if r.endpoint == nil {
		return nil, fmt.Errorf("route endpoint is nil")
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("message router is closed")
	}
	r.mu.Unlock()

	stream, err := r.listenRoute(spec)
	if err != nil {
		return nil, err
	}

	handle := &routeHandle{
		router: r,
		spec:   spec,
		stream: stream,
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = handle.closeStream()
		return nil, fmt.Errorf("message router is closed")
	}
	if _, exists := r.routes[spec.BusChannel]; exists {
		r.mu.Unlock()
		_ = handle.closeStream()
		return nil, fmt.Errorf("route already registered for channel %q", spec.BusChannel)
	}
	// Publish the route before detaching the fallback forwarder so concurrent subscribers see it.
	r.routes[spec.BusChannel] = handle
	r.mu.Unlock()

	var detached bus.MessageHandler
	if r.endpoint != nil {
		detached = r.endpoint.detachSubscriptionForwarder(spec.BusChannel)
	}
	if detached != nil {
		detached.Close()
	}

	stream.HandleContext(context.Background(), func(ctx context.Context, message *model.Message) {
		if err := r.RouteMessage(ctx, spec.BusChannel, message); err != nil {
			r.logger.Error("failed to route fabric message", "channel", spec.BusChannel, "err", err)
		}
	}, func(_ context.Context, err error) {
		r.endpoint.sendRouteError(spec.DefaultDest, err)
	})
	return handle, nil
}

func (r *messageRouter) listenRoute(spec RouteSpec) (bus.MessageHandler, error) {
	switch spec.Direction {
	case model.RequestDir:
		return r.bus.ListenRequestStream(spec.BusChannel)
	case model.ResponseDir:
		return r.bus.ListenStream(spec.BusChannel)
	default:
		return nil, fmt.Errorf("unsupported route direction %d", spec.Direction)
	}
}

func (r *messageRouter) RouteMessage(ctx context.Context, channel string, msg *model.Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	r.mu.Lock()
	handle, exists := r.routes[channel]
	r.mu.Unlock()
	if !exists {
		return fmt.Errorf("route not registered for channel %q", channel)
	}

	r.endpoint.forwardRouteMessage(handle.spec.BusChannel, handle.spec.DefaultDest, handle.spec.AllowOverride, msg)
	return nil
}

func (r *messageRouter) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	handles := make([]*routeHandle, 0, len(r.routes))
	for _, handle := range r.routes {
		handles = append(handles, handle)
	}
	r.routes = make(map[string]*routeHandle)
	r.closed = true
	r.mu.Unlock()

	for _, handle := range handles {
		_ = handle.closeStream()
	}
	return nil
}

func (r *messageRouter) hasRoute(channel string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.routes[channel]
	return exists
}

func (h *routeHandle) Close() error {
	h.router.mu.Lock()
	if existing, ok := h.router.routes[h.spec.BusChannel]; ok && existing == h {
		delete(h.router.routes, h.spec.BusChannel)
	}
	h.router.mu.Unlock()
	return h.closeStream()
}

func (h *routeHandle) closeStream() error {
	h.once.Do(func() {
		if h.stream != nil {
			h.stream.Close()
		}
	})
	return nil
}
