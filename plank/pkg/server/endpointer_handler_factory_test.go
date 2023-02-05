package server

import (
    "encoding/json"
    "fmt"
    "github.com/google/uuid"
    "github.com/pb33f/ranch/bus"
    "github.com/pb33f/ranch/model"
    "github.com/pb33f/ranch/service"
    "github.com/stretchr/testify/assert"
    "net/http"
    "os"
    "testing"
    "time"
)

func TestBuildEndpointHandler_Timeout(t *testing.T) {
    b := bus.ResetBus()
    service.ResetServiceRegistry()
    msgChan := make(chan *model.Message, 1)
    _ = b.GetChannelManager().CreateChannel("test-chan")
    port := GetTestPort()
    config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
    ps := NewPlatformServer(config).(*platformServer)
    ps.eventbus = b
    assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
        return model.Request{
            Payload: nil,
            Request: "test-request",
        }
    }, 5*time.Millisecond, msgChan), "GET", "http://localhost", nil, "request timed out")
}

func TestBuildEndpointHandler_ChanResponseErr(t *testing.T) {
    b := bus.ResetBus()
    service.ResetServiceRegistry()
    msgChan := make(chan *model.Message, 1)
    _ = b.GetChannelManager().CreateChannel("test-chan")
    port := GetTestPort()
    config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
    ps := NewPlatformServer(config).(*platformServer)
    ps.eventbus = b
    assert.HTTPErrorf(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
        uId := &uuid.UUID{}
        msgChan <- &model.Message{Error: fmt.Errorf("test error")}
        return model.Request{
            Id:      uId,
            Payload: nil,
            Request: "test-request",
        }
    }, 5*time.Second, msgChan), "GET", "http://localhost", nil, "test error")
}

func TestBuildEndpointHandler_SuccessResponse(t *testing.T) {
    b := bus.ResetBus()
    service.ResetServiceRegistry()
    msgChan := make(chan *model.Message, 1)
    _ = b.GetChannelManager().CreateChannel("test-chan")
    port := GetTestPort()
    config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
    ps := NewPlatformServer(config).(*platformServer)
    ps.eventbus = b
    assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
        uId := &uuid.UUID{}
        msgChan <- &model.Message{Payload: &model.Response{
            Id:      uId,
            Payload: []byte("{\"error\": false}"),
        }}
        return model.Request{
            Id:      uId,
            Payload: nil,
            Request: "test-request",
        }
    }, 5*time.Second, msgChan), "GET", "http://localhost", nil, "{\"error\": false}")
}

func TestBuildEndpointHandler_ErrorResponse(t *testing.T) {
    b := bus.ResetBus()
    service.ResetServiceRegistry()
    _ = b.GetChannelManager().CreateChannel("test-chan")
    port := GetTestPort()
    config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
    ps := NewPlatformServer(config).(*platformServer)
    ps.eventbus = b

    msgChan := make(chan *model.Message, 1)
    uId := &uuid.UUID{}
    rsp := &model.Response{
        Id:        uId,
        Payload:   "{\"error\": true}",
        ErrorCode: 500,
        Error:     true,
    }
    expected, _ := json.Marshal(rsp.Payload)

    assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
        msgChan <- &model.Message{Payload: rsp}
        return model.Request{
            Id:      uId,
            Payload: nil,
            Request: "test-request",
        }

    }, 5*time.Second, msgChan), "GET", "http://localhost", nil, string(expected))
}

func TestBuildEndpointHandler_ErrorResponseAlternative(t *testing.T) {
    b := bus.ResetBus()
    service.ResetServiceRegistry()
    msgChan := make(chan *model.Message, 1)
    _ = b.GetChannelManager().CreateChannel("test-chan")
    port := GetTestPort()
    config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
    ps := NewPlatformServer(config).(*platformServer)
    ps.eventbus = b

    uId := &uuid.UUID{}
    rsp := &model.Response{
        Id:        uId,
        ErrorCode: 418,
        Error:     true,
    }

    assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
        msgChan <- &model.Message{Payload: rsp}
        return model.Request{
            Id:      uId,
            Payload: nil,
            Request: "test-request",
        }

    }, 5*time.Second, msgChan), "GET", "http://localhost", nil, "418")
}

func TestBuildEndpointHandler_CatchPanic(t *testing.T) {
    b := bus.ResetBus()
    service.ResetServiceRegistry()
    msgChan := make(chan *model.Message, 1)
    _ = b.GetChannelManager().CreateChannel("test-chan")
    port := GetTestPort()
    config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
    ps := NewPlatformServer(config).(*platformServer)
    ps.eventbus = b
    assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
        panic("peekaboo")
        return model.Request{
            Payload: nil,
            Request: "test-request",
        }
    }, 5*time.Second, msgChan), "GET", "http://localhost", nil, "Internal Server Error")
}
