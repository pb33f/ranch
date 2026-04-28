// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pb33f/ranch/stompserver"
	"github.com/pb33f/ranch/transport/fabric"
	"github.com/spf13/pflag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/handlers"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/plank/pkg/middleware"
	"github.com/pb33f/ranch/plank/pkg/routing"
	"github.com/pb33f/ranch/service"
	"github.com/pb33f/ranch/store"
)

const RANCH_SERVER_ONLINE_CHANNEL = bus.RANCH_INTERNAL_CHANNEL_PREFIX + "ranch-online-notify"
const AllMethodsWildcard = "*" // every method, open the gates!

type httpBridgeMatchMode int

const (
	httpBridgeExact httpBridgeMatchMode = iota
	httpBridgePrefix
)

// NewPlatformServer configures and returns a new platformServer instance
func NewPlatformServer(config *PlatformServerConfig) PlatformServer {

	// configure a default logger if none is provided
	if config.Logger == nil {
		config.Logger = defaultLogger()
	}

	ps := new(platformServer)
	sanitizeConfigRootPath(config)
	ps.serverConfig = config
	ps.ServerAvailability = &ServerAvailability{}
	ps.messageBridgeMap = make(map[string]*MessageBridge)
	ps.routeHandles = make(map[string]fabric.RouteHandle)
	ps.readyCh = make(chan struct{})
	ps.eventbus = bus.NewEventBusWithLogger(config.Logger)
	ps.storeManager = store.NewManagerWithLogger(ps.eventbus, config.Logger)
	ps.registry = service.NewServiceRegistryWithLogger(ps.eventbus, ps.storeManager, config.Logger)
	ps.lifecycle = service.NewServiceLifecycleManager(ps.registry)
	ps.initialize()
	return ps
}

func defaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// NewPlatformServerFromConfig returns a new instance of PlatformServer based on the config JSON file provided as configPath
func NewPlatformServerFromConfig(configPath string) (PlatformServer, error) {
	var config PlatformServerConfig

	// no config no server
	configBytes, err := os.ReadFile(configPath) // #nosec G304 -- config path comes from the caller by design.
	if err != nil {
		return nil, err
	}

	// malformed config no server as well
	if err = json.Unmarshal(configBytes, &config); err != nil {
		return nil, err
	}
	applyPlatformServerConfigJSONDefaults(&config, configBytes)
	if config.Logger == nil {
		config.Logger = defaultLogger()
	}

	ps := new(platformServer)
	ps.eventbus = bus.NewEventBusWithLogger(config.Logger)
	ps.storeManager = store.NewManagerWithLogger(ps.eventbus, config.Logger)
	ps.registry = service.NewServiceRegistryWithLogger(ps.eventbus, ps.storeManager, config.Logger)
	ps.lifecycle = service.NewServiceLifecycleManager(ps.registry)
	sanitizeConfigRootPath(&config)

	// handle invalid duration by setting it to the default value of 5 minutes
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5
	}

	// handle invalid duration by setting it to the default value of 1 minute
	if config.RestBridgeTimeout <= 0 {
		config.RestBridgeTimeout = 1
	}

	// the raw value from the config.json needs to be multiplied by time.Minute otherwise it's interpreted as nanosecond
	config.ShutdownTimeout *= time.Minute

	// the raw value from the config.json needs to be multiplied by time.Minute otherwise it's interpreted as nanosecond
	config.RestBridgeTimeout *= time.Minute

	if config.TLSCertConfig != nil {
		if !path.IsAbs(config.TLSCertConfig.CertFile) {
			config.TLSCertConfig.CertFile = path.Clean(path.Join(config.RootDir, config.TLSCertConfig.CertFile))
		}

		if !path.IsAbs(config.TLSCertConfig.KeyFile) {
			config.TLSCertConfig.KeyFile = path.Clean(path.Join(config.RootDir, config.TLSCertConfig.KeyFile))
		}
	}

	ps.serverConfig = &config
	ps.ServerAvailability = &ServerAvailability{}
	ps.messageBridgeMap = make(map[string]*MessageBridge)
	ps.routeHandles = make(map[string]fabric.RouteHandle)
	ps.readyCh = make(chan struct{})
	ps.initialize()
	return ps, nil
}

// CreateServerConfig creates a new instance of PlatformServerConfig and returns the pointer to it.
func CreateServerConfig() (*PlatformServerConfig, error) {
	factory := &serverConfigFactory{}
	factory.configureFlags(pflag.CommandLine)
	return generatePlatformServerConfig(factory)
}

// GetConnectionListener
func (ps *platformServer) GetFabricConnectionListener() stompserver.RawConnectionListener {
	return ps.fabricConn
}

func (ps *platformServer) GetStompServer() stompserver.StompServer {
	if ps.fabricEndpoint != nil {
		return ps.fabricEndpoint.StompServer()
	}
	return nil
}

func (ps *platformServer) Ready() <-chan struct{} {
	return ps.readyCh
}

func (ps *platformServer) Bus() bus.EventBus {
	return ps.eventbus
}

func (ps *platformServer) Lifecycle() service.ServiceLifecycleManager {
	return ps.lifecycle
}

func (ps *platformServer) StoreManager() store.Manager {
	return ps.storeManager
}

func (ps *platformServer) Fabric() fabric.Endpoint {
	return ps.fabricEndpoint
}

// StartServer starts listening on the host and port as specified by ServerConfig.
func (ps *platformServer) StartServer(ctx context.Context, syschan chan os.Signal) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ps.SyscallChan = syschan
	if ps.SyscallChan != nil {
		signal.Notify(ps.SyscallChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(ps.SyscallChan)
	}

	// ensure port is available
	ps.checkPortAvailability()

	// finalize handler by setting out writer
	ps.loadGlobalHttpHandler(ps.router)

	// configure SPA
	// NOTE: the reason SPA app route is configured during server startup is that if the base uri is `/` for SPA
	// then all other routes registered after SPA route will be masked away.
	ps.configureSPA()

	httpListener, err := net.Listen("tcp", ps.HttpServer.Addr)
	if err != nil {
		return wrapError(errServerInit, err)
	}

	if ps.serverConfig.TLSCertConfig != nil {
		if _, err := tls.LoadX509KeyPair(ps.serverConfig.TLSCertConfig.CertFile, ps.serverConfig.TLSCertConfig.KeyFile); err != nil {
			_ = httpListener.Close()
			ps.ServerAvailability.Http = false
			return wrapError(errServerInit, err)
		}
	}

	serveDone := make(chan error, 1)
	closeHTTP := func() {
		ps.ServerAvailability.Http = false
		_ = ps.HttpServer.Close()
	}
	ps.ServerAvailability.Http = true
	go func() {
		if ps.serverConfig.TLSCertConfig != nil {
			ps.serverConfig.Logger.Info("[ranch] yee-haw! starting up the ranch's HTTPS server at %s:%d with TLS", "host", ps.serverConfig.Host, "port", ps.serverConfig.Port)
			if err := ps.HttpServer.ServeTLS(httpListener, ps.serverConfig.TLSCertConfig.CertFile, ps.serverConfig.TLSCertConfig.KeyFile); err != nil {
				if !errors.Is(err, http.ErrServerClosed) {
					serveDone <- wrapError(errServerInit, err)
					return
				}
			}
		} else {
			ps.serverConfig.Logger.Info("[ranch] yee-haw! starting up the ranch's HTTP server", "host", ps.serverConfig.Host, "port", ps.serverConfig.Port)
			if err := ps.HttpServer.Serve(httpListener); err != nil {
				if !errors.Is(err, http.ErrServerClosed) {
					serveDone <- wrapError(errServerInit, err)
					return
				}
			}
		}
		serveDone <- nil
	}()

	if ps.serverConfig.FabricConfig != nil {
		fabricPort := ps.serverConfig.Port
		fabricEndpointPath := ps.serverConfig.FabricConfig.FabricEndpoint
		if ps.serverConfig.FabricConfig.UseTCP {
			// if using TCP adjust port accordingly and drop endpoint
			fabricPort = ps.serverConfig.FabricConfig.TCPPort
			fabricEndpointPath = ""
		}
		brokerLocation := fmt.Sprintf("%s:%d%s", ps.serverConfig.Host, fabricPort, fabricEndpointPath)
		ps.serverConfig.Logger.Info("[ranch] hot-dang! starting up the ranch's STOMP message broker", "location", brokerLocation)

		endpointConfig := *ps.serverConfig.FabricConfig.EndpointConfig
		endpointConfig.Logger = ps.serverConfig.Logger
		ep, err := fabric.New(ps.eventbus, ps.fabricConn, endpointConfig)
		if err != nil {
			closeHTTP()
			return wrapError(errServerInit, err)
		}
		ps.fabricEndpoint = ep
		if err := ps.fabricEndpoint.Start(ctx); err != nil {
			closeHTTP()
			return wrapError(errServerInit, err)
		}
		if err := ps.drainPendingRoutes(); err != nil {
			closeHTTP()
			_ = ps.fabricEndpoint.Stop()
			return wrapError(errServerInit, err)
		}
		ps.ServerAvailability.Fabric = true
	}

	// spawn another goroutine to respond to syscall to shut down servers and terminate the main thread
	if ps.SyscallChan != nil {
		go func() {
			select {
			case <-ps.SyscallChan:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	select {
	case err := <-serveDone:
		if err != nil {
			ps.ServerAvailability.Http = false
			ps.serverConfig.Logger.Error(err.Error())
		}
		return err
	default:
	}

	ps.readyOnce.Do(func() {
		close(ps.readyCh)
	})
	_ = ps.eventbus.SendResponseMessage(RANCH_SERVER_ONLINE_CHANNEL, true, nil)

	select {
	case err := <-serveDone:
		if err != nil {
			ps.ServerAvailability.Http = false
			ps.serverConfig.Logger.Error(err.Error())
		}
		return err
	case <-ctx.Done():
		// notify subscribers that the server is shutting down
		_ = ps.eventbus.SendResponseMessage(RANCH_SERVER_ONLINE_CHANNEL, false, nil)
		ps.StopServer()
		return nil
	}
}

// StopServer attempts to gracefully stop the HTTP and STOMP server if running
func (ps *platformServer) StopServer() {
	ps.serverConfig.Logger.Info("[ranch] server shutting down... see you around soon, partner!")
	ps.ServerAvailability.Http = false

	baseCtx := context.Background()
	shutdownCtx, cancel := context.WithTimeout(baseCtx, ps.serverConfig.ShutdownTimeout)

	go func() {
		<-shutdownCtx.Done()
		if errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
			ps.serverConfig.Logger.Error(
				"server failed to gracefully shut down after timeout", "timeout",
				ps.serverConfig.ShutdownTimeout.String())
		}
	}()
	defer cancel()

	// call all registered services' OnServerShutdown() hook
	wg := sync.WaitGroup{}
	for _, svcChannel := range ps.registry.GetAllServiceChannels() {
		hooks := ps.lifecycle.GetOnServerShutdownService(svcChannel)
		if hooks != nil {
			ps.serverConfig.Logger.Info("teardown in progress for service", "channel", svcChannel)
			wg.Add(1)
			go func(cName string, h service.OnServerShutdownEnabled) {
				h.OnServerShutdown()
				ps.serverConfig.Logger.Info("teardown completed for service", "channel", cName)
				wg.Done()

			}(svcChannel, hooks)
		}
	}

	// start graceful shutdown
	err := ps.HttpServer.Shutdown(shutdownCtx)
	if err != nil {
		ps.serverConfig.Logger.Error(err.Error())
	}

	if ps.fabricEndpoint != nil {
		if err = ps.fabricEndpoint.Stop(); err != nil {
			ps.serverConfig.Logger.Error(err.Error())
		}
		ps.fabricEndpoint = nil
		ps.ServerAvailability.Fabric = false
	}

	if ps.profilerServer != nil {
		if err = ps.profilerServer.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ps.serverConfig.Logger.Error(err.Error())
		}
	}

	// wait for all teardown jobs to be done. if shutdown deadline arrives earlier
	// the main thread will be terminated forcefully
	wg.Wait()
}

// SetStaticRoute adds a route where static resources will be served
func (ps *platformServer) SetStaticRoute(prefix, fullpath string, middlewareFn ...routing.MiddlewareFunc) {
	ndir := NoDirFileSystem{http.Dir(fullpath)}
	endpointHandlerMapKey := prefix + "*"
	compositeHandler := http.StripPrefix(prefix, middleware.BasicSecurityHeaderMiddleware()(http.FileServer(ndir)))

	for _, mw := range middlewareFn {
		compositeHandler = mw(compositeHandler)
	}

	ps.endpointHandlerMap[endpointHandlerMapKey] = compositeHandler.(http.HandlerFunc)
	ps.router.PathPrefix(prefix + "/").Name(endpointHandlerMapKey).Handler(ps.endpointHandlerMap[endpointHandlerMapKey])
}

// RegisterService registers a Fabric service with Bifrost
func (ps *platformServer) RegisterService(svc service.FabricService, svcChannel string) error {
	err := ps.registry.RegisterService(svc, svcChannel)
	svcType := reflect.TypeOf(svc)

	if err == nil {
		ps.serverConfig.Logger.Info("[ranch] Service registered", "name", svcType.String(), "channel", svcChannel)
		var hooks service.OnServiceReadyEnabled
		if hooks = ps.lifecycle.GetOnReadyCapableService(svcChannel); hooks == nil {
			// if service has no lifecycle hooks mark the channel as ready straight up
			store := ps.storeManager.GetStore(service.ServiceReadyStore)
			store.Put(svcChannel, true, service.ServiceInitStateChange)
			ps.serverConfig.Logger.Info("[ranch] service initialized successfully", "name", svcType.String())
		}
		if rerr := ps.registerOrPendRoute(svcChannel); rerr != nil {
			ps.serverConfig.Logger.Error("[ranch] route registration failed", "channel", svcChannel, "err", rerr)
			_ = ps.registry.UnregisterService(svcChannel)
			return rerr
		}
	}
	return err
}

func (ps *platformServer) UnregisterService(svcChannel string) error {
	if err := ps.registry.UnregisterService(svcChannel); err != nil {
		return err
	}

	ps.routeMu.Lock()
	defer ps.routeMu.Unlock()

	if h, ok := ps.routeHandles[svcChannel]; ok {
		delete(ps.routeHandles, svcChannel)
		return h.Close()
	}

	for i, spec := range ps.pendingRoutes {
		if spec.BusChannel == svcChannel {
			ps.pendingRoutes = append(ps.pendingRoutes[:i], ps.pendingRoutes[i+1:]...)
			break
		}
	}
	return nil
}

func (ps *platformServer) registerOrPendRoute(serviceChannel string) error {
	if ps.serverConfig.FabricConfig == nil {
		return nil
	}

	spec := fabric.RouteSpec{
		BusChannel:    serviceChannel,
		DefaultDest:   fabric.TopicDestination(ps.serverConfig.FabricConfig.EndpointConfig, serviceChannel),
		AllowOverride: true,
		Direction:     model.ResponseDir,
	}

	ps.routeMu.Lock()
	defer ps.routeMu.Unlock()

	if ps.routeHandles == nil {
		ps.routeHandles = make(map[string]fabric.RouteHandle)
	}
	if ps.fabricEndpoint == nil {
		ps.pendingRoutes = append(ps.pendingRoutes, spec)
		return nil
	}

	handle, err := ps.fabricEndpoint.Router().RegisterRoute(spec)
	if err != nil {
		return err
	}
	ps.routeHandles[serviceChannel] = handle
	return nil
}

func (ps *platformServer) drainPendingRoutes() error {
	ps.routeMu.Lock()
	defer ps.routeMu.Unlock()

	if ps.routeHandles == nil {
		ps.routeHandles = make(map[string]fabric.RouteHandle)
	}
	for _, spec := range ps.pendingRoutes {
		handle, err := ps.fabricEndpoint.Router().RegisterRoute(spec)
		if err != nil {
			return fmt.Errorf("register route %q: %w", spec.BusChannel, err)
		}
		ps.routeHandles[spec.BusChannel] = handle
	}
	ps.pendingRoutes = nil
	return nil
}

// SetHttpChannelBridge establishes a conduit between the transport service channel and an HTTP endpoint
// that allows a client to invoke the service via REST.
func (ps *platformServer) SetHttpChannelBridge(bridgeConfig *service.RESTBridgeConfig) {
	ps.setHttpChannelBridge(bridgeConfig, httpBridgeExact)
}

// SetHttpPathPrefixChannelBridge establishes a conduit between the transport service channel and a path prefix
// every request on this prefix will be sent through to the target service, all methods, all sub paths, lock, stock and barrel.
func (ps *platformServer) SetHttpPathPrefixChannelBridge(bridgeConfig *service.RESTBridgeConfig) {
	ps.setHttpChannelBridge(bridgeConfig, httpBridgePrefix)
}

func (ps *platformServer) setHttpChannelBridge(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	endpointHandlerKey := bridgeEndpointHandlerKey(bridgeConfig, mode)

	if _, ok := ps.endpointHandlerMap[endpointHandlerKey]; ok {
		ps.logDuplicateBridge(bridgeConfig, mode)
		return
	}

	if ps.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel] == nil {
		ps.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel] = make([]string, 0)
	}

	ps.ensureMessageBridge(bridgeConfig.ServiceChannel)
	ps.endpointHandlerMap[endpointHandlerKey] = ps.buildEndpointHandler(
		bridgeConfig.ServiceChannel,
		bridgeConfig.FabricRequestBuilder,
		ps.serverConfig.RestBridgeTimeout,
		ps.messageBridgeMap[bridgeConfig.ServiceChannel].payloadChannel)

	ps.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel] = append(
		ps.serviceChanToBridgeEndpoints[bridgeConfig.ServiceChannel], endpointHandlerKey)

	ps.registerHttpBridgeRoute(bridgeConfig, endpointHandlerKey, mode)
	ps.logBridgeRegistration(bridgeConfig, mode)
}

func bridgeEndpointHandlerKey(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) string {
	if mode == httpBridgePrefix {
		return bridgeConfig.Uri + "-" + AllMethodsWildcard
	}
	return bridgeConfig.Uri + "-" + bridgeConfig.Method
}

func (ps *platformServer) ensureMessageBridge(serviceChannel string) {
	if _, exists := ps.messageBridgeMap[serviceChannel]; exists {
		return
	}

	handler, _ := ps.eventbus.ListenStream(serviceChannel)
	ps.messageBridgeMap[serviceChannel] = &MessageBridge{
		ServiceListenStream: handler,
		payloadChannel:      make(chan *model.Message, 100),
	}
	handler.Handle(func(message *model.Message) {
		ps.messageBridgeMap[serviceChannel].payloadChannel <- message
	}, func(err error) {})
}

func (ps *platformServer) registerHttpBridgeRoute(
	bridgeConfig *service.RESTBridgeConfig, endpointHandlerKey string, mode httpBridgeMatchMode) {
	handler := ps.endpointHandlerMap[endpointHandlerKey]
	if mode == httpBridgePrefix {
		ps.router.
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

	ps.router.
		Path(bridgeConfig.Uri).
		Methods(permittedMethods...).
		Name(fmt.Sprintf("%s-%s", bridgeConfig.Uri, bridgeConfig.Method)).
		Handler(handler)
}

func (ps *platformServer) logDuplicateBridge(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) {
	if mode == httpBridgePrefix {
		ps.serverConfig.Logger.Warn("[ranch] path prefix is already being handled. "+
			"Try another prefix or remove it before assigning a new handler", "uri", bridgeConfig.Uri, "method", bridgeConfig.Method)
		return
	}

	ps.serverConfig.Logger.Warn("[ranch] endpoint is already associated with a handler, "+
		"Try another endpoint or remove it before assigning a new handler", "uri", bridgeConfig.Uri, "method", bridgeConfig.Method)
}

func (ps *platformServer) logBridgeRegistration(bridgeConfig *service.RESTBridgeConfig, mode httpBridgeMatchMode) {
	if mode == httpBridgePrefix {
		ps.serverConfig.Logger.Info(
			"[ranch] Service channel is now bridged to a REST path prefix",
			"channel", bridgeConfig.ServiceChannel, "url", bridgeConfig.Uri)
		return
	}
	ps.serverConfig.Logger.Info(
		"[ranch] service channel is bridged to a REST endpoint",
		"channel", bridgeConfig.ServiceChannel, "url", bridgeConfig.Uri, "method", bridgeConfig.Method)
}

// GetMiddlewareManager returns the MiddleManager instance
func (ps *platformServer) GetMiddlewareManager() middleware.MiddlewareManager {
	return ps.middlewareManager
}

func (ps *platformServer) GetRestBridgeSubRoute(uri, method string) (*routing.Route, error) {
	route, err := ps.getSubRoute(fmt.Sprintf("%s-%s", uri, method))
	if route == nil {
		return nil, fmt.Errorf("no route exists at %s (%s) exists", uri, method)
	}
	return route, err
}

// CustomizeTLSConfig is used to create a customized TLS configuration for use with http.Server.
// this function needs to be called before the server starts, otherwise it will error out.
func (c *platformServer) CustomizeTLSConfig(tls *tls.Config) error {
	if c.ServerAvailability.Http || c.ServerAvailability.Fabric {
		return fmt.Errorf("TLS configuration can be provided only if the server is not running")
	}
	c.HttpServer.TLSConfig = tls
	return nil
}

// clearHttpChannelBridgesForService removes routes associated with serviceChannel while keeping the rest intact.
func (ps *platformServer) clearHttpChannelBridgesForService(serviceChannel string) *routing.Router {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	lookupMap := make(map[string]bool)
	for _, key := range ps.serviceChanToBridgeEndpoints[serviceChannel] {
		lookupMap[key] = true
	}
	newRouter := ps.router.CloneExcluding(lookupMap)

	// if in override mode delete existing mappings associated with the service
	existingMappings := ps.serviceChanToBridgeEndpoints[serviceChannel]
	ps.serviceChanToBridgeEndpoints[serviceChannel] = make([]string, 0)
	for _, handlerKey := range existingMappings {
		ps.serverConfig.Logger.Info("[ranch] Removing existing service - REST mapping", "key", handlerKey, "channel", serviceChannel)
		delete(ps.endpointHandlerMap, handlerKey)
	}
	return newRouter
}

func (ps *platformServer) getSubRoute(name string) (*routing.Route, error) {
	route := ps.router.Get(name)
	if route == nil {
		return nil, fmt.Errorf("no route exists under name %s", name)
	}
	return route, nil
}

func (ps *platformServer) loadGlobalHttpHandler(h *routing.Router) {
	ps.lock.Lock()
	defer ps.lock.Unlock()
	ps.router = h
	if updater, ok := ps.middlewareManager.(interface{ SetRouter(*routing.Router) }); ok {
		updater.SetRouter(h)
	}
	ps.HttpServer.Handler = handlers.RecoveryHandler()(
		handlers.CompressHandler(
			handlers.ProxyHeaders(ps.router)))
}

func (ps *platformServer) checkPortAvailability() {
	// is the port free?
	_, err := net.Dial("tcp", fmt.Sprintf(":%d", ps.serverConfig.Port))

	// connection should fail otherwise it means there's already a listener on the host+port combination, in which case we stop here
	if err == nil {
		ps.serverConfig.Logger.Error("Server could not start because another process is using the port - try another",
			"host", ps.serverConfig.Host, "port", ps.serverConfig.Port)
	}
}
