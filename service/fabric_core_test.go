// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package service

import (
    "errors"
    "github.com/google/uuid"
    "github.com/pb33f/ranch/bus"
    "github.com/pb33f/ranch/model"
    "github.com/stretchr/testify/assert"
    "sync"
    "testing"
)

func newTestFabricCore(channelName string) FabricServiceCore {
    eventBus := bus.NewEventBusInstance()
    eventBus.GetChannelManager().CreateChannel(channelName)
    return &fabricCore{
        channelName: channelName,
        bus:         eventBus,
    }
}

func TestFabricCore_Bus(t *testing.T) {
    core := newTestFabricCore("test-channel")
    assert.NotNil(t, core.Bus())
}

func TestFabricCore_SendMethods(t *testing.T) {
    core := newTestFabricCore("test-channel")

    mh, _ := core.Bus().ListenStream("test-channel")

    wg := sync.WaitGroup{}

    var count = 0
    var lastMessage *model.Message

    mh.Handle(func(message *model.Message) {
        count++
        lastMessage = message
        wg.Done()
    }, func(e error) {
        assert.Fail(t, "unexpected error")
    })

    id := uuid.New()
    req := model.Request{
        Id:      &id,
        Request: "test-request",
        BrokerDestination: &model.BrokerDestinationConfig{
            Destination: "test",
        },
    }

    wg.Add(1)
    core.SendResponse(&req, "test-response")
    wg.Wait()

    assert.Equal(t, count, 1)

    response, ok := lastMessage.Payload.(*model.Response)
    assert.True(t, ok)
    assert.Equal(t, response.Id, req.Id)
    assert.Equal(t, response.Payload, "test-response")
    assert.False(t, response.Error)
    assert.Equal(t, response.BrokerDestination.Destination, "test")

    wg.Add(1)
    h := make(map[string]string)
    h["hello"] = "there"
    core.SendResponseWithHeaders(&req, "test-response-with-headers", h)
    wg.Wait()

    assert.Equal(t, count, 2)

    response, ok = lastMessage.Payload.(*model.Response)
    assert.True(t, ok)
    assert.Equal(t, response.Id, req.Id)
    assert.Equal(t, response.Payload, "test-response-with-headers")
    assert.False(t, response.Error)
    assert.Equal(t, response.BrokerDestination.Destination, "test")
    assert.Equal(t, response.Headers["hello"], "there")

    wg.Add(1)
    core.SendErrorResponse(&req, 404, "test-error")
    wg.Wait()

    assert.Equal(t, count, 3)
    response = lastMessage.Payload.(*model.Response)

    assert.Equal(t, response.Id, req.Id)
    assert.Nil(t, response.Payload)
    assert.True(t, response.Error)
    assert.Equal(t, response.ErrorCode, 404)
    assert.Equal(t, response.ErrorMessage, "test-error")

    wg.Add(1)

    h = make(map[string]string)
    h["chicken"] = "nugget"
    core.SendErrorResponseWithHeaders(&req, 422, "test-header-error", h)
    wg.Wait()

    assert.Equal(t, count, 4)
    response = lastMessage.Payload.(*model.Response)

    assert.Equal(t, response.Id, req.Id)
    assert.Equal(t, response.Headers["chicken"], "nugget")
    assert.Nil(t, response.Payload)
    assert.True(t, response.Error)
    assert.Equal(t, response.ErrorCode, 422)
    assert.Equal(t, response.ErrorMessage, "test-header-error")

    wg.Add(1)

    h = make(map[string]string)
    h["potato"] = "dog"
    core.SendErrorResponseWithHeadersAndPayload(&req, 500, "test-header-payload-error", "oh my!", h)
    wg.Wait()

    assert.Equal(t, count, 5)
    response = lastMessage.Payload.(*model.Response)

    assert.Equal(t, response.Id, req.Id)
    assert.Equal(t, "dog", response.Headers["potato"])
    assert.Equal(t, "oh my!", response.Payload.(string))
    assert.True(t, response.Error)
    assert.Equal(t, response.ErrorCode, 500)
    assert.Equal(t, response.ErrorMessage, "test-header-payload-error")

    wg.Add(1)
    core.HandleUnknownRequest(&req)
    wg.Wait()

    assert.Equal(t, count, 6)
    response = lastMessage.Payload.(*model.Response)

    assert.Equal(t, response.Id, req.Id)
    assert.True(t, response.Error)
    assert.Equal(t, 403, response.ErrorCode)
    assert.Equal(t, nil, response.Payload)
}

func TestFabricCore_RestServiceRequest(t *testing.T) {

    core := newTestFabricCore("test-channel")

    core.Bus().GetChannelManager().CreateChannel(restServiceChannel)

    var lastRequest *model.Request

    wg := sync.WaitGroup{}

    mh, _ := core.Bus().ListenRequestStream(restServiceChannel)
    mh.Handle(
        func(message *model.Message) {
            lastRequest = message.Payload.(*model.Request)
            wg.Done()
        },
        func(e error) {})

    var lastSuccess, lastError *model.Response

    restRequest := &RestServiceRequest{
        Uri:     "test",
        Headers: map[string]string{"h1": "value1"},
    }

    wg.Add(1)
    core.RestServiceRequest(restRequest, func(response *model.Response) {
        lastSuccess = response
        wg.Done()
    }, func(response *model.Response) {
        lastError = response
        wg.Done()
    })

    wg.Wait()

    wg.Add(1)
    core.Bus().SendResponseMessage(restServiceChannel, &model.Response{
        Payload: "test",
    }, lastRequest.Id)
    wg.Wait()

    assert.NotNil(t, lastSuccess)
    assert.Nil(t, lastError)

    assert.Equal(t, lastRequest.Payload, restRequest)
    assert.Equal(t, len(lastRequest.Payload.(*RestServiceRequest).Headers), 1)
    assert.Equal(t, lastRequest.Payload.(*RestServiceRequest).Headers["h1"], "value1")
    assert.Equal(t, lastSuccess.Payload, "test")

    lastSuccess, lastError = nil, nil

    core.SetHeaders(map[string]string{"h2": "value2", "h1": "new-value"})

    wg.Add(1)
    core.RestServiceRequest(restRequest, func(response *model.Response) {
        lastSuccess = response
        wg.Done()
    }, func(response *model.Response) {
        lastError = response
        wg.Done()
    })

    wg.Wait()

    wg.Add(1)
    core.Bus().SendResponseMessage(restServiceChannel, &model.Response{
        ErrorMessage: "error",
        Error:        true,
        ErrorCode:    1,
    }, lastRequest.Id)
    wg.Wait()

    assert.Nil(t, lastSuccess)
    assert.NotNil(t, lastError)
    assert.Equal(t, lastError.ErrorMessage, "error")
    assert.Equal(t, lastError.ErrorCode, 1)

    assert.Equal(t, len(lastRequest.Payload.(*RestServiceRequest).Headers), 2)
    assert.Equal(t, lastRequest.Payload.(*RestServiceRequest).Headers["h1"], "value1")
    assert.Equal(t, lastRequest.Payload.(*RestServiceRequest).Headers["h2"], "value2")

    lastSuccess, lastError = nil, nil
    wg.Add(1)
    core.RestServiceRequest(restRequest, func(response *model.Response) {
        lastSuccess = response
        wg.Done()
    }, func(response *model.Response) {
        lastError = response
        wg.Done()
    })

    wg.Wait()

    wg.Add(1)
    core.Bus().SendErrorMessage(restServiceChannel, errors.New("test-error"), lastRequest.Id)
    wg.Wait()

    assert.Nil(t, lastSuccess)
    assert.NotNil(t, lastError)
    assert.Equal(t, lastError.ErrorMessage, "test-error")
    assert.Equal(t, lastError.ErrorCode, 500)
}

func TestFabricCore_GenerateJSONHeaders(t *testing.T) {
    core := newTestFabricCore("test-channel")
    h := core.GenerateJSONHeaders()
    assert.EqualValues(t, "application/json", h["Content-Type"])
}

func TestFabricCore_SetDefaultJSONHeaders(t *testing.T) {
    core := newTestFabricCore("test-channel")
    core.SetDefaultJSONHeaders()

    mh, _ := core.Bus().ListenStream("test-channel")

    wg := sync.WaitGroup{}

    var lastMessage *model.Message

    mh.Handle(func(message *model.Message) {
        lastMessage = message
        wg.Done()
    }, func(e error) {
        assert.Fail(t, "unexpected error")
    })

    id := uuid.New()
    req := model.Request{
        Id:      &id,
        Payload: "test-headers",
    }

    wg.Add(1)
    core.SendResponse(&req, "test-response")
    wg.Wait()

    response := lastMessage.Payload.(*model.Response)

    // content-type and accept should have been set.
    assert.Len(t, response.Headers, 1)
    assert.EqualValues(t, "application/json", response.Headers["Content-Type"])
}

func TestFabricCore_SetDefaultJSONHeadersEmpty(t *testing.T) {
    core := newTestFabricCore("test-channel")

    // set empty headers
    core.SetHeaders(nil)

    mh, _ := core.Bus().ListenStream("test-channel")

    wg := sync.WaitGroup{}

    var lastMessage *model.Message

    mh.Handle(func(message *model.Message) {
        lastMessage = message
        wg.Done()
    }, func(e error) {
        assert.Fail(t, "unexpected error")
    })

    id := uuid.New()
    req := model.Request{
        Id:      &id,
        Payload: "test-headers",
    }

    wg.Add(1)
    core.SendResponseWithHeaders(&req, "test-response", map[string]string{"Content-Type": "pizza/cake"})
    wg.Wait()

    response := lastMessage.Payload.(*model.Response)

    // content-type and accept should have been set.
    assert.Len(t, response.Headers, 1)
    assert.EqualValues(t, "pizza/cake", response.Headers["Content-Type"])
}
