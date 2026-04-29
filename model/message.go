// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-viper/mapstructure/v2"
	"github.com/google/uuid"
	"reflect"
)

// Direction int defining which way messages are travelling on a Channel.
type Direction int

const (
	// RequestDir marks a message as a service request.
	RequestDir Direction = 0
	// ResponseDir marks a message as a service response.
	ResponseDir Direction = 1
	// ErrorDir marks a message as an error response.
	ErrorDir Direction = 2
)

// A Message is the encapsulation of the event sent on the bus.
// It holds a Direction, errors, a Payload and more.
type Message struct {
	Id            *uuid.UUID      `json:"id"`            // message identifier
	DestinationId *uuid.UUID      `json:"destinationId"` // destinationId (targeted recipient)
	Channel       string          `json:"channel"`       // reference to channel message was sent on.
	Destination   string          `json:"destination"`   // destination message was sent to (if galactic)
	Payload       any             `json:"payload"`
	Error         error           `json:"error"`
	Direction     Direction       `json:"direction"`
	Headers       []MessageHeader `json:"headers"`
}

// MessageHeader carries message metadata as a label/value pair.
type MessageHeader struct {
	Label string
	Value string
}

// Decode unwraps a Message payload from a Response envelope and decodes it into T.
func Decode[T any](msg *Message) (T, error) {
	var zero T
	if msg == nil {
		return zero, fmt.Errorf("Decode: message cannot be nil")
	}

	payload, err := unwrapResponsePayload("Decode", msg.Payload)
	if err != nil {
		return zero, err
	}

	return decodePayload[T](payload)
}

// CastPayloadToType converts the raw any typed Payload into the
// specified object passed as an argument.
func (m *Message) CastPayloadToType(typ any) error {
	if m == nil {
		return fmt.Errorf("CastPayloadToType: message cannot be nil")
	}

	// assert pointer type
	typVal := reflect.ValueOf(typ)
	if typVal.Kind() != reflect.Ptr {
		return fmt.Errorf("CastPayloadToType: invalid argument. argument should be the address of an object")
	}

	// nil-check
	if typVal.IsNil() {
		return fmt.Errorf("CastPayloadToType: cannot cast to nil")
	}

	payload, err := unwrapResponsePayload("CastPayloadToType", m.Payload)
	if err != nil {
		return err
	}

	return decodePayloadInto(payload, typ)
}

func unwrapResponsePayload(operation string, payload any) (any, error) {
	switch p := payload.(type) {
	case *Response:
		return responsePayload(p)
	case Response:
		return responsePayload(&p)
	case []byte:
		var unwrappedResponse Response
		if err := json.Unmarshal(p, &unwrappedResponse); err != nil {
			return nil, fmt.Errorf("%s: failed to unmarshal payload %v: %w", operation, payload, err)
		}
		return responsePayload(&unwrappedResponse)
	default:
		return payload, nil
	}
}

func responsePayload(resp *Response) (any, error) {
	if resp == nil {
		return nil, nil
	}
	if resp.Error {
		return nil, errors.New(resp.ErrorMessage)
	}
	return resp.Payload, nil
}

func decodePayload[T any](payload any) (T, error) {
	var decoded T
	if payload == nil {
		return decoded, nil
	}
	if typed, ok := payload.(T); ok {
		return typed, nil
	}

	decodedType := reflect.TypeOf((*T)(nil)).Elem()
	if decodedType.Kind() == reflect.Ptr {
		decodedPtr := reflect.New(decodedType.Elem())
		if err := decodePayloadInto(payload, decodedPtr.Interface()); err != nil {
			return decoded, err
		}
		return decodedPtr.Interface().(T), nil
	}

	if err := decodePayloadInto(payload, &decoded); err != nil {
		return decoded, err
	}
	return decoded, nil
}

func decodePayloadInto(payload any, target any) error {
	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Ptr || targetValue.IsNil() {
		return fmt.Errorf("decodePayloadInto: target must be a non-nil pointer")
	}
	if payload == nil {
		targetValue.Elem().Set(reflect.Zero(targetValue.Elem().Type()))
		return nil
	}

	payloadValue := reflect.ValueOf(payload)
	if payloadValue.Type().AssignableTo(targetValue.Elem().Type()) {
		targetValue.Elem().Set(payloadValue)
		return nil
	}
	return mapstructure.Decode(payload, target)
}
