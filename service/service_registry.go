// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package service

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"

	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/store"
)

var internalServices = map[string]bool{
	"fabric-rest": true,
}

const (
	// LifecycleManagerChannelName is the internal channel for lifecycle requests.
	LifecycleManagerChannelName = bus.RANCH_INTERNAL_CHANNEL_PREFIX + "service-lifecycle-manager"

	// ServiceReadyStore is the store used for service readiness notifications.
	ServiceReadyStore = "service-ready-notification-store"
	// ServiceInitStateChange is the store state used for service readiness changes.
	ServiceInitStateChange = "service-init-state-change"
)

// ServiceRegistry is the registry for  all local fabric services.
type ServiceRegistry interface {
	// GetAllServiceChannels returns all active Fabric service channels as a slice of strings
	GetAllServiceChannels() []string

	// RegisterService registers a new fabric service and associates it with a given EventBus channel.
	// Only one fabric service can be associated with a given channel.
	// If the fabric service implements the FabricInitializableService interface
	// its Init method will be called during the registration process.
	RegisterService(service FabricService, serviceChannelName string) error

	// UnregisterService unregisters the fabric service associated with the given channel.
	UnregisterService(serviceChannelName string) error

	// SetGlobalRestServiceBaseHost sets the global base host or host:port to be used by the restService
	SetGlobalRestServiceBaseHost(host string)

	// GetService returns the FabricService for the channel name given as the parameter
	GetService(serviceChannelName string) (FabricService, error)
}

type serviceRegistry struct {
	lock             sync.Mutex
	services         map[string]*fabricServiceWrapper
	bus              bus.EventBus
	storeManager     store.Manager
	lifecycleManager ServiceLifecycleManager
	logger           *slog.Logger
}

// NewServiceRegistry creates a service registry using the default logger.
func NewServiceRegistry(eventBus bus.EventBus, storeManager store.Manager) ServiceRegistry {
	return NewServiceRegistryWithLogger(eventBus, storeManager, nil)
}

// NewServiceRegistryWithLogger creates a service registry using logger, or slog.Default when logger is nil.
func NewServiceRegistryWithLogger(eventBus bus.EventBus, storeManager store.Manager, logger *slog.Logger) ServiceRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	registry := &serviceRegistry{
		bus:          eventBus,
		storeManager: storeManager,
		services:     make(map[string]*fabricServiceWrapper),
		logger:       logger,
	}
	registry.lifecycleManager = NewServiceLifecycleManager(registry)
	// create a channel for service lifecycle manager
	_ = eventBus.GetChannelManager().CreateChannel(LifecycleManagerChannelName)

	// create a bus store for delivering service ready notifications
	storeManager.CreateStoreWithType(ServiceReadyStore, reflect.TypeOf(true)).Initialize()

	return registry
}

// GetService returns the FabricService instance registered at the provided service channel name.
// if no service is found at the service channel it returns an error.
func (r *serviceRegistry) GetService(serviceChannelName string) (FabricService, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	if serviceWrapper, ok := r.services[serviceChannelName]; ok {
		return serviceWrapper.service, nil
	}
	return nil, fmt.Errorf("fabric service not found at channel %s", serviceChannelName)
}

func (r *serviceRegistry) SetGlobalRestServiceBaseHost(host string) {
	r.lock.Lock()
	defer r.lock.Unlock()
	if svc, ok := r.services[RestServiceChannel]; ok {
		if rest, ok := svc.service.(*restService); ok {
			rest.setBaseHost(host)
		}
	}
}

// GetAllServiceChannels returns the list of service channels that are registered with the registry
func (r *serviceRegistry) GetAllServiceChannels() []string {
	r.lock.Lock()
	defer r.lock.Unlock()
	services := make([]string, 0)
	for chanName := range r.services {
		// do not return internal services like fabric-rest
		if isInternal, found := internalServices[chanName]; !isInternal || !found {
			services = append(services, chanName)
		}
	}
	return services
}

func (r *serviceRegistry) RegisterService(service FabricService, serviceChannelName string) error {
	r.lock.Lock()

	if service == nil {
		r.lock.Unlock()
		return fmt.Errorf("unable to register service: nil service")
	}

	if _, ok := r.services[serviceChannelName]; ok {
		r.lock.Unlock()
		return fmt.Errorf("unable to register service: service channel name is already used: %s", serviceChannelName)
	}

	sw := newServiceWrapper(r.bus, service, serviceChannelName, r.logger)
	err := sw.init()
	if err != nil {
		r.lock.Unlock()
		return err
	}

	r.services[serviceChannelName] = sw

	// if the service is an internal service like fabric-rest don't bother setting up lifecycle hooks
	if internalServices[serviceChannelName] {
		r.lock.Unlock()
		return nil
	}
	r.lock.Unlock()

	// see if the service implements ServiceLifecycleHookEnabled interface and set up REST bridges as configured
	var hooks RESTBridgeEnabled
	lcm := r.lifecycleManager
	if lcm == nil {
		lcm = NewServiceLifecycleManager(r)
	}

	// hand off registering REST bridges to Plank via bus messages
	if hooks = lcm.GetRESTBridgeEnabledService(serviceChannelName); hooks != nil {
		if err = r.bus.SendResponseMessage(
			LifecycleManagerChannelName,
			&SetupRESTBridgeRequest{ServiceChannel: serviceChannelName, Config: hooks.GetRESTBridgeConfig()},
			r.bus.GetId()); err != nil {
			return err
		}
	}

	return nil
}

func (r *serviceRegistry) UnregisterService(serviceChannelName string) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	sw, ok := r.services[serviceChannelName]
	if !ok {
		return fmt.Errorf("unable to unregister service: no service is registered for channel \"%s\"", serviceChannelName)
	}
	sw.unregister()
	delete(r.services, serviceChannelName)
	return nil
}

type fabricServiceWrapper struct {
	service           FabricService
	fabricCore        *fabricCore
	requestMsgHandler bus.MessageHandler
}

func newServiceWrapper(
	bus bus.EventBus, service FabricService, serviceChannelName string, logger *slog.Logger) *fabricServiceWrapper {

	return &fabricServiceWrapper{
		service: service,
		fabricCore: &fabricCore{
			bus:         bus,
			channelName: serviceChannelName,
			logger:      logger,
		},
	}
}

func (sw *fabricServiceWrapper) init() error {
	sw.fabricCore.bus.GetChannelManager().CreateChannel(sw.fabricCore.channelName)

	initializationService, ok := sw.service.(FabricInitializableService)
	if ok {
		initializationErr := initializationService.Init(sw.fabricCore)
		if initializationErr != nil {
			return initializationErr
		}
	}

	mh, err := sw.fabricCore.bus.ListenRequestStream(sw.fabricCore.channelName)
	if err != nil {
		return err
	}

	sw.requestMsgHandler = mh
	mh.HandleContext(
		context.Background(),
		func(ctx context.Context, message *model.Message) {
			requestPtr, ok := message.Payload.(*model.Request)
			if !ok {
				request, ok := message.Payload.(model.Request)
				if !ok {
					sw.fabricCore.log().Warn(
						"cannot cast service request payload to model.Request",
						"payloadType", fmt.Sprintf("%T", message.Payload),
						"channel", sw.fabricCore.channelName)
					return
				}
				requestPtr = &request
			}

			if message.DestinationId != nil {
				requestPtr.Id = message.DestinationId
			}

			if contextService, ok := sw.service.(ContextFabricService); ok {
				contextService.HandleServiceRequestContext(ctx, requestPtr, sw.fabricCore)
				return
			}
			sw.service.HandleServiceRequest(requestPtr, sw.fabricCore)
		},
		func(context.Context, error) {})

	return nil
}

func (sw *fabricServiceWrapper) unregister() {
	if sw.requestMsgHandler != nil {
		sw.requestMsgHandler.Close()
	}
}
