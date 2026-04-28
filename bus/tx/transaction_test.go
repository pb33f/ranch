// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package tx

import (
	"context"
	"errors"

	"github.com/google/uuid"
	buspkg "github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/store"
	"github.com/stretchr/testify/assert"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cancelBeforeSubscribeBus struct {
	buspkg.EventBus
	cancel context.CancelFunc
}

func (b *cancelBeforeSubscribeBus) ListenOnceForDestination(
	channelName string, destId *uuid.UUID) (buspkg.MessageHandler, error) {

	mh, err := b.EventBus.ListenOnceForDestination(channelName, destId)
	if err != nil {
		return nil, err
	}
	return &cancelBeforeSubscribeHandler{
		MessageHandler: mh,
		cancel:         b.cancel,
	}, nil
}

type cancelBeforeSubscribeHandler struct {
	buspkg.MessageHandler
	cancel context.CancelFunc
}

func (h *cancelBeforeSubscribeHandler) HandleContext(
	ctx context.Context,
	successHandler buspkg.MessageHandlerContextFunction,
	errorHandler buspkg.MessageErrorContextFunction,
) {
	h.cancel()
	time.Sleep(10 * time.Millisecond)
	h.MessageHandler.HandleContext(ctx, successHandler, errorHandler)
}

func TestBusTransaction_CommitContextCanceled(t *testing.T) {
	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)
	b.GetChannelManager().CreateChannel("test-channel")

	tr := New(b, storeManager, SyncTransaction)
	assert.Nil(t, tr.SendRequest("test-channel", "sample-request"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, tr.CommitContext(ctx), context.Canceled)
}

func TestBusTransaction_CommitContextCancelAfterCommit(t *testing.T) {
	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)
	b.GetChannelManager().CreateChannel("test-channel")

	tr := New(b, storeManager, SyncTransaction)
	assert.Nil(t, tr.SendRequest("test-channel", "sample-request"))

	errC := make(chan error, 1)
	assert.Nil(t, tr.OnError(func(e error) {
		errC <- e
	}))

	ctx, cancel := context.WithCancel(context.Background())
	assert.Nil(t, tr.CommitContext(ctx))
	cancel()

	select {
	case err := <-errC:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		assert.Fail(t, "expected transaction cancellation")
	}
}

func TestBusTransaction_CommitContextCancelClosesResponseListener(t *testing.T) {
	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)
	channel := b.GetChannelManager().CreateChannel("test-channel")

	tr := New(b, storeManager, SyncTransaction)
	assert.Nil(t, tr.SendRequest("test-channel", "sample-request"))

	ctx, cancel := context.WithCancel(context.Background())
	assert.NoError(t, tr.CommitContext(ctx))
	assert.True(t, channel.ContainsHandlers())

	cancel()
	deadline := time.After(time.Second)
	for channel.ContainsHandlers() {
		select {
		case <-deadline:
			t.Fatal("transaction response listener was not closed after cancellation")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestBusTransaction_CommitContextCancelDuringHandleSetupClosesResponseListener(t *testing.T) {
	baseBus := buspkg.NewEventBus()
	storeManager := store.NewManager(baseBus)
	channel := baseBus.GetChannelManager().CreateChannel("test-channel")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &cancelBeforeSubscribeBus{
		EventBus: baseBus,
		cancel:   cancel,
	}
	tr := New(b, storeManager, SyncTransaction)
	assert.Nil(t, tr.SendRequest("test-channel", "sample-request"))

	assert.NoError(t, tr.CommitContext(ctx))
	deadline := time.After(time.Second)
	for channel.ContainsHandlers() {
		select {
		case <-deadline:
			t.Fatal("transaction response listener was not closed after setup cancellation")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestBusTransaction_OnCompleteSync(t *testing.T) {

	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)

	b.GetChannelManager().CreateChannel("test-channel")

	var channelReqMessage *model.Message
	var requestCounter = 0

	wg := sync.WaitGroup{}

	mh, _ := b.ListenRequestStream("test-channel")
	mh.Handle(func(message *model.Message) {
		requestCounter++
		channelReqMessage = message
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "unexpected error")
	})

	tr := New(b, storeManager, SyncTransaction)

	storeManager.CreateStore("testStore")
	assert.Nil(t, tr.WaitForStoreReady("testStore"))
	assert.Nil(t, tr.SendRequest("test-channel", "sample-request"))

	var completeCounter int64

	assert.NoError(t, tr.OnComplete(func(responses []*model.Message) {
		atomic.AddInt64(&completeCounter, 1)
		wg.Done()
	}))

	assert.NoError(t, tr.OnError(func(e error) {
		assert.Fail(t, "unexpected error")
	}))

	assert.NoError(t, tr.OnComplete(func(responses []*model.Message) {
		atomic.AddInt64(&completeCounter, 1)
		assert.Equal(t, len(responses), 2)
		assert.Equal(t, responses[1].Channel, "test-channel")
		assert.Equal(t, responses[1].Payload, "sample-response")
		wg.Done()
	}))

	assert.Equal(t, requestCounter, 0)

	wg.Add(1)

	assert.Nil(t, tr.Commit())

	go storeManager.CreateStore("testStore").Initialize()

	wg.Wait()

	assert.Equal(t, requestCounter, 1)
	assert.NotNil(t, channelReqMessage)

	assert.Equal(t, channelReqMessage.Payload, "sample-request")

	for i := 0; i < 50; i++ {
		assert.NoError(t, b.SendResponseMessage("test-channel", "general-message", nil))
	}

	assert.Equal(t, completeCounter, int64(0))

	wg.Add(2)
	assert.NoError(t, b.SendResponseMessage("test-channel", "sample-response", channelReqMessage.DestinationId))

	wg.Wait()

	assert.Equal(t, tr.(*busTransaction).state, completedState)

	assert.Equal(t, completeCounter, int64(2))

	assert.NoError(t, b.SendResponseMessage("test-channel", "sample-response2", channelReqMessage.DestinationId))
	assert.Equal(t, completeCounter, int64(2))
}

func TestBusTransaction_OnCompleteErrorHandling(t *testing.T) {

	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)

	tr := New(b, storeManager, SyncTransaction)

	assert.EqualError(t, tr.Commit(), "cannot commit empty transaction")

	assert.Equal(t, tr.(*busTransaction).state, uncommittedState)

	storeManager.CreateStore("testStore")
	assert.Nil(t, tr.WaitForStoreReady("testStore"))

	assert.EqualError(t, tr.WaitForStoreReady("invalid-store"), "cannot find store 'invalid-store'")

	assert.NoError(t, tr.Commit())

	assert.EqualError(t, tr.OnComplete(func(responses []*model.Message) {}), "transaction has already been committed")

	assert.Equal(t, tr.(*busTransaction).state, committedState)
	assert.EqualError(t, tr.Commit(), "transaction has already been committed")

	assert.EqualError(t, tr.WaitForStoreReady("test"), "transaction has already been committed")
	assert.EqualError(t, tr.SendRequest("test", "test"), "transaction has already been committed")
}

func TestBusTransaction_OnErrorSync(t *testing.T) {

	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)

	tr := New(b, storeManager, SyncTransaction)

	storeManager.CreateStore("testStore")
	assert.Nil(t, tr.WaitForStoreReady("testStore"))

	b.GetChannelManager().CreateChannel("test-channel")

	var channelReqMessage *model.Message
	var requestCounter = 0

	wg := sync.WaitGroup{}

	mh, _ := b.ListenRequestStream("test-channel")
	mh.Handle(func(message *model.Message) {
		requestCounter++
		channelReqMessage = message
		wg.Done()
	}, func(e error) {
	})

	assert.NoError(t, tr.SendRequest("test-channel", "sample-request"))
	assert.NoError(t, tr.SendRequest("test-channel", "sample-request"))
	assert.NoError(t, tr.SendRequest("test-channel", "sample-request"))

	assert.NoError(t, tr.OnComplete(func(responses []*model.Message) {
		assert.Fail(t, "invalid state")
	}))

	var errorHandlerCount int64 = 0
	assert.NoError(t, tr.OnError(func(e error) {
		atomic.AddInt64(&errorHandlerCount, 1)
		wg.Done()
	}))

	assert.NoError(t, tr.OnError(func(e error) {
		atomic.AddInt64(&errorHandlerCount, 1)
		assert.EqualError(t, e, "test-error")
		wg.Done()
	}))

	assert.NoError(t, tr.Commit())

	assert.Equal(t, tr.(*busTransaction).state, committedState)

	wg.Add(1)

	storeManager.GetStore("testStore").Initialize()

	wg.Wait()

	assert.Equal(t, requestCounter, 1)
	assert.NotNil(t, channelReqMessage)

	wg.Add(2)
	assert.NoError(t, b.SendErrorMessage("test-channel", errors.New("test-error"), channelReqMessage.DestinationId))

	wg.Wait()

	assert.Equal(t, tr.(*busTransaction).state, abortedState)

	assert.Equal(t, requestCounter, 1)
	assert.Equal(t, errorHandlerCount, int64(2))

	assert.EqualError(t, tr.Commit(), "transaction has already been committed")
}

func TestBusTransaction_OnCompleteAsync(t *testing.T) {

	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)

	b.GetChannelManager().CreateChannel("test-channel")

	var channelReqMessage *model.Message
	var requestCounter = 0

	wg := sync.WaitGroup{}

	mh, _ := b.ListenRequestStream("test-channel")
	mh.Handle(func(message *model.Message) {
		requestCounter++
		channelReqMessage = message
		wg.Done()
	}, func(e error) {
		assert.Fail(t, "unexpected error")
	})

	tr := New(b, storeManager, AsyncTransaction)

	storeManager.CreateStore("testStore")
	assert.Nil(t, tr.WaitForStoreReady("testStore"))
	assert.Nil(t, tr.WaitForStoreReady("testStore"))
	storeManager.CreateStore("testStore2")
	assert.Nil(t, tr.WaitForStoreReady("testStore2"))
	storeManager.CreateStore("testStore3")
	assert.Nil(t, tr.WaitForStoreReady("testStore3"))
	assert.Nil(t, tr.SendRequest("test-channel", "sample-request"))

	var completeCounter int64

	assert.NoError(t, tr.OnComplete(func(responses []*model.Message) {
		atomic.AddInt64(&completeCounter, 1)
		wg.Done()
	}))

	assert.NoError(t, tr.OnComplete(func(responses []*model.Message) {
		atomic.AddInt64(&completeCounter, 1)
		assert.Equal(t, len(responses), 5)
		assert.Equal(t, responses[4].Channel, "test-channel")
		assert.Equal(t, responses[4].Payload, "sample-response")
		wg.Done()
	}))

	wg.Add(1)
	assert.Nil(t, tr.Commit())
	wg.Wait()

	assert.NotNil(t, storeManager.GetStore("testStore"))
	assert.NotNil(t, storeManager.GetStore("testStore2"))
	assert.NotNil(t, storeManager.GetStore("testStore3"))
	assert.Equal(t, requestCounter, 1)
	assert.NotNil(t, channelReqMessage)
	assert.Equal(t, channelReqMessage.Payload, "sample-request")

	for i := 0; i < 20; i++ {
		assert.NoError(t, b.SendResponseMessage("test-channel", "general-message", nil))
	}

	assert.Equal(t, completeCounter, int64(0))

	wg.Add(2)

	assert.NoError(t, b.SendResponseMessage("test-channel", "sample-response", channelReqMessage.DestinationId))
	storeManager.GetStore("testStore").Initialize()
	storeManager.GetStore("testStore2").Initialize()
	storeManager.GetStore("testStore3").Initialize()

	wg.Wait()

	assert.Equal(t, completeCounter, int64(2))
}

func TestBusTransaction_OnErrorAsync(t *testing.T) {

	b := buspkg.NewEventBus()
	storeManager := store.NewManager(b)

	tr := New(b, storeManager, AsyncTransaction)

	b.GetChannelManager().CreateChannel("test-channel")
	b.GetChannelManager().CreateChannel("test-channel2")

	var channelReqMessage, channelReqMessage2 *model.Message

	wg := sync.WaitGroup{}

	mh, _ := b.ListenRequestStream("test-channel")
	mh.Handle(func(message *model.Message) {
		channelReqMessage = message
		wg.Done()
	}, func(e error) {
	})

	mh2, _ := b.ListenRequestStream("test-channel2")
	mh2.Handle(func(message *model.Message) {
		channelReqMessage2 = message
		wg.Done()
	}, func(e error) {
	})

	assert.NoError(t, tr.OnComplete(func(responses []*model.Message) {
		assert.Fail(t, "invalid state")
	}))

	var errorHandlerCount int64 = 0
	assert.NoError(t, tr.OnError(func(e error) {
		atomic.AddInt64(&errorHandlerCount, 1)
		assert.EqualError(t, e, "test-error")
		wg.Done()
	}))

	assert.NoError(t, tr.SendRequest("test-channel", "sample-request"))
	assert.NoError(t, tr.SendRequest("test-channel2", "sample-request2"))

	wg.Add(2)
	assert.NoError(t, tr.Commit())
	wg.Wait()

	wg.Add(1)
	assert.NoError(t, b.SendErrorMessage("test-channel2", errors.New("test-error"), channelReqMessage2.DestinationId))

	wg.Wait()

	assert.Equal(t, errorHandlerCount, int64(1))

	for i := 0; i < 50; i++ {
		assert.NoError(t, b.SendErrorMessage("test-channel", errors.New("test-error-2"), channelReqMessage.DestinationId))
	}

	assert.Equal(t, errorHandlerCount, int64(1))
}
