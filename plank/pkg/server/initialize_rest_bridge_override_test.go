package server

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/plank/services"
	"github.com/pb33f/ranch/service"
	"github.com/stretchr/testify/assert"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestInitialize_DebugLogging(t *testing.T) {
	// arrange
	testRoot := filepath.Join(os.TempDir(), "plank-tests")
	_ = os.MkdirAll(testRoot, 0750)
	defer func() { _ = os.RemoveAll(testRoot) }()

	cfg := GetBasicTestServerConfig(testRoot, "stdout", "stdout", "stderr", GetTestPort(), true)
	cfg.Debug = true

	// act
	_, _, _ = CreateTestServer(cfg)

}

func TestInitialize_RestBridgeOverride(t *testing.T) {
	// arrange
	testRoot := filepath.Join(os.TempDir(), "plank-tests")
	_ = os.MkdirAll(testRoot, 0750)
	defer func() { _ = os.RemoveAll(testRoot) }()

	cfg := GetBasicTestServerConfig(testRoot, "stdout", "stdout", "stderr", GetTestPort(), true)
	baseUrl, _, testServerInterface := CreateTestServer(cfg)
	testServer := testServerInterface.(*platformServer)
	defer func() { _ = testServerInterface.UnregisterService(services.PingPongServiceChan) }()

	// register ping pong service with default bridge points of /rest/ping-pong, /rest/ping-pong2 and /rest/ping-pong/{from}/{to}/{message}
	assert.NoError(t, testServerInterface.RegisterService(services.NewPingPongService(), services.PingPongServiceChan))

	// start server
	syschan := make(chan os.Signal)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, testServerInterface.StartServer(context.Background(), syschan)) }()

	// act
	// replace existing rest bridges with a new config
	oldRouter := testServer.GetRouter()

	// assert
	RunWhenPlatformServerReady(t, testServerInterface, func(t2 *testing.T) {
		_ = testServerInterface.Bus().SendResponseMessage(service.LifecycleManagerChannelName, &service.SetupRESTBridgeRequest{
			ServiceChannel: services.PingPongServiceChan,
			Override:       true,
			Config: []*service.RESTBridgeConfig{
				{
					ServiceChannel: services.PingPongServiceChan,
					Uri:            "/ping-new",
					Method:         "GET",
					FabricRequestBuilder: func(w http.ResponseWriter, r *http.Request) model.Request {
						return model.Request{Id: &uuid.UUID{}, RequestCommand: "ping-get", Payload: r.URL.Query().Get("message")}
					},
				},
			},
		}, testServerInterface.Bus().GetId())

		// router instance should have been swapped
		time.Sleep(1 * time.Second)
		assert.False(t, testServer.GetRouter() == oldRouter)

		// old endpoints should 404
		rsp, err := http.Get(fmt.Sprintf("%s/rest/ping-pong", baseUrl))
		assert.Nil(t, err)
		assert.EqualValues(t, 404, rsp.StatusCode)

		rsp, err = http.Get(fmt.Sprintf("%s/rest/ping-pong2", baseUrl))
		assert.Nil(t, err)
		assert.EqualValues(t, 404, rsp.StatusCode)

		rsp, err = http.Get(fmt.Sprintf("%s/rest/ping-pong/a/b/c", baseUrl))
		assert.Nil(t, err)
		assert.EqualValues(t, 404, rsp.StatusCode)

		// new endpoints should respond successfully
		rsp, err = http.Get(fmt.Sprintf("%s/ping-new", baseUrl))
		assert.Nil(t, err)
		assert.EqualValues(t, 200, rsp.StatusCode)

		syschan <- syscall.SIGINT
		wg.Done()
	})

	wg.Wait()
}
