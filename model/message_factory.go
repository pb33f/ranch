// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package model

import "github.com/google/uuid"

// MessageConfig contains the fields used by message factory helpers.
type MessageConfig struct {
	Id            *uuid.UUID
	DestinationId *uuid.UUID
	Destination   string
	Channel       string
	Payload       any
	Headers       []MessageHeader
	Direction     Direction
	Err           error
}

func checkId(msgConfig *MessageConfig) {
	if msgConfig.Id == nil {
		id := uuid.New()
		msgConfig.Id = &id
	}
}

// GenerateRequest creates a request message from msgConfig.
func GenerateRequest(msgConfig *MessageConfig) *Message {
	checkId(msgConfig)
	return &Message{
		Headers:       msgConfig.Headers,
		Id:            msgConfig.Id,
		Channel:       msgConfig.Channel,
		DestinationId: msgConfig.DestinationId,
		Destination:   msgConfig.Destination,
		Payload:       msgConfig.Payload,
		Direction:     RequestDir}
}

// GenerateResponse creates a response message from msgConfig.
func GenerateResponse(msgConfig *MessageConfig) *Message {
	checkId(msgConfig)
	return &Message{
		Headers:       msgConfig.Headers,
		Id:            msgConfig.Id,
		Channel:       msgConfig.Channel,
		DestinationId: msgConfig.DestinationId,
		Destination:   msgConfig.Destination,
		Payload:       msgConfig.Payload,
		Direction:     ResponseDir}
}

// GenerateError creates an error message from msgConfig.
func GenerateError(msgConfig *MessageConfig) *Message {
	checkId(msgConfig)
	return &Message{
		Id:            msgConfig.Id,
		Channel:       msgConfig.Channel,
		DestinationId: msgConfig.DestinationId,
		Destination:   msgConfig.Destination,
		Error:         msgConfig.Err,
		Direction:     ErrorDir}
}
