// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package service

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
)

// FabricServiceCore is the interface providing base functionality to fabric services.
type FabricServiceCore interface {
	// Bus Returns the EventBus instance.
	Bus() bus.EventBus

	// SendResponse Uses the "responsePayload" and "request" params to build and send model.Response object
	// on the service channel.
	SendResponse(request *model.Request, responsePayload interface{})

	// SendResponseAsString Uses the "responsePayload" and "request" params to build and send model.Response object
	// on the service channel. The payload is not marshalled to JSON, but sent as a string.
	SendResponseAsString(request *model.Request, responsePayload string)

	// SendResponseAsStringWithHeaders Uses the "responsePayload" and "request" params to build and send model.Response object
	// on the service channel. The payload is not marshalled to JSON, but sent as a string... also.. you know.. headers
	SendResponseAsStringWithHeaders(request *model.Request, responsePayload string, headers map[string]any)

	// SendResponseWithHeaders is the same as SendResponse, but include headers. Useful for HTTP REST interfaces - these headers will be
	// set as HTTP response headers. Great for custom mime-types, binary stuff and more.
	SendResponseWithHeaders(request *model.Request, responsePayload interface{}, headers map[string]any)

	// SendErrorResponse builds an error model.Response object and sends it on the service channel as response to the "request" param.
	SendErrorResponse(request *model.Request, responseErrorCode int, responseErrorMessage string)

	// SendErrorResponseWithPayload is the same as SendErrorResponse, but adds a payload
	SendErrorResponseWithPayload(request *model.Request, responseErrorCode int, responseErrorMessage string,
		payload interface{})

	// SendErrorResponseWithHeaders is the same as SendErrorResponse, but adds headers as well.
	SendErrorResponseWithHeaders(request *model.Request, responseErrorCode int, responseErrorMessage string,
		headers map[string]any)

	// SendErrorResponseAsStringWithHeadersAndPayload is the same as SendErrorResponseWithPayload, but adds headers as well.
	SendErrorResponseAsStringWithHeadersAndPayload(request *model.Request, responseErrorCode int, responseErrorMessage string,
		payload string, headers map[string]any)

	// SendErrorResponseWithHeadersAndPayload is the same as SendErrorResponseWithPayload, but adds headers as well.
	SendErrorResponseWithHeadersAndPayload(request *model.Request, responseErrorCode int, responseErrorMessage string,
		payload interface{}, headers map[string]any)

	// HandleUnknownRequest handles unknown/unsupported/un-implemented requests,
	HandleUnknownRequest(request *model.Request)

	// RestServiceRequest will make a new RestService call.
	RestServiceRequest(restRequest *RestServiceRequest,
		successHandler model.ResponseHandlerFunction, errorHandler model.ResponseHandlerFunction)

	// SetHeaders Set global headers for a given fabric service (each service has its own set of global headers).
	// The headers will be applied to all requests made by this instance's RestServiceRequest method.
	// Global header values can be overridden per request via the RestServiceRequest.Headers property.
	SetHeaders(headers map[string]string)

	// GenerateJSONHeaders Automatically ready to go map with json headers.
	GenerateJSONHeaders() map[string]string

	// SetDefaultJSONHeaders Automatically sets default accept and return content types as 'application/json'
	SetDefaultJSONHeaders()
}

type fabricCore struct {
	channelName string
	bus         bus.EventBus
	headers     map[string]string
}

func (core *fabricCore) Bus() bus.EventBus {
	return core.bus
}

func (core *fabricCore) SendResponse(request *model.Request, responsePayload interface{}) {

	headers := core.mergeHeadersWithDefaults(nil)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           responsePayload,
		Headers:           headers,
		Marshal:           true,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendResponseAsStringWithHeaders(request *model.Request, responsePayload string, headers map[string]any) {

	headers = core.mergeHeadersWithDefaults(headers)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           responsePayload,
		Headers:           headers,
		Marshal:           false,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendResponseAsString(request *model.Request, responsePayload string) {

	headers := core.mergeHeadersWithDefaults(nil)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           responsePayload,
		Headers:           headers,
		Marshal:           false,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendResponseWithHeaders(request *model.Request, responsePayload interface{}, headers map[string]any) {

	headers = core.mergeHeadersWithDefaults(headers)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           responsePayload,
		Marshal:           true,
		BrokerDestination: request.BrokerDestination,
		Headers:           headers,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendErrorResponse(
	request *model.Request, responseErrorCode int, responseErrorMessage string) {
	core.SendErrorResponseWithPayload(request, responseErrorCode, responseErrorMessage, nil)
}

func (core *fabricCore) SendErrorResponseWithPayload(
	request *model.Request,
	responseErrorCode int, responseErrorMessage string, payload interface{}) {

	headers := core.mergeHeadersWithDefaults(nil)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           payload,
		Headers:           headers,
		Error:             true,
		Marshal:           true,
		ErrorCode:         responseErrorCode,
		ErrorMessage:      responseErrorMessage,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendErrorResponseWithHeaders(
	request *model.Request,
	responseErrorCode int, responseErrorMessage string, headers map[string]any) {

	headers = core.mergeHeadersWithDefaults(headers)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Headers:           headers,
		Error:             true,
		Marshal:           true,
		ErrorCode:         responseErrorCode,
		ErrorMessage:      responseErrorMessage,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendErrorResponseWithHeadersAndPayload(
	request *model.Request,
	responseErrorCode int, responseErrorMessage string, payload interface{}, headers map[string]any) {

	headers = core.mergeHeadersWithDefaults(headers)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           payload,
		Headers:           headers,
		Error:             true,
		Marshal:           true,
		ErrorCode:         responseErrorCode,
		ErrorMessage:      responseErrorMessage,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) SendErrorResponseAsStringWithHeadersAndPayload(
	request *model.Request,
	responseErrorCode int, responseErrorMessage string, payload string, headers map[string]any) {

	headers = core.mergeHeadersWithDefaults(headers)

	response := &model.Response{
		Id:                request.Id,
		Destination:       core.channelName,
		Payload:           payload,
		Headers:           headers,
		Error:             true,
		Marshal:           false,
		ErrorCode:         responseErrorCode,
		ErrorMessage:      responseErrorMessage,
		BrokerDestination: request.BrokerDestination,
	}
	core.bus.SendResponseMessage(core.channelName, response, request.Id)
}

func (core *fabricCore) HandleUnknownRequest(request *model.Request) {
	errorMsg := fmt.Sprintf("unsupported request for \"%s\": %s", core.channelName, request.RequestCommand)
	core.SendErrorResponse(request, 403, errorMsg)
}

func (core *fabricCore) SetHeaders(headers map[string]string) {
	core.headers = headers
}

func (core *fabricCore) GenerateJSONHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/json"}
}

func (core *fabricCore) SetDefaultJSONHeaders() {
	core.SetHeaders(core.GenerateJSONHeaders())
}

func (core *fabricCore) mergeHeadersWithDefaults(headers map[string]any) map[string]any {

	// merge global service headers with the headers from user supplied headers.
	// note that headers specified in user requirements will override the global headers.
	mergedHeaders := make(map[string]any)
	for k, v := range core.headers {
		mergedHeaders[k] = v
	}
	if headers != nil {
		for k, v := range headers {
			mergedHeaders[k] = v
		}
	}
	return mergedHeaders
}

func (core *fabricCore) RestServiceRequest(restRequest *RestServiceRequest,
	successHandler model.ResponseHandlerFunction, errorHandler model.ResponseHandlerFunction) {

	// merge global service headers with the headers from the httpRequest
	// note that headers specified in restRequest override the global headers.
	mergedHeaders := make(map[string]string)
	for k, v := range core.headers {
		mergedHeaders[k] = v
	}
	for k, v := range restRequest.Headers {
		mergedHeaders[k] = v
	}
	restRequest.Headers = mergedHeaders

	id := uuid.New()
	request := &model.Request{
		Id:      &id,
		Payload: restRequest,
	}
	mh, _ := core.bus.ListenOnceForDestination(restServiceChannel, request.Id)
	mh.Handle(func(message *model.Message) {
		response := message.Payload.(*model.Response)
		if response.Error {
			errorHandler(response)
		} else {
			successHandler(response)
		}
	}, func(e error) {
		errorHandler(&model.Response{
			Error:        true,
			ErrorMessage: e.Error(),
			ErrorCode:    500,
		})
	})
	core.bus.SendRequestMessage(restServiceChannel, request, request.Id)
}
