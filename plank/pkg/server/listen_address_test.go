package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestExplicitHTTPListenAddress(t *testing.T) {
	cfg := &PlatformServerConfig{RootDir: t.TempDir(), HTTPListenAddress: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NoBanner: true}
	ps := NewPlatformServer(cfg).(*platformServer)
	if ps.HttpServer.Addr != cfg.HTTPListenAddress {
		t.Fatal(ps.HttpServer.Addr)
	}
	// The same address handed to StartServer must resolve to a loopback listener.
	ln, err := net.Listen("tcp", ps.HttpServer.Addr)
	if err != nil {
		t.Fatal(err)
	}
	if !ln.Addr().(*net.TCPAddr).IP.IsLoopback() {
		t.Fatal(ln.Addr())
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Use a fixed reserved port so the running HTTP endpoint can be probed.
	ps.HttpServer.Addr = ln.Addr().String()
	done := make(chan error, 1)
	go func() { done <- ps.StartServer(ctx, make(chan os.Signal, 1)) }()
	select {
	case <-ps.Ready():
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + ln.Addr().String() + "/native-listener-check")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatal(response.StatusCode)
	}
	ps.StopServer()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestDefaultHTTPListenAddress(t *testing.T) {
	cfg := &PlatformServerConfig{RootDir: t.TempDir(), Port: 9090, Host: "advertised.example", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NoBanner: true}
	ps := NewPlatformServer(cfg).(*platformServer)
	if ps.HttpServer.Addr != ":9090" {
		t.Fatal(ps.HttpServer.Addr)
	}
}
