// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
	"context"
	"crypto/tls"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/plank/pkg/middleware"
	"github.com/pb33f/ranch/plank/pkg/routing"
	"github.com/pb33f/ranch/store"
	"github.com/pb33f/ranch/transport/fabric"
	"log/slog"

	"github.com/pb33f/ranch/service"
	"github.com/pb33f/ranch/stompserver"
	"golang.org/x/net/http2"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// DefaultDebugProfilerPort is the fallback pprof listener port when debug profiling is enabled.
const DefaultDebugProfilerPort = 6062

// PlatformServerConfig holds all the core configuration needed for the functionality of Plank
type PlatformServerConfig struct {
	RootDir                        string              `json:"root_dir"`                       // root directory the server should base itself on
	StaticDir                      []string            `json:"static_dir"`                     // static content folders that HTTP server should serve
	SpaConfig                      *SpaConfig          `json:"spa_config"`                     // single page application configuration
	Host                           string              `json:"host"`                           // hostname for the server
	Port                           int                 `json:"port"`                           // port for the server
	Logger                         *slog.Logger        `json:"-"`                              // logger instance
	FabricConfig                   *FabricBrokerConfig `json:"fabric_config"`                  // Fabric (websocket) configuration
	TLSCertConfig                  *TLSCertConfig      `json:"tls_config"`                     // TLS certificate configuration
	Debug                          bool                `json:"debug"`                          // enable debug logging
	DebugProfilerPort              int                 `json:"debug_profiler_port"`            // pprof port when debug is enabled; 0 asks the OS for a free port
	NoBanner                       bool                `json:"no_banner"`                      // start server without displaying the banner
	ShutdownTimeout                time.Duration       `json:"shutdown_timeout_in_minutes"`    // graceful server shutdown timeout in minutes
	RestBridgeTimeout              time.Duration       `json:"rest_bridge_timeout_in_minutes"` // rest bridge timeout in minutes
	DefaultNotFoundHandler         http.Handler        `json:"-"`                              // fallback handler for unmatched paths
	DefaultMethodNotAllowedHandler http.Handler        `json:"-"`                              // fallback handler for paths matched with the wrong method
	RouteErrorPolicies             []*RouteErrorPolicy `json:"-"`                              // longest-prefix route error overrides
	SocketCreationFunc             http.HandlerFunc    `json:"-"`                              // override default websocket creation code.
}

// TLSCertConfig wraps around key information for TLS configuration
type TLSCertConfig struct {
	CertFile                  string `json:"cert_file"`                   // path to certificate file
	KeyFile                   string `json:"key_file"`                    // path to private key file
	SkipCertificateValidation bool   `json:"skip_certificate_validation"` // whether to skip certificate validation (useful for self-signed cert)
}

// FabricBrokerConfig defines the endpoint for WebSocket as well as detailed endpoint configuration
type FabricBrokerConfig struct {
	FabricEndpoint string                 `json:"fabric_endpoint"` // URI to WebSocket endpoint
	UseTCP         bool                   `json:"use_tcp"`         // Use TCP instead of WebSocket
	TCPPort        int                    `json:"tcp_port"`        // TCP port to use if UseTCP is true
	EndpointConfig *fabric.EndpointConfig `json:"endpoint_config"` // STOMP configuration
}

// PlatformServer exposes public API methods that control the behavior of the Plank instance.
type PlatformServer interface {
	StartServer(ctx context.Context, syschan chan os.Signal) error                  // start server
	StopServer()                                                                    // stop server
	Ready() <-chan struct{}                                                         // closed when configured listeners are ready
	GetRouter() *routing.Router                                                     // get router instance
	RegisterService(svc service.FabricService, svcChannel string) error             // register a new service at given channel
	UnregisterService(svcChannel string) error                                      // unregister a service and close its fabric route
	Bus() bus.EventBus                                                              // get event bus
	Lifecycle() service.ServiceLifecycleManager                                     // get service lifecycle manager
	StoreManager() store.Manager                                                    // get store manager
	Fabric() fabric.Endpoint                                                        // get fabric endpoint
	SetHttpChannelBridge(bridgeConfig *service.RESTBridgeConfig)                    // set up a REST bridge for a service
	SetStaticRoute(prefix, fullpath string, middlewareFn ...routing.MiddlewareFunc) // set up a static content route
	SetHttpPathPrefixChannelBridge(bridgeConfig *service.RESTBridgeConfig)          // set up a REST bridge for a path prefix for a service.
	CustomizeTLSConfig(tls *tls.Config) error                                       // used to replace default tls.Config for HTTP server with a custom config
	GetRestBridgeSubRoute(uri, method string) (*routing.Route, error)               // get route that maps to the provided uri and method
	GetMiddlewareManager() middleware.MiddlewareManager                             // get middleware manager
	GetFabricConnectionListener() stompserver.RawConnectionListener
	GetStompServer() stompserver.StompServer // get STOMP server instance for connection management
}

// platformServer is the main struct that holds all components together including servers, various managers etc.
type platformServer struct {
	HttpServer         *http.Server                      // Http server instance
	Http2Server        *http2.Server                     // Http server instance
	SyscallChan        chan os.Signal                    // syscall channel to receive SIGINT, SIGKILL events
	eventbus           bus.EventBus                      // event bus pointer
	storeManager       store.Manager                     // store manager
	registry           service.ServiceRegistry           // service registry
	lifecycle          service.ServiceLifecycleManager   // service lifecycle manager
	serverConfig       *PlatformServerConfig             // server config instance
	middlewareManager  middleware.MiddlewareManager      // middleware maanger instance
	router             *routing.Router                   // router instance
	endpointHandlerMap map[string]http.HandlerFunc       // internal map to store rest endpoint -handler mappings
	bridges            *RestBridgeRegistry               // REST bridge route and response registry
	fabricConn         stompserver.RawConnectionListener // WebSocket listener instance
	fabricEndpoint     fabric.Endpoint                   // fabric endpoint instance
	pendingRoutes      []fabric.RouteSpec                // routes registered before fabric starts
	routeHandles       map[string]fabric.RouteHandle     // active routes keyed by service channel
	routeMu            sync.Mutex                        // route bookkeeping lock
	readyCh            chan struct{}                     // server readiness signal
	readyOnce          sync.Once                         // closes readyCh exactly once
	profilerListener   net.Listener                      // debug pprof listener
	profilerServer     *http.Server                      // debug pprof server
	ServerAvailability *ServerAvailability               // server availability (not much used other than for internal monitoring for now)
	lock               sync.Mutex                        // lock
}

// ServerAvailability contains boolean fields to indicate what components of the system are available or not
type ServerAvailability struct {
	Http   bool // Http server availability
	Fabric bool // stomp broker availability
}

// RouteErrorPolicy configures prefix-specific handling for router-generated errors.
type RouteErrorPolicy struct {
	PathPrefix              string       `json:"path_prefix"`
	NotFoundHandler         http.Handler `json:"-"`
	MethodNotAllowedHandler http.Handler `json:"-"`
}
