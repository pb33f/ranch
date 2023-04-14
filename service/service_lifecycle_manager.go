package service

import (
    "github.com/pb33f/ranch/model"
    "net/http"
)

var svcLifecycleManagerInstance ServiceLifecycleManager

type RequestBuilder func(w http.ResponseWriter, r *http.Request) model.Request

type ServiceLifecycleManager interface {
    //GetServiceHooks(serviceChannelName string) ServiceLifecycleHookEnabled
    GetOnReadyCapableService(serviceChannelName string) OnServiceReadyEnabled
    GetOnServerShutdownService(serviceChannelName string) OnServerShutdownEnabled
    GetRESTBridgeEnabledService(serviceChannelName string) RESTBridgeEnabled
    OverrideRESTBridgeConfig(serviceChannelName string, config []*RESTBridgeConfig) error
}

type ServiceLifecycleHookEnabled interface {
    OnServiceReady() chan bool                // service initialization logic should be implemented here
    OnServerShutdown()                        // teardown logic goes here and will be automatically invoked on graceful server shutdown
    GetRESTBridgeConfig() []*RESTBridgeConfig // service-to-REST endpoint mappings go here
}

type RESTBridgeEnabled interface {
    GetRESTBridgeConfig() []*RESTBridgeConfig // service-to-REST endpoint mappings go here
}

type OnServiceReadyEnabled interface {
    OnServiceReady() chan bool // service initialization logic should be implemented here
}

type OnServerShutdownEnabled interface {
    OnServerShutdown() // teardown logic goes here and will be automatically invoked on graceful server shutdown
}

type SetupRESTBridgeRequest struct {
    ServiceChannel string
    Override       bool
    Config         []*RESTBridgeConfig
}

type RESTBridgeConfig struct {
    ServiceChannel       string         // transport service channel
    Uri                  string         // URI to map the transport service to
    Method               string         // HTTP verb to map the transport service request to URI with
    AllowHead            bool           // whether HEAD calls are allowed for this bridge point
    AllowOptions         bool           // whether OPTIONS calls are allowed for this bridge point
    FabricRequestBuilder RequestBuilder // function to transform HTTP request into a transport request
}

type serviceLifecycleManager struct {
    serviceRegistryRef ServiceRegistry // service registry reference
}

// GetOnReadyCapableService returns a service that implements OnServiceReadyEnabled
func (lm *serviceLifecycleManager) GetOnReadyCapableService(serviceChannelName string) OnServiceReadyEnabled {
    service, err := lm.serviceRegistryRef.GetService(serviceChannelName)
    if err != nil {
        return nil
    }

    if lifecycleHookEnabled, ok := service.(OnServiceReadyEnabled); ok {
        return lifecycleHookEnabled
    }
    return nil
}

// GetOnServerShutdownService returns a service that implements OnServerShutdownEnabled
func (lm *serviceLifecycleManager) GetOnServerShutdownService(serviceChannelName string) OnServerShutdownEnabled {
    service, err := lm.serviceRegistryRef.GetService(serviceChannelName)
    if err != nil {
        return nil
    }

    if lifecycleHookEnabled, ok := service.(OnServerShutdownEnabled); ok {
        return lifecycleHookEnabled
    }
    return nil
}

// GetRESTBridgeEnabledService returns a service that implements OnServerShutdownEnabled
func (lm *serviceLifecycleManager) GetRESTBridgeEnabledService(serviceChannelName string) RESTBridgeEnabled {
    service, err := lm.serviceRegistryRef.GetService(serviceChannelName)
    if err != nil {
        return nil
    }

    if lifecycleHookEnabled, ok := service.(RESTBridgeEnabled); ok {
        return lifecycleHookEnabled
    }
    return nil
}

// OverrideRESTBridgeConfig overrides the REST bridge configuration currently present with the provided new bridge configs
func (lm *serviceLifecycleManager) OverrideRESTBridgeConfig(serviceChannelName string, config []*RESTBridgeConfig) error {
    _, err := lm.serviceRegistryRef.GetService(serviceChannelName)
    if err != nil {
        return err
    }
    reg := lm.serviceRegistryRef.(*serviceRegistry)
    if err = reg.bus.SendResponseMessage(
        LifecycleManagerChannelName,
        &SetupRESTBridgeRequest{ServiceChannel: serviceChannelName, Config: config, Override: true},
        reg.bus.GetId()); err != nil {
        return err
    }
    return nil
}

// GetServiceLifecycleManager returns a singleton instance of ServiceLifecycleManager
func GetServiceLifecycleManager() ServiceLifecycleManager {
    if svcLifecycleManagerInstance == nil {
        svcLifecycleManagerInstance = &serviceLifecycleManager{
            serviceRegistryRef: registry,
        }
    }
    return svcLifecycleManagerInstance
}

// newServiceLifecycleManager returns a new instance of ServiceLifecycleManager
func newServiceLifecycleManager(reg ServiceRegistry) ServiceLifecycleManager {
    return &serviceLifecycleManager{serviceRegistryRef: reg}
}
