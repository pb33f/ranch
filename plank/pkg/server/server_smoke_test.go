package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/stretchr/testify/assert"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSmokeTests_TLS tests if Plank starts with TLS enabled
func TestSmokeTests_TLS(t *testing.T) {
	// pre-arrange
	testRoot := filepath.Join(os.TempDir(), "plank-tests")
	_ = os.MkdirAll(testRoot, 0750)
	defer func() { _ = os.RemoveAll(testRoot) }()

	// arrange
	port := GetTestPort()
	cfg := GetBasicTestServerConfig(testRoot, "stdout", "null", "stderr", port, true)
	cfg.FabricConfig = GetTestFabricBrokerConfig()
	cfg.TLSCertConfig = GetTestTLSCertConfig(testRoot)

	// act
	var wg sync.WaitGroup
	sigChan := make(chan os.Signal)
	baseUrl, _, testServer := CreateTestServer(cfg)

	// assert to make sure the server was created with the correct test arguments
	assert.EqualValues(t, fmt.Sprintf("https://localhost:%d", port), baseUrl)

	wg.Add(1)
	go func() { assert.NoError(t, testServer.StartServer(context.Background(), sigChan)) }()

	originalTransport := http.DefaultTransport
	originalTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- smoke test uses a self-signed cert.

	RunWhenPlatformServerReady(t, testServer, func(t *testing.T) {
		resp, err := http.Get(baseUrl) // #nosec G107 -- test URL is constructed from the local test server.
		if err != nil {
			defer func() {
				testServer.StopServer()
				wg.Done()
			}()
			t.Fatal(err)
		}
		assert.EqualValues(t, http.StatusNotFound, resp.StatusCode)
		testServer.StopServer()
		wg.Done()
	})
	wg.Wait()
}

// TestSmokeTests_TLS_InvalidCert tests if Plank fails to start because of an invalid cert
func TestSmokeTests_TLS_InvalidCert(t *testing.T) {
	testRoot := filepath.Join(os.TempDir(), "plank-tests-invalid-cert")
	_ = os.MkdirAll(testRoot, 0750)
	defer func() { _ = os.RemoveAll(testRoot) }()

	port := GetTestPort()
	cfg := GetBasicTestServerConfig(testRoot, "stdout", "null", "stderr", port, true)
	cfg.FabricConfig = nil
	cfg.TLSCertConfig = &TLSCertConfig{
		CertFile: filepath.Join(testRoot, "missing.crt"),
		KeyFile:  filepath.Join(testRoot, "missing.key"),
	}

	_, _, testServer := CreateTestServer(cfg)
	err := testServer.StartServer(context.Background(), make(chan os.Signal, 1))

	assert.Error(t, err)
	select {
	case <-testServer.Ready():
		t.Fatal("server became ready after TLS startup failure")
	default:
	}
}

func TestSmokeTests(t *testing.T) {
	testRoot := filepath.Join(os.TempDir(), "plank-tests")
	_ = os.MkdirAll(testRoot, 0750)
	defer func() { _ = os.RemoveAll(testRoot) }()

	port := GetTestPort()
	cfg := GetBasicTestServerConfig(testRoot, "stdout", "stdout", "stderr", port, true)
	cfg.NoBanner = true
	cfg.FabricConfig = GetTestFabricBrokerConfig()

	baseUrl, _, testServer := CreateTestServer(cfg)

	assert.EqualValues(t, fmt.Sprintf("http://localhost:%d", port), baseUrl)

	syschan := make(chan os.Signal, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, testServer.StartServer(context.Background(), syschan)) }()
	RunWhenPlatformServerReady(t, testServer, func(t *testing.T) {
		// root url - 404
		t.Run("404 on root", func(t2 *testing.T) {
			cl := http.DefaultClient
			rsp, err := cl.Get(baseUrl)
			assert.Nil(t2, err)
			assert.EqualValues(t2, 404, rsp.StatusCode)
		})

		// connection to fabric endpoint
		t.Run("fabric endpoint should exist", func(t2 *testing.T) {
			cl := http.DefaultClient
			rsp, err := cl.Get(fmt.Sprintf("%s/ws", baseUrl))
			assert.Nil(t2, err)
			assert.EqualValues(t2, 400, rsp.StatusCode)
		})

		testServer.StopServer()
		wg.Done()
	})
	wg.Wait()
}

func TestSmokeTests_NoFabric(t *testing.T) {
	testRoot := filepath.Join(os.TempDir(), "plank-tests")
	_ = os.MkdirAll(testRoot, 0750)
	defer func() { _ = os.RemoveAll(testRoot) }()

	port := GetTestPort()
	cfg := GetBasicTestServerConfig(testRoot, "stdout", "stdout", "stderr", port, true)
	cfg.FabricConfig = nil
	baseUrl, _, testServer := CreateTestServer(cfg)

	assert.EqualValues(t, fmt.Sprintf("http://localhost:%d", port), baseUrl)

	syschan := make(chan os.Signal, 1)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() { assert.NoError(t, testServer.StartServer(context.Background(), syschan)) }()
	RunWhenPlatformServerReady(t, testServer, func(t *testing.T) {
		// fabric - 404
		t.Run("404 on fabric endpoint", func(t2 *testing.T) {
			cl := http.DefaultClient
			rsp, err := cl.Get(fmt.Sprintf("%s/ws", baseUrl))
			assert.Nil(t2, err)
			assert.EqualValues(t2, 404, rsp.StatusCode)
		})

		testServer.StopServer()
		wg.Done()
	})
	wg.Wait()
}
