package server

import (
    "fmt"
    "github.com/gorilla/mux"
    "github.com/pb33f/ranch/bus"
    "github.com/pb33f/ranch/model"
    "github.com/pb33f/ranch/plank/pkg/middleware"
    "github.com/pb33f/ranch/plank/utils"
    "github.com/pb33f/ranch/service"
    "github.com/pb33f/ranch/stompserver"
    "net/http"
    _ "net/http/pprof"
    "path/filepath"
    "reflect"
    "runtime"
    "time"
)

func (ps *platformServer) GetRouter() *mux.Router {
    return ps.router
}

// initialize sets up basic configurations according to the serverConfig object such as setting output writer,
// log formatter, creating a router instance, and setting up an HttpServer instance.
func (ps *platformServer) initialize() {
    var err error

    // initialize core components
    var serviceRegistryInstance = service.GetServiceRegistry()
    var svcLifecycleManager = service.GetServiceLifecycleManager()

    // create essential bus channels
    ps.eventbus.GetChannelManager().CreateChannel(RANCH_SERVER_ONLINE_CHANNEL)

    // initialize HTTP endpoint handlers map
    ps.endpointHandlerMap = map[string]http.HandlerFunc{}
    ps.serviceChanToBridgeEndpoints = make(map[string][]string, 0)

    // initialize log output streams
    //if err = ps.serverConfig.LogConfig.PrepareLogFiles(); err != nil {
    //	panic(err)
    //}

    // alias outputLogFp as ps.out for platform log outputs
    //ps.out = ps.serverConfig.LogConfig.GetPlatformLogFilePointer()

    // set logrus out writer options and assign output stream to ps.out
    //formatter := utils.CreateTextFormatterFromFormatOptions(ps.serverConfig.LogConfig.FormatOptions)
    //	utils.Log.SetFormatter(formatter)
    //	utils.Log.SetOutput(ps.out)

    // if debug flag is provided enable extra logging. also, enable profiling at port 6060
    if ps.serverConfig.Debug {
        go func() {
            runtime.SetBlockProfileRate(1) // capture traces of all possible contended mutex holders
            profilerRouter := mux.NewRouter()
            profilerRouter.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)
            if err := http.ListenAndServe(":6060", profilerRouter); err != nil {
                panic(err)
            }
        }()
        ps.serverConfig.Logger.Debug("Debug logging and profiling enabled. Available types of profiles at http://localhost:6060/debug/pprof")
    }

    // set a new route handler
    ps.router = mux.NewRouter().Schemes("http", "https").Subrouter()

    // register a reserved path /health for use with container orchestration layer like k8s
    //ps.endpointHandlerMap["/health"] = func(w http.ResponseWriter, r *http.Request) {
    //	_, _ = w.Write([]byte("OK"))
    //}
    //ps.router.Path("/health").Name("/health").Handler(
    //	middleware.CacheControlMiddleware([]string{"/health"}, middleware.NewCacheControlDirective().NoStore())(ps.endpointHandlerMap["/health"]))

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

        fabricSvc, _ := serviceRegistryInstance.GetService(request.ServiceChannel)
        svcReadyStore := ps.eventbus.GetStoreManager().GetStore(service.ServiceReadyStore)
        hooks := svcLifecycleManager.GetOnReadyCapableService(request.ServiceChannel)

        if request.Override {
            // clear old bridges affected by this override. there's a suboptimal workaround for mux.Router not
            // supporting a way to dynamically remove routers slice. see clearHttpChannelBridgesForService for details
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
        channelManager.CreateChannel(bus.STOMP_SESSION_NOTIFY_CHANNEL)
    }

    // configure Fabric
    ps.configureFabric()

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
            ps.serverConfig.SocketCreationFunc) // TODO: consider tightening access by allowing configuring allowedOrigins
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

    // TODO: error if the base uri conflicts with another URI registered before
    for _, asset := range ps.serverConfig.SpaConfig.StaticAssets {
        folderPath, uri := utils.DeriveStaticURIFromPath(asset)
        ps.SetStaticRoute(
            utils.SanitizeUrl(uri, false),
            folderPath,
            ps.serverConfig.SpaConfig.CacheControlMiddleware())
    }

    // TODO: consider handling handlers of conflicting keys
    endpointHandlerMapKey := ps.serverConfig.SpaConfig.BaseUri + "*"
    ps.endpointHandlerMap[endpointHandlerMapKey] = func(w http.ResponseWriter, r *http.Request) { // '*' at the end of BaseUri is to indicate it is a prefix route handler
        resource := "index.html"

        // if the URI contains an extension we treat it as access to static resources
        if len(filepath.Ext(r.URL.Path)) > 0 {
            resource = filepath.Clean(r.URL.Path)
        }
        http.ServeFile(w, r, filepath.Join(ps.serverConfig.SpaConfig.RootFolder, resource))
    }

    spaConfigCacheControlMiddleware := ps.serverConfig.SpaConfig.CacheControlMiddleware()
    ps.router.
        PathPrefix(ps.serverConfig.SpaConfig.BaseUri).
        Name(endpointHandlerMapKey).
        Handler(spaConfigCacheControlMiddleware(ps.endpointHandlerMap[endpointHandlerMapKey]))
}
