// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
    "net"
    "net/http"
    "net/http/httptest"
    "sync"
    "testing"
    "time"

    "github.com/go-stomp/stomp/v3/frame"
    "github.com/gorilla/mux"
    "github.com/gorilla/websocket"
    "github.com/stretchr/testify/assert"
)

func TestWebSocketConnectionListener_NewListenerInvalidAddr(t *testing.T) {
    wsListener, err := NewWebSocketConnectionListener("invalid-addr", "/fabric", nil, nil, false)
    assert.Nil(t, wsListener)
    assert.NotNil(t, err)
}

func TestWebSocketConnectionListener_CheckOrigin(t *testing.T) {
    wsListener := &webSocketConnectionListener{
        allowedOrigins: nil,
    }

    r := new(http.Request)
    r.Header = make(http.Header)
    r.Header["Origin"] = []string{"http://localhost:4200"}
    r.Host = "localhost:8000"

    assert.Equal(t, wsListener.checkOrigin(r), true)

    wsListener.allowedOrigins = make([]string, 0)
    assert.Equal(t, wsListener.checkOrigin(r), true)

    wsListener.allowedOrigins = []string{"appfabric.eng.vmware.com:4200"}
    assert.Equal(t, wsListener.checkOrigin(r), false)

    wsListener.allowedOrigins = []string{"appfabric.eng.vmware.com:4200", "localhost:4200"}
    assert.Equal(t, wsListener.checkOrigin(r), true)

    wsListener.allowedOrigins = []string{"appfabric.eng.vmware.com:4200"}
    r.Host = "localhost:4200"
    assert.Equal(t, wsListener.checkOrigin(r), true)

    r.Header["Origin"] = []string{}
    assert.Equal(t, wsListener.checkOrigin(r), true)

    r.Header["Origin"] = []string{"http://192.168.0.%31/"}
    assert.Equal(t, wsListener.checkOrigin(r), false)
}

func TestWebSocketConnectionListener_NewListener(t *testing.T) {

    listener, err := NewWebSocketConnectionListener("", "/fabric", []string{"localhost:8000"}, nil, false)

    assert.Nil(t, err)
    assert.NotNil(t, listener)

    wsListener := listener.(*webSocketConnectionListener)

    wg := sync.WaitGroup{}
    wg.Add(1)

    dialer := &websocket.Dialer{}
    var clientConn *websocket.Conn
    go func() {
        var err error
        clientConn, _, err = dialer.Dial(
            "ws://"+wsListener.tcpConnectionListener.Addr().String()+"/fabric", nil)
        assert.NotNil(t, clientConn)
        assert.Nil(t, err)

        wg.Done()
    }()

    rawConn, err := listener.Accept()
    assert.NotNil(t, rawConn)
    assert.Nil(t, err)

    wg.Wait()

    go func() {
        wsWriter, _ := clientConn.NextWriter(websocket.TextMessage)
        wr := frame.NewWriter(wsWriter)
        wr.Write(frame.New(frame.CONNECT, frame.AcceptVersion, "1.2"))
        wsWriter.Close()
    }()

    f, e := rawConn.ReadFrame()

    assert.NotNil(t, f)
    assert.Nil(t, e)

    verifyFrame(t, f, frame.New(frame.CONNECT, frame.AcceptVersion, "1.2"), true)

    wg.Add(1)

    go func() {
        rawConn.WriteFrame(frame.New(frame.CONNECTED, frame.Version, "1.2"))
        wg.Done()
    }()

    _, wsReader, _ := clientConn.NextReader()
    serverFrame, err := frame.NewReader(wsReader).Read()
    assert.NotNil(t, serverFrame)
    assert.Nil(t, err)

    wg.Wait()

    verifyFrame(t, serverFrame, frame.New(frame.CONNECTED, frame.Version, "1.2"), true)

    rawConn.SetReadDeadline(time.Now().Add(time.Duration(-1) * time.Second))
    _, timeoutErr := rawConn.ReadFrame()

    assert.NotNil(t, timeoutErr)

    rawConn.SetReadDeadline(time.Time{})

    assert.Nil(t, rawConn.Close())

    _, reader, err := clientConn.NextReader()
    assert.Nil(t, reader)
    assert.NotNil(t, err)

    err = rawConn.WriteFrame(frame.New(frame.MESSAGE))
    assert.NotNil(t, err)

    var failedClientConn *websocket.Conn
    wg.Add(1)
    go func() {
        requestHeaders := make(http.Header)
        requestHeaders["Origin"] = []string{"http://192.168.0.%31/"}

        var err error
        failedClientConn, _, err = dialer.Dial(
            "ws://"+wsListener.tcpConnectionListener.Addr().String()+"/fabric", requestHeaders)
        assert.Nil(t, failedClientConn)
        assert.NotNil(t, err)
        wg.Done()
    }()

    failedConn, connErr := listener.Accept()
    assert.Nil(t, failedConn)
    assert.NotNil(t, connErr)

    wg.Wait()

    listener.Close()

    clientConn2, _, err := dialer.Dial(
        "ws://"+wsListener.tcpConnectionListener.Addr().String()+"/fabric", nil)
    assert.Nil(t, clientConn2)
    assert.NotNil(t, err)
}

// mockIPBlockingChecker is a test double that records TrackConnection calls
// and allows pre-configuring blocked IPs.
type mockIPBlockingChecker struct {
    mu         sync.Mutex
    trackedIPs []string
    blockedIPs map[string]string // ip -> reason
}

func (m *mockIPBlockingChecker) IsIPBlocked(ip string) (bool, string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    if reason, ok := m.blockedIPs[ip]; ok {
        return true, reason
    }
    return false, ""
}

func (m *mockIPBlockingChecker) TrackConnection(ip string, sessionID string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.trackedIPs = append(m.trackedIPs, ip)
}

func (m *mockIPBlockingChecker) TrackDisconnection(ip string, sessionID string) {}

func (m *mockIPBlockingChecker) ExtractRealIP(remoteAddr string, headers map[string][]string) string {
    host, _, err := net.SplitHostPort(remoteAddr)
    if err != nil {
        return remoteAddr
    }
    return host
}

func TestListener_TrackConnectionCalledOnInvalidUpgrade(t *testing.T) {
    mock := &mockIPBlockingChecker{blockedIPs: make(map[string]string)}

    router := mux.NewRouter()
    server := httptest.NewServer(router)
    defer server.Close()

    httpServer := &http.Server{Handler: router}

    listener, err := NewWebSocketConnectionFromExistingHttpServer(
        httpServer, router, "/ws", nil, nil, false, nil,
    )
    assert.NoError(t, err)
    assert.NotNil(t, listener)

    // set the checker on the listener
    wsListener := listener.(*webSocketConnectionListener)
    wsListener.SetIPBlockingChecker(mock)

    // send a plain HTTP request (no upgrade headers) — will get 400 but should still track
    resp, err := http.Get(server.URL + "/ws")
    assert.NoError(t, err)
    assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
    resp.Body.Close()

    mock.mu.Lock()
    defer mock.mu.Unlock()
    assert.Len(t, mock.trackedIPs, 1, "TrackConnection should have been called once")
    assert.Equal(t, "127.0.0.1", mock.trackedIPs[0])
}

func TestListener_BlockedIPGets403(t *testing.T) {
    mock := &mockIPBlockingChecker{
        blockedIPs: map[string]string{
            "127.0.0.1": "test block reason",
        },
    }

    router := mux.NewRouter()
    server := httptest.NewServer(router)
    defer server.Close()

    httpServer := &http.Server{Handler: router}

    listener, err := NewWebSocketConnectionFromExistingHttpServer(
        httpServer, router, "/ws", nil, nil, false, nil,
    )
    assert.NoError(t, err)

    wsListener := listener.(*webSocketConnectionListener)
    wsListener.SetIPBlockingChecker(mock)

    // send request — should get 403 because IP is blocked
    resp, err := http.Get(server.URL + "/ws")
    assert.NoError(t, err)
    assert.Equal(t, http.StatusForbidden, resp.StatusCode)
    resp.Body.Close()

    mock.mu.Lock()
    defer mock.mu.Unlock()
    assert.Empty(t, mock.trackedIPs, "blocked IPs should be rejected before tracking")
}

func TestListener_SetIPBlockingCheckerViaServer(t *testing.T) {
    mock := &mockIPBlockingChecker{blockedIPs: make(map[string]string)}

    router := mux.NewRouter()
    httpServer := &http.Server{Handler: router}

    listener, err := NewWebSocketConnectionFromExistingHttpServer(
        httpServer, router, "/ws", nil, nil, false, nil,
    )
    assert.NoError(t, err)

    // create a stomp server with this listener and set checker via server API
    config := NewStompConfig(0, nil)
    stompSrv := NewStompServer(listener, config)
    stompSrv.SetIPBlockingChecker(mock)

    // verify the listener received the checker via the optional interface
    wsListener := listener.(*webSocketConnectionListener)
    got := wsListener.getIPBlockingChecker()
    assert.Equal(t, mock, got, "SetIPBlockingChecker on server should propagate to WebSocket listener")
}
