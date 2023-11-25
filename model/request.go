// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"github.com/google/uuid"
	"net/http"
	"net/url"
)

type Request struct {
	Id                 *uuid.UUID          `json:"id,omitempty"`
	Destination        string              `json:"channel,omitempty"`
	Payload            interface{}         `json:"payload,omitempty"`
	RequestCommand     string              `json:"request,omitempty" mapstructure:"request"`
	HttpRequest        *http.Request       `json:"-"`
	HttpResponseWriter http.ResponseWriter `json:"-"`
	// Populated if the request was sent on a "private" channel and
	// indicates where to send back the Response.
	// A service should check this field and if not null copy it to the
	// Response.BrokerDestination field to ensure that the response will be sent
	// back on the correct the "private" channel.
	BrokerDestination *BrokerDestinationConfig `json:"-"`
}

// CreateServiceRequest is a small utility function that takes request type and payload and
// returns a new model.Request instance populated with them
func CreateServiceRequest(requestType string, body []byte) Request {
	id := uuid.New()
	return Request{
		Id:             &id,
		RequestCommand: requestType,
		Payload:        body}
}

// CreateServiceRequestWithValues does the same as CreateServiceRequest, except the payload is url.Values and not
// A byte[] array
func CreateServiceRequestWithValues(requestType string, vals url.Values) Request {
	id := uuid.New()
	return Request{
		Id:             &id,
		RequestCommand: requestType,
		Payload:        vals}
}

// CreateServiceRequestWithHttpRequest does the same as CreateServiceRequest, except the payload is a pointer to the
// Incoming http.Request, so you can essentially extract what ever you want from the incoming request within your service.
func CreateServiceRequestWithHttpRequest(requestType string, r *http.Request) Request {
	id := uuid.New()
	return Request{
		Id:             &id,
		RequestCommand: requestType,
		Payload:        r}
}
