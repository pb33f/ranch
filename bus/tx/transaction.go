// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package tx

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/store"
	"sync"
)

type TransactionType int

const (
	AsyncTransaction TransactionType = iota
	SyncTransaction
)

type ReadyFunction func(responses []*model.Message)

type Transaction interface {
	// Sends a request to a channel as a part of this transaction.
	SendRequest(channel string, payload any) error
	//  Wait for a store to be initialized as a part of this transaction.
	WaitForStoreReady(storeName string) error
	// Registers a new complete handler. Once all responses to requests have been received,
	// the transaction is complete.
	OnComplete(completeHandler ReadyFunction) error
	// Register a new error handler. If an error is thrown by any of the responders, the transaction
	// is aborted and the error sent to the registered errorHandlers.
	OnError(errorHandler bus.MessageErrorFunction) error
	// Commit the transaction, all requests will be sent and will wait for responses.
	// Once all the responses are in, onComplete handlers will be called with the responses.
	Commit() error
	CommitContext(ctx context.Context) error
}

type transactionState int

const (
	uncommittedState transactionState = iota
	committedState
	completedState
	abortedState
)

type busTransactionRequest struct {
	requestIndex int
	storeName    string
	channelName  string
	payload      any
}

type busTransaction struct {
	transactionType    TransactionType
	state              transactionState
	lock               sync.Mutex
	requests           []*busTransactionRequest
	responses          []*model.Message
	onCompleteHandlers []ReadyFunction
	onErrorHandlers    []bus.MessageErrorFunction
	bus                bus.EventBus
	storeManager       store.Manager
	ctx                context.Context
	completedRequests  int
}

func New(eventBus bus.EventBus, storeManager store.Manager, transactionType TransactionType) Transaction {
	transaction := new(busTransaction)

	transaction.bus = eventBus
	transaction.storeManager = storeManager
	transaction.state = uncommittedState
	transaction.transactionType = transactionType
	transaction.requests = make([]*busTransactionRequest, 0)
	transaction.onCompleteHandlers = make([]ReadyFunction, 0)
	transaction.onErrorHandlers = make([]bus.MessageErrorFunction, 0)
	transaction.completedRequests = 0

	return transaction
}

func (tr *busTransaction) checkUncommittedState() error {
	if tr.state != uncommittedState {
		return fmt.Errorf("transaction has already been committed")
	}
	return nil
}

func (tr *busTransaction) SendRequest(channel string, payload any) error {
	tr.lock.Lock()
	defer tr.lock.Unlock()

	if err := tr.checkUncommittedState(); err != nil {
		return err
	}

	tr.requests = append(tr.requests, &busTransactionRequest{
		channelName:  channel,
		payload:      payload,
		requestIndex: len(tr.requests),
	})

	return nil
}

func (tr *busTransaction) WaitForStoreReady(storeName string) error {
	tr.lock.Lock()
	defer tr.lock.Unlock()

	if err := tr.checkUncommittedState(); err != nil {
		return err
	}

	if tr.storeManager.GetStore(storeName) == nil {
		return fmt.Errorf("cannot find store '%s'", storeName)
	}

	tr.requests = append(tr.requests, &busTransactionRequest{
		storeName:    storeName,
		requestIndex: len(tr.requests),
	})

	return nil
}

func (tr *busTransaction) OnComplete(completeHandler ReadyFunction) error {
	tr.lock.Lock()
	defer tr.lock.Unlock()

	if err := tr.checkUncommittedState(); err != nil {
		return err
	}

	tr.onCompleteHandlers = append(tr.onCompleteHandlers, completeHandler)
	return nil
}

func (tr *busTransaction) OnError(errorHandler bus.MessageErrorFunction) error {
	tr.lock.Lock()
	defer tr.lock.Unlock()

	if err := tr.checkUncommittedState(); err != nil {
		return err
	}

	tr.onErrorHandlers = append(tr.onErrorHandlers, errorHandler)
	return nil
}

func (tr *busTransaction) Commit() error {
	return tr.CommitContext(context.Background())
}

func (tr *busTransaction) CommitContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tr.lock.Lock()

	if err := tr.checkUncommittedState(); err != nil {
		tr.lock.Unlock()
		return err
	}

	if len(tr.requests) == 0 {
		tr.lock.Unlock()
		return fmt.Errorf("cannot commit empty transaction")
	}

	tr.state = committedState
	tr.ctx = ctx

	// init responses slice
	tr.responses = make([]*model.Message, len(tr.requests))
	transactionType := tr.transactionType
	tr.lock.Unlock()

	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			tr.onTransactionError(ctx.Err())
		}()
	}

	if transactionType == AsyncTransaction {
		tr.startAsyncTransaction(ctx)
	} else {
		tr.startSyncTransaction(ctx)
	}

	return nil
}

func (tr *busTransaction) startSyncTransaction(ctx context.Context) {
	tr.executeRequest(ctx, tr.requests[0])
}

func (tr *busTransaction) executeRequest(ctx context.Context, request *busTransactionRequest) {
	if err := ctx.Err(); err != nil {
		tr.onTransactionError(err)
		return
	}
	if request.storeName != "" {
		tr.waitForStore(ctx, request)
	} else {
		tr.sendRequest(ctx, request)
	}
}

func (tr *busTransaction) startAsyncTransaction(ctx context.Context) {
	for _, req := range tr.requests {
		tr.executeRequest(ctx, req)
	}
}

func (tr *busTransaction) sendRequest(ctx context.Context, req *busTransactionRequest) {
	reqId := uuid.New()

	mh, err := tr.bus.ListenOnceForDestination(req.channelName, &reqId)
	if err != nil {
		tr.onTransactionError(err)
		return
	}

	var closeOnce sync.Once
	listenerDone := make(chan struct{})
	closeListener := func() {
		closeOnce.Do(func() {
			mh.Close()
			close(listenerDone)
		})
	}
	mh.HandleContext(ctx, func(_ context.Context, message *model.Message) {
		closeListener()
		tr.onTransactionRequestSuccess(req, message)
	}, func(_ context.Context, e error) {
		closeListener()
		tr.onTransactionError(e)
	})

	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				closeListener()
			case <-listenerDone:
			}
		}()
	}

	if err := tr.bus.SendRequestMessageContext(ctx, req.channelName, req.payload, &reqId); err != nil {
		closeListener()
		tr.onTransactionError(err)
	}
}

func (tr *busTransaction) onTransactionError(err error) {
	tr.lock.Lock()
	defer tr.lock.Unlock()

	if tr.state == abortedState || tr.state == completedState {
		return
	}

	tr.state = abortedState
	for _, errorHandler := range tr.onErrorHandlers {
		go errorHandler(err)
	}
}

func (tr *busTransaction) waitForStore(ctx context.Context, req *busTransactionRequest) {
	store := tr.storeManager.GetStore(req.storeName)
	if store == nil {
		tr.onTransactionError(fmt.Errorf("cannot find store '%s'", req.storeName))
		return
	}
	if err := ctx.Err(); err != nil {
		tr.onTransactionError(err)
		return
	}
	store.WhenReady(func() {
		if err := ctx.Err(); err != nil {
			tr.onTransactionError(err)
			return
		}
		tr.onTransactionRequestSuccess(req, &model.Message{
			Direction: model.ResponseDir,
			Payload:   store.AllValuesAsMap(),
		})
	})
}

func (tr *busTransaction) onTransactionRequestSuccess(req *busTransactionRequest, message *model.Message) {
	var triggerOnCompleteHandler = false
	tr.lock.Lock()

	if tr.state == abortedState {
		tr.lock.Unlock()
		return
	}

	tr.responses[req.requestIndex] = message
	tr.completedRequests++

	if tr.completedRequests == len(tr.requests) {
		tr.state = completedState
		triggerOnCompleteHandler = true
	}

	tr.lock.Unlock()

	if triggerOnCompleteHandler {
		for _, completeHandler := range tr.onCompleteHandlers {
			go completeHandler(tr.responses)
		}
		return
	}

	// If this is a sync transaction execute the next request
	if tr.transactionType == SyncTransaction && req.requestIndex < len(tr.requests)-1 {
		tr.executeRequest(tr.ctx, tr.requests[req.requestIndex+1])
	}
}
