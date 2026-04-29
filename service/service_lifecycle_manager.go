package service

import (
	"github.com/pb33f/ranch/model"
	"net/http"
)

// RequestBuilder converts an HTTP request into a Fabric service request.
type RequestBuilder func(w http.ResponseWriter, r *http.Request) model.Request

// ServiceLifecycleManager exposes lifecycle-aware service lookup and REST bridge overrides.
type ServiceLifecycleManager interface {
	GetOnReadyCapableService(serviceChannelName string) OnServiceReadyEnabled
	GetOnServerShutdownService(serviceChannelName string) OnServerShutdownEnabled
	GetRESTBridgeEnabledService(serviceChannelName string) RESTBridgeEnabled
	OverrideRESTBridgeConfig(serviceChannelName string, config []*RESTBridgeConfig) error
}

// ServiceLifecycleHookEnabled is implemented by services with ready, shutdown, and REST bridge hooks.
type ServiceLifecycleHookEnabled interface {
	OnServiceReady() chan bool                // service initialization logic should be implemented here
	OnServerShutdown()                        // teardown logic goes here and will be automatically invoked on graceful server shutdown
	GetRESTBridgeConfig() []*RESTBridgeConfig // service-to-REST endpoint mappings go here
}

// RESTBridgeEnabled is implemented by services that expose REST bridge configuration.
type RESTBridgeEnabled interface {
	GetRESTBridgeConfig() []*RESTBridgeConfig // service-to-REST endpoint mappings go here
}

// OnServiceReadyEnabled is implemented by services that signal initialization completion.
type OnServiceReadyEnabled interface {
	OnServiceReady() chan bool // service initialization logic should be implemented here
}

// OnServerShutdownEnabled is implemented by services that need graceful shutdown callbacks.
type OnServerShutdownEnabled interface {
	OnServerShutdown() // teardown logic goes here and will be automatically invoked on graceful server shutdown
}

// SetupRESTBridgeRequest asks Plank to create or override REST bridge routes for a service.
type SetupRESTBridgeRequest struct {
	ServiceChannel string
	Override       bool
	Config         []*RESTBridgeConfig
}

// RESTBridgeConfig maps one HTTP endpoint to a Fabric service channel.
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

// NewServiceLifecycleManager creates a lifecycle manager backed by a service registry.
func NewServiceLifecycleManager(reg ServiceRegistry) ServiceLifecycleManager {
	return &serviceLifecycleManager{serviceRegistryRef: reg}
}
