// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
    "crypto/tls"
    "github.com/gorilla/mux"
    "github.com/pb33f/ranch/bus"
    "github.com/pb33f/ranch/model"
    "github.com/pb33f/ranch/plank/pkg/middleware"
    "log/slog"

    "github.com/pb33f/ranch/service"
    "github.com/pb33f/ranch/stompserver"
    "golang.org/x/net/http2"
    "io"
    "net/http"
    "os"
    "sync"
    "time"
)

// PlatformServerConfig holds all the core configuration needed for the functionality of Plank
type PlatformServerConfig struct {
    RootDir            string              `json:"root_dir"`                       // root directory the server should base itself on
    StaticDir          []string            `json:"static_dir"`                     // static content folders that HTTP server should serve
    SpaConfig          *SpaConfig          `json:"spa_config"`                     // single page application configuration
    Host               string              `json:"host"`                           // hostname for the server
    Port               int                 `json:"port"`                           // port for the server
    Logger             *slog.Logger        `json:"-"`                              // logger instance
    FabricConfig       *FabricBrokerConfig `json:"fabric_config"`                  // Fabric (websocket) configuration
    TLSCertConfig      *TLSCertConfig      `json:"tls_config"`                     // TLS certificate configuration
    Debug              bool                `json:"debug"`                          // enable debug logging
    NoBanner           bool                `json:"no_banner"`                      // start server without displaying the banner
    ShutdownTimeout    time.Duration       `json:"shutdown_timeout_in_minutes"`    // graceful server shutdown timeout in minutes
    RestBridgeTimeout  time.Duration       `json:"rest_bridge_timeout_in_minutes"` // rest bridge timeout in minutes
    SocketCreationFunc http.HandlerFunc    `json:"-"`                              // override default websocket creation code.
}

// TLSCertConfig wraps around key information for TLS configuration
type TLSCertConfig struct {
    CertFile                  string `json:"cert_file"`                   // path to certificate file
    KeyFile                   string `json:"key_file"`                    // path to private key file
    SkipCertificateValidation bool   `json:"skip_certificate_validation"` // whether to skip certificate validation (useful for self-signed cert)
}

// FabricBrokerConfig defines the endpoint for WebSocket as well as detailed endpoint configuration
type FabricBrokerConfig struct {
    FabricEndpoint string              `json:"fabric_endpoint"` // URI to WebSocket endpoint
    UseTCP         bool                `json:"use_tcp"`         // Use TCP instead of WebSocket
    TCPPort        int                 `json:"tcp_port"`        // TCP port to use if UseTCP is true
    EndpointConfig *bus.EndpointConfig `json:"endpoint_config"` // STOMP configuration
}

// PlatformServer exposes public API methods that control the behavior of the Plank instance.
type PlatformServer interface {
    StartServer(syschan chan os.Signal)                                         // start server
    StopServer()                                                                // stop server
    GetRouter() *mux.Router                                                     // get *mux.Router instance
    RegisterService(svc service.FabricService, svcChannel string) error         // register a new service at given channel
    SetHttpChannelBridge(bridgeConfig *service.RESTBridgeConfig)                // set up a REST bridge for a service
    SetStaticRoute(prefix, fullpath string, middlewareFn ...mux.MiddlewareFunc) // set up a static content route
    SetHttpPathPrefixChannelBridge(bridgeConfig *service.RESTBridgeConfig)      // set up a REST bridge for a path prefix for a service.
    CustomizeTLSConfig(tls *tls.Config) error                                   // used to replace default tls.Config for HTTP server with a custom config
    GetRestBridgeSubRoute(uri, method string) (*mux.Route, error)               // get *mux.Route that maps to the provided uri and method
    GetMiddlewareManager() middleware.MiddlewareManager                         // get middleware manager
    GetFabricConnectionListener() stompserver.RawConnectionListener
}

// platformServer is the main struct that holds all components together including servers, various managers etc.
type platformServer struct {
    HttpServer                   *http.Server                      // Http server instance
    Http2Server                  *http2.Server                     // Http server instance
    SyscallChan                  chan os.Signal                    // syscall channel to receive SIGINT, SIGKILL events
    eventbus                     bus.EventBus                      // event bus pointer
    serverConfig                 *PlatformServerConfig             // server config instance
    middlewareManager            middleware.MiddlewareManager      // middleware maanger instance
    router                       *mux.Router                       // *mux.Router instance
    routerConcurrencyProtection  *int32                            // atomic int32 to protect the main router being concurrently written to
    out                          io.Writer                         // platform log output pointer
    endpointHandlerMap           map[string]http.HandlerFunc       // internal map to store rest endpoint -handler mappings
    serviceChanToBridgeEndpoints map[string][]string               // internal map to store service channel - endpoint handler key mappings
    fabricConn                   stompserver.RawConnectionListener // WebSocket listener instance
    ServerAvailability           *ServerAvailability               // server availability (not much used other than for internal monitoring for now)
    lock                         sync.Mutex                        // lock
    messageBridgeMap             map[string]*MessageBridge
}

// MessageBridge is a conduit used for returning service responses as HTTP responses
type MessageBridge struct {
    ServiceListenStream bus.MessageHandler  // message handler returned by bus.ListenStream responsible for relaying back messages as HTTP responses
    payloadChannel      chan *model.Message // internal golang channel used for passing bus responses/errors across goroutines
}

// ServerAvailability contains boolean fields to indicate what components of the system are available or not
type ServerAvailability struct {
    Http   bool // Http server availability
    Fabric bool // stomp broker availability
}
