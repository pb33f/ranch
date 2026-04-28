package server

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/bus"
	"github.com/pb33f/ranch/model"
	"github.com/stretchr/testify/assert"
	"net/http"
	"os"
	"testing"
	"time"
)

func newTestEndpointHandlerServer() (*platformServer, bus.EventBus) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config).(*platformServer)
	return ps, ps.eventbus
}

func TestBuildEndpointHandler_Timeout(t *testing.T) {
	ps, b := newTestEndpointHandlerServer()
	msgChan := make(chan *model.Message, 1)
	_ = b.GetChannelManager().CreateChannel("test-chan")
	assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
		return model.Request{
			Payload:        nil,
			RequestCommand: "test-request",
		}
	}, 5*time.Millisecond, msgChan), "GET", "http://localhost", nil, "request timed out")
}

func TestBuildEndpointHandler_ChanResponseErr(t *testing.T) {
	ps, b := newTestEndpointHandlerServer()
	msgChan := make(chan *model.Message, 1)
	_ = b.GetChannelManager().CreateChannel("test-chan")
	assert.HTTPErrorf(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
		uId := &uuid.UUID{}
		msgChan <- &model.Message{Error: fmt.Errorf("test error")}
		return model.Request{
			Id:             uId,
			Payload:        nil,
			RequestCommand: "test-request",
		}
	}, 5*time.Second, msgChan), "GET", "http://localhost", nil, "test error")
}

func TestBuildEndpointHandler_SuccessResponse(t *testing.T) {
	ps, b := newTestEndpointHandlerServer()
	msgChan := make(chan *model.Message, 1)
	_ = b.GetChannelManager().CreateChannel("test-chan")
	assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
		uId := &uuid.UUID{}
		msgChan <- &model.Message{Payload: &model.Response{
			Id:      uId,
			Payload: "{\"error\": false}",
		}}
		return model.Request{
			Id:             uId,
			Payload:        nil,
			RequestCommand: "test-request",
		}
	}, 5*time.Second, msgChan), "GET", "http://localhost", nil, "{\"error\": false}")
}

func TestBuildEndpointHandler_ErrorResponse(t *testing.T) {
	ps, b := newTestEndpointHandlerServer()
	_ = b.GetChannelManager().CreateChannel("test-chan")

	expected := `{"error": true}`

	msgChan := make(chan *model.Message, 1)
	uId := &uuid.UUID{}
	rsp := &model.Response{
		Id:        uId,
		Payload:   expected,
		ErrorCode: 500,
		Error:     true,
	}

	assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
		msgChan <- &model.Message{Payload: rsp}
		return model.Request{
			Id:             uId,
			Payload:        nil,
			RequestCommand: "test-request",
		}

	}, 5*time.Second, msgChan), "GET", "http://localhost", nil, expected)
}

func TestBuildEndpointHandler_ErrorResponseAlternative(t *testing.T) {
	ps, b := newTestEndpointHandlerServer()
	msgChan := make(chan *model.Message, 1)
	_ = b.GetChannelManager().CreateChannel("test-chan")

	uId := &uuid.UUID{}
	rsp := &model.Response{
		Id:        uId,
		ErrorCode: 418,
		Error:     true,
	}

	assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
		msgChan <- &model.Message{Payload: rsp}
		return model.Request{
			Id:             uId,
			Payload:        nil,
			RequestCommand: "test-request",
		}

	}, 5*time.Second, msgChan), "GET", "http://localhost", nil, "418")
}

func TestBuildEndpointHandler_CatchPanic(t *testing.T) {
	ps, b := newTestEndpointHandlerServer()
	msgChan := make(chan *model.Message, 1)
	_ = b.GetChannelManager().CreateChannel("test-chan")
	assert.HTTPBodyContains(t, ps.buildEndpointHandler("test-chan", func(w http.ResponseWriter, r *http.Request) model.Request {
		panic("peekaboo")
	}, 5*time.Second, msgChan), "GET", "http://localhost", nil, "Internal Server Error")
}
