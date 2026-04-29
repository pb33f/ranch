package server

import (
	"errors"
	"fmt"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/plank/pkg/middleware"
	"github.com/pb33f/ranch/plank/pkg/routing"
	"github.com/pb33f/ranch/plank/utils"
	"github.com/pb33f/ranch/service"
	"github.com/pb33f/ranch/stompserver"
	"github.com/pb33f/ranch/transport/fabric"
	"net"
	"net/http"
	_ "net/http/pprof" // #nosec G108 -- pprof is only started when debug mode enables the profiler listener.
	"path/filepath"
	"reflect"
	"runtime"
	"time"
)

func (ps *platformServer) GetRouter() *routing.Router {
	ps.lock.Lock()
	defer ps.lock.Unlock()
	return ps.router
}

// initialize sets up basic configurations according to the serverConfig object such as setting output writer,
// log formatter, creating a router instance, and setting up an HttpServer instance.
func (ps *platformServer) initialize() {
	var err error

	// create essential bus channels
	ps.eventbus.GetChannelManager().CreateChannel(RANCH_SERVER_ONLINE_CHANNEL)

	// initialize HTTP endpoint handlers map
	ps.endpointHandlerMap = map[string]http.HandlerFunc{}

	// if debug flag is provided enable extra logging. also, enable profiling.
	if ps.serverConfig.Debug {
		ps.startDebugProfiler()
	}

	// set a new route handler
	ps.router = routing.NewRouter()
	ps.configureRouterErrorHandlers(ps.router)
	ps.bridges = NewRestBridgeRegistry(
		ps.router,
		&ps.endpointHandlerMap,
		ps.eventbus,
		ps.serverConfig.Logger,
		ps.serverConfig.RestBridgeTimeout,
		ps.buildEndpointHandler)

	// register static paths
	for _, dir := range ps.serverConfig.StaticDir {
		p, uri := utils.DeriveStaticURIFromPath(dir)
		ps.serverConfig.Logger.Info("Serving static path", "path", p, "uri", uri)
		ps.SetStaticRoute(uri, p)
	}

	// create an Http server instance
	ps.HttpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", ps.serverConfig.Port),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	if err = ps.registry.RegisterService(service.NewRestService(), service.RestServiceChannel); err != nil {
		ps.serverConfig.Logger.Error(err.Error())
	}

	// set up a listener to receive REST bridge configs for services and set them up according to their specs
	lcmChanHandler, err := ps.eventbus.ListenStreamForDestination(service.LifecycleManagerChannelName, ps.eventbus.GetId())
	if err != nil {
		ps.serverConfig.Logger.Error(err.Error())
	}

	lcmChanHandler.Handle(func(message *model.Message) {
		request, ok := message.Payload.(*service.SetupRESTBridgeRequest)
		if !ok {
			ps.serverConfig.Logger.Error("failed to set up REST bridge ")
		}

		fabricSvc, _ := ps.registry.GetService(request.ServiceChannel)
		svcReadyStore := ps.storeManager.GetStore(service.ServiceReadyStore)
		hooks := ps.lifecycle.GetOnReadyCapableService(request.ServiceChannel)

		if request.Override {
			// clear old bridges affected by this override.
			newRouter := ps.clearHttpChannelBridgesForService(request.ServiceChannel)
			ps.loadGlobalHttpHandler(newRouter)
		}

		for _, config := range request.Config {
			ps.SetHttpChannelBridge(config)
		}

		// REST bridge setup done. now wait for service to be ready
		if val, found := svcReadyStore.Get(request.ServiceChannel); !found || !val.(bool) {
			if hooks != nil {
				readyChan := hooks.OnServiceReady()
				svcReadyStore.Put(request.ServiceChannel, <-readyChan, service.ServiceInitStateChange)
				close(readyChan)
			}
			ps.serverConfig.Logger.Info("[ranch] service initialized successfully", "name", reflect.TypeOf(fabricSvc).String())
		}

	}, func(err error) {
		ps.serverConfig.Logger.Error(err.Error())
	})

	// instantiate a new middleware manager
	ps.middlewareManager = middleware.NewMiddlewareManager(&ps.endpointHandlerMap, ps.router, ps.serverConfig.Logger)

	// create an internal bus channel to notify significant changes in sessions such as disconnect
	if ps.serverConfig.FabricConfig != nil {
		channelManager := ps.eventbus.GetChannelManager()
		channelManager.CreateChannel(fabric.STOMP_SESSION_NOTIFY_CHANNEL)
	}

	// configure Fabric
	ps.configureFabric()

}

func (ps *platformServer) startDebugProfiler() {
	runtime.SetBlockProfileRate(1) // capture traces of all possible contended mutex holders

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", ps.serverConfig.DebugProfilerPort))
	if err != nil {
		ps.serverConfig.Logger.Error("debug profiler failed to start", "err", err)
		return
	}

	ps.profilerListener = ln
	ps.profilerServer = &http.Server{
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := ps.profilerServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ps.serverConfig.Logger.Error("debug profiler stopped unexpectedly", "err", err)
		}
	}()

	ps.serverConfig.Logger.Debug(
		"Debug logging and profiling enabled. Available types of profiles",
		"url", fmt.Sprintf("http://%s/debug/pprof", ln.Addr().String()))
}

func (ps *platformServer) configureFabric() {
	if ps.serverConfig.FabricConfig == nil {
		return
	}

	var err error
	if ps.serverConfig.FabricConfig.UseTCP {
		ps.fabricConn, err = stompserver.NewTcpConnectionListener(fmt.Sprintf(":%d", ps.serverConfig.FabricConfig.TCPPort))
	} else {
		ps.fabricConn, err = stompserver.NewWebSocketConnectionFromExistingHttpServer(
			ps.HttpServer,
			ps.router,
			ps.serverConfig.FabricConfig.FabricEndpoint,
			nil, ps.serverConfig.Logger, ps.serverConfig.Debug,
			ps.serverConfig.SocketCreationFunc)
	}

	// if creation of listener fails, crash and burn
	if err != nil {
		panic(err)
	}
}

func (ps *platformServer) configureSPA() {
	if ps.serverConfig.SpaConfig == nil {
		return
	}

	for _, asset := range ps.serverConfig.SpaConfig.StaticAssets {
		folderPath, uri := utils.DeriveStaticURIFromPath(asset)
		ps.SetStaticRoute(
			utils.SanitizeUrl(uri, false),
			folderPath,
			ps.serverConfig.SpaConfig.CacheControlMiddleware())
	}

	spaConfigCacheControlMiddleware := ps.serverConfig.SpaConfig.CacheControlMiddleware()
	endpointHandlerMapKey := ps.serverConfig.SpaConfig.BaseUri + "*"
	ps.endpointHandlerMap[endpointHandlerMapKey] = spaConfigCacheControlMiddleware(http.HandlerFunc(ps.serveSPAResource)).ServeHTTP
}

func (ps *platformServer) serveSPAResource(w http.ResponseWriter, r *http.Request) {
	resource := "index.html"

	// if the URI contains an extension we treat it as access to static resources
	if len(filepath.Ext(r.URL.Path)) > 0 {
		resource = filepath.Clean(r.URL.Path)
	}
	http.ServeFile(w, r, filepath.Join(ps.serverConfig.SpaConfig.RootFolder, resource))
}
