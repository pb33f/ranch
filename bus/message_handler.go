// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package bus

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pb33f/ranch/model"
	"sync"
)

// MessageHandlerFunction handles a delivered bus message.
type MessageHandlerFunction func(*model.Message)

// MessageHandlerContextFunction handles a delivered bus message with the dispatch context.
type MessageHandlerContextFunction func(context.Context, *model.Message)

// MessageErrorFunction handles asynchronous bus errors.
type MessageErrorFunction func(error)

// MessageErrorContextFunction handles asynchronous bus errors with the dispatch context.
type MessageErrorContextFunction func(context.Context, error)

// MessageHandler provides access to the ID the handler is listening for from all messages
// It also provides a Handle method that accepts a success and error function as handlers.
// The Fire method will fire the message queued when using RequestOnce or RequestStream
type MessageHandler interface {
	GetId() *uuid.UUID
	GetDestinationId() *uuid.UUID
	Handle(successHandler MessageHandlerFunction, errorHandler MessageErrorFunction)
	HandleContext(
		ctx context.Context, successHandler MessageHandlerContextFunction, errorHandler MessageErrorContextFunction)
	Fire() error
	FireContext(ctx context.Context) error
	Close()
}

type messageHandler struct {
	id              *uuid.UUID
	destination     *uuid.UUID
	channel         *Channel
	requestMessage  *model.Message
	ignoreId        bool
	handlerContext  context.Context
	wrapperFunction MessageHandlerContextFunction
	successHandler  MessageHandlerContextFunction
	errorHandler    MessageErrorContextFunction
	subscriptionId  *uuid.UUID
	invokeOnce      *sync.Once
	setupOnce       sync.Once
	channelManager  ChannelManager
	closeMu         sync.Mutex
	closed          bool
}

func (msgHandler *messageHandler) Handle(successHandler MessageHandlerFunction, errorHandler MessageErrorFunction) {
	var successContextHandler MessageHandlerContextFunction
	if successHandler != nil {
		successContextHandler = func(_ context.Context, msg *model.Message) {
			successHandler(msg)
		}
	}
	var errorContextHandler MessageErrorContextFunction
	if errorHandler != nil {
		errorContextHandler = func(_ context.Context, err error) {
			errorHandler(err)
		}
	}
	msgHandler.HandleContext(
		context.Background(),
		successContextHandler,
		errorContextHandler,
	)
}

func (msgHandler *messageHandler) HandleContext(
	ctx context.Context, successHandler MessageHandlerContextFunction, errorHandler MessageErrorContextFunction) {
	if ctx == nil {
		ctx = context.Background()
	}
	msgHandler.setupOnce.Do(func() {
		msgHandler.handlerContext = ctx
		msgHandler.successHandler = successHandler
		msgHandler.errorHandler = errorHandler

		subscriptionId, _ := msgHandler.channelManager.SubscribeChannelHandlerContext(
			msgHandler.channel.Name, msgHandler.wrapperFunction, false)
		msgHandler.closeMu.Lock()
		msgHandler.subscriptionId = subscriptionId
		closed := msgHandler.closed
		msgHandler.closeMu.Unlock()
		if closed && subscriptionId != nil {
			_ = msgHandler.channelManager.UnsubscribeChannelHandler(
				msgHandler.channel.Name, subscriptionId)
		}
	})
}

func (msgHandler *messageHandler) Close() {
	msgHandler.closeMu.Lock()
	msgHandler.closed = true
	subscriptionId := msgHandler.subscriptionId
	msgHandler.closeMu.Unlock()
	if subscriptionId != nil {
		_ = msgHandler.channelManager.UnsubscribeChannelHandler(
			msgHandler.channel.Name, subscriptionId)
	}
}

func (msgHandler *messageHandler) GetId() *uuid.UUID {
	return msgHandler.id
}

func (msgHandler *messageHandler) GetDestinationId() *uuid.UUID {
	return msgHandler.destination
}

func (msgHandler *messageHandler) Fire() error {
	return msgHandler.FireContext(context.Background())
}

func (msgHandler *messageHandler) FireContext(ctx context.Context) error {
	if msgHandler.requestMessage != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		start := msgHandler.channel.activity.watermark()
		msgHandler.channel.dispatchContext(ctx, msgHandler.requestMessage)
		// Wait only for dispatches scheduled after start and before this FireContext
		// reaches quiescence; concurrent publishers can keep sending independently.
		return msgHandler.channel.activity.waitQuiescentAfter(ctx, start)
	}
	return fmt.Errorf("nothing to fire, request is empty")
}
