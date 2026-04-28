// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/plank/services"
	"github.com/pb33f/ranch/service"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context/ctxhttp"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewPlatformServer(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config)
	assert.NotNil(t, ps)
}

func TestNewPlatformServer_EmptyRootDir(t *testing.T) {
	port := GetTestPort()
	newConfig := GetBasicTestServerConfig("", "stdout", "stdout", "stderr", port, true)
	NewPlatformServer(newConfig)
	wd, _ := os.Getwd()
	assert.Equal(t, wd, newConfig.RootDir)
}

func TestPlatformServer_StartServer(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config)
	syschan := make(chan os.Signal, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, ps.StartServer(context.Background(), syschan)) }()
	RunWhenPlatformServerReady(t, ps, func(t2 *testing.T) {
		rsp, err := http.Get(fmt.Sprintf("http://localhost:%d", port))
		assert.Nil(t, err)

		_, err = io.ReadAll(rsp.Body)
		assert.Nil(t, err)
		assert.Equal(t, 404, rsp.StatusCode)
		ps.StopServer()
		wg.Done()
	})

	wg.Wait()
}

func TestPlatformServer_Ready_AfterHttpOnly(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	config.FabricConfig = nil
	ps := NewPlatformServer(config)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ps.StartServer(ctx, make(chan os.Signal, 1))
	}()

	select {
	case <-ps.Ready():
		assert.Nil(t, ps.GetStompServer())
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	cancel()
	assert.NoError(t, <-done)
}

func TestPlatformServer_Ready_AfterFabric(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	config.FabricConfig = GetTestFabricBrokerConfig()
	ps := NewPlatformServer(config)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ps.StartServer(ctx, make(chan os.Signal, 1))
	}()

	select {
	case <-ps.Ready():
		assert.NotNil(t, ps.GetStompServer())
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	cancel()
	assert.NoError(t, <-done)
}

func TestPlatformServer_RegisterService(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config)

	err := ps.RegisterService(services.NewPingPongService(), services.PingPongServiceChan)
	assert.Nil(t, err)
}

func TestPlatformServer_SetHttpPathPrefixChannelBridge(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config)

	// register a service
	_ = ps.RegisterService(services.NewPingPongService(), services.PingPongServiceChan)

	// set PathPrefix bridge
	bridgeConfig := &service.RESTBridgeConfig{
		ServiceChannel: services.PingPongServiceChan,
		Uri:            "/ping-pong",
		FabricRequestBuilder: func(w http.ResponseWriter, r *http.Request) model.Request {
			return model.Request{
				Payload:        "hello",
				RequestCommand: "ping-get",
			}
		},
	}
	ps.SetHttpPathPrefixChannelBridge(bridgeConfig)

	syschan := make(chan os.Signal, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, ps.StartServer(context.Background(), syschan)) }()
	RunWhenPlatformServerReady(t, ps, func(t2 *testing.T) {
		// GET
		rsp, err := http.Get(fmt.Sprintf("http://localhost:%d/ping-pong", port))
		assert.Nil(t, err)

		body, err := io.ReadAll(rsp.Body)
		assert.Nil(t, err)
		assert.Contains(t, string(body), "hello")

		// POST
		rsp, err = http.Post(fmt.Sprintf("http://localhost:%d/ping-pong", port), "application/json", strings.NewReader(""))
		assert.Nil(t, err)
		body, err = io.ReadAll(rsp.Body)
		assert.Nil(t, err)
		assert.Contains(t, string(body), "hello")

		// DELETE
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:%d/ping-pong", port), strings.NewReader(""))
		rsp, err = ctxhttp.Do(context.Background(), http.DefaultClient, req)
		assert.Nil(t, err)
		body, err = io.ReadAll(rsp.Body)
		assert.Nil(t, err)
		assert.Contains(t, string(body), "hello")

		ps.StopServer()
		_ = ps.UnregisterService(services.PingPongServiceChan)
		wg.Done()
	})

	wg.Wait()
}

func TestPlatformServer_SetHttpChannelBridge(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config)
	_ = ps.RegisterService(services.NewPingPongService(), services.PingPongServiceChan)

	syschan := make(chan os.Signal, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, ps.StartServer(context.Background(), syschan)) }()
	RunWhenPlatformServerReady(t, ps, func(t2 *testing.T) {
		rsp, err := http.Get(fmt.Sprintf("http://localhost:%d/rest/ping-pong2?message=hello", port))
		assert.Nil(t, err)

		body, err := io.ReadAll(rsp.Body)
		assert.Nil(t, err)
		assert.Contains(t, string(body), "hello")
		ps.StopServer()
		_ = ps.UnregisterService(services.PingPongServiceChan)
		wg.Done()
	})

	wg.Wait()
}

func TestPlatformServer_UnknownRequest(t *testing.T) {
	port := GetTestPort()
	config := GetBasicTestServerConfig(os.TempDir(), "stdout", "stdout", "stderr", port, true)
	ps := NewPlatformServer(config)
	_ = ps.RegisterService(services.NewPingPongService(), services.PingPongServiceChan)
	defer func() { _ = ps.UnregisterService(services.PingPongServiceChan) }()
	setupBridge(ps, "/ping", "GET", services.PingPongServiceChan, "bubble")

	syschan := make(chan os.Signal, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, ps.StartServer(context.Background(), syschan)) }()
	RunWhenPlatformServerReady(t, ps, func(t2 *testing.T) {
		rsp, err := http.Get(fmt.Sprintf("http://localhost:%d/ping?msg=hello", port))
		assert.Nil(t, err)

		body, err := io.ReadAll(rsp.Body)
		assert.Nil(t, err)
		assert.Contains(t, string(body), "unsupported request")

		ps.StopServer()
		wg.Done()
	})

	wg.Wait()
}

func setupBridge(ps PlatformServer, endpoint, method, channel, request string) {
	bridgeConfig := &service.RESTBridgeConfig{
		ServiceChannel: channel,
		Uri:            endpoint,
		Method:         method,
		AllowHead:      false,
		AllowOptions:   false,
		FabricRequestBuilder: func(w http.ResponseWriter, r *http.Request) model.Request {
			q := r.URL.Query()
			return model.Request{
				Id:             &uuid.UUID{},
				Payload:        q.Get("msg"),
				RequestCommand: request}

		},
	}
	ps.SetHttpChannelBridge(bridgeConfig)
}
