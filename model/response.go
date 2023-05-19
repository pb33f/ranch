// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"github.com/google/uuid"
)

// Response represents a payload sent by a Fabric application.
type Response struct {
	Id           *uuid.UUID  `json:"id,omitempty"`
	Destination  string      `json:"channel,omitempty"`
	Payload      interface{} `json:"payload,omitempty"`
	Error        bool        `json:"error,omitempty"`
	ErrorCode    int         `json:"errorCode,omitempty"`
	ErrorMessage string      `json:"errorMessage,omitempty"`
	// If populated the response will be sent to a single client
	// on the specified destination topic.
	BrokerDestination *BrokerDestinationConfig `json:"-"`
	Headers           map[string]interface{}   `json:"-"` // passthrough any http headers
	Marshal           bool                     `json:"-"` // if true, the payload be marshalled into JSON.
}

// Used to specify the target user queue of the Response
type BrokerDestinationConfig struct {
	Destination  string
	ConnectionId string
}

type ResponseHandlerFunction func(*Response)
