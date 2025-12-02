// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
    "fmt"
    "github.com/go-stomp/stomp/v3/frame"
    "github.com/gorilla/mux"
    "github.com/gorilla/websocket"
    "log/slog"
    "net"
    "net/http"
    "net/url"
    "strings"
    "sync"

    "time"
)

// IPBlockingChecker is an interface for checking if an IP should be blocked
type IPBlockingChecker interface {
    IsIPBlocked(ip string) (bool, string) // Returns blocked status and reason
    TrackConnection(ip string, sessionID string)
    TrackDisconnection(ip string, sessionID string)
    ExtractRealIP(remoteAddr string, headers map[string][]string) string
}

type WebSocketStompConnection struct {
    WSCon      *websocket.Conn
    writeMutex sync.Mutex
}

func (c *WebSocketStompConnection) ReadFrame() (*frame.Frame, error) {
    _, r, err := c.WSCon.NextReader()
    if err != nil {
        return nil, err
    }
    frameR := frame.NewReader(r)
    f, e := frameR.Read()
    return f, e
}

func (c *WebSocketStompConnection) WriteFrame(f *frame.Frame) error {
    c.writeMutex.Lock()
    defer c.writeMutex.Unlock()

    wr, err := c.WSCon.NextWriter(websocket.TextMessage)
    if err != nil {
        return err
    }
    frameWr := frame.NewWriter(wr)
    err = frameWr.Write(f)
    if err != nil {
        return err
    }
    err = wr.Close()
    return err
}

func (c *WebSocketStompConnection) SetReadDeadline(t time.Time) {
    c.WSCon.SetReadDeadline(t)
}

func (c *WebSocketStompConnection) GetRemoteAddr() string {
    return c.WSCon.RemoteAddr().String()
}

func (c *WebSocketStompConnection) Close() error {
    return c.WSCon.Close()
}

type webSocketConnectionListener struct {
    httpServer            *http.Server
    requestHandler        *http.ServeMux
    tcpConnectionListener net.Listener
    connectionsChannel    chan RawConnResult
    closeChannel          chan *Connection
    openChannel           chan *Connection
    allowedOrigins        []string
    ipBlockingChecker     IPBlockingChecker // Optional IP blocking checker
}

type RawConnResult struct {
    Conn RawConnection
    Err  error
}

func NewWebSocketConnectionFromExistingHttpServer(httpServer *http.Server, handler *mux.Router,
    endpoint string, allowedOrigins []string, logger *slog.Logger, debug bool, customSocketFunc http.HandlerFunc) (RawConnectionListener, error) {
    l := &webSocketConnectionListener{
        httpServer:         httpServer,
        connectionsChannel: make(chan RawConnResult),
        closeChannel:       make(chan *Connection),
        openChannel:        make(chan *Connection),
        allowedOrigins:     allowedOrigins,
    }

    var upgrader = websocket.Upgrader{
        ReadBufferSize:  1024,
        WriteBufferSize: 1024,
    }

    upgrader.CheckOrigin = l.checkOrigin

    handler.HandleFunc(endpoint, func(writer http.ResponseWriter, request *http.Request) {
        // Check for IP blocking if checker is configured
        if l.ipBlockingChecker != nil {
            realIP := l.ipBlockingChecker.ExtractRealIP(request.RemoteAddr, request.Header)
            if blocked, reason := l.ipBlockingChecker.IsIPBlocked(realIP); blocked {
                if logger != nil {
                    logger.Warn(fmt.Sprintf(LogBlockedIPAttempted, realIP, reason))
                }
                // IMPORTANT: We now allow blocked IPs to connect so they can receive
                // a STOMP ERROR frame. The STOMP server will handle sending the ERROR
                // frame and closing the connection immediately after.
                // This is less efficient but allows the UI to receive the error message.
            } else {
                // Track the connection only if not blocked
                sessionID := request.Header.Get(HeaderXSessionID)
                if sessionID == "" {
                    if cookie, err := request.Cookie(CookieNameSession); err == nil {
                        sessionID = cookie.Value
                    }
                }
                l.ipBlockingChecker.TrackConnection(realIP, sessionID)
            }
        }

        if debug {
            if logger != nil {
                logger.Info(fmt.Sprintf(LogWebSocketConnection, request.RemoteAddr))
            }
        }
        if !strings.Contains(request.Header.Get(HeaderConnection), HeaderUpgrade) ||
            request.Header.Get(HeaderUpgrade) != HeaderUpgradeValue {
            writer.WriteHeader(http.StatusBadRequest)
            if debug {
                if logger != nil {
                    logger.Warn(fmt.Sprintf(LogWebSocketFailed, request.RemoteAddr))
                }
            }
            return
        }

        upgrader.Subprotocols = websocket.Subprotocols(request)
        conn, err := upgrader.Upgrade(writer, request, nil)
        if err != nil {
            l.connectionsChannel <- RawConnResult{Err: err}
            return
        }

        wsConn := &WebSocketStompConnection{
            WSCon: conn,
        }

        conn.SetCloseHandler(func(code int, text string) error {
            if debug {
                if logger != nil {
                    logger.Info(fmt.Sprintf(LogWebSocketClosed, request.RemoteAddr))
                }
            }
            // Track disconnection if IP blocker is configured
            if l.ipBlockingChecker != nil {
                realIP := l.ipBlockingChecker.ExtractRealIP(request.RemoteAddr, request.Header)
                sessionID := request.Header.Get(HeaderXSessionID)
                if sessionID == "" {
                    if cookie, err := request.Cookie(CookieNameSession); err == nil {
                        sessionID = cookie.Value
                    }
                }
                l.ipBlockingChecker.TrackDisconnection(realIP, sessionID)
            }
            l.closeChannel <- &Connection{
                Source: request.RemoteAddr,
            }
            return nil
        })

        go func() {
            l.connectionsChannel <- RawConnResult{
                Conn: wsConn,
            }
            l.openChannel <- &Connection{
                Source: request.RemoteAddr,
            }
        }()

        if customSocketFunc != nil {
            customSocketFunc.ServeHTTP(writer, request)
        }

    })

    return l, nil
}

func NewWebSocketConnectionListener(addr string, endpoint string, allowedOrigins []string, logger *slog.Logger, debug bool) (RawConnectionListener, error) {
    rh := http.NewServeMux()
    l := &webSocketConnectionListener{
        requestHandler: rh,
        httpServer: &http.Server{
            Addr:    addr,
            Handler: rh,
        },
        connectionsChannel: make(chan RawConnResult),
        allowedOrigins:     allowedOrigins,
    }

    var upgrader = websocket.Upgrader{
        ReadBufferSize:  1024,
        WriteBufferSize: 1024,
    }

    upgrader.CheckOrigin = l.checkOrigin

    rh.HandleFunc(endpoint, func(writer http.ResponseWriter, request *http.Request) {
        if debug {
            if logger != nil {
                logger.Info(fmt.Sprintf("websocket connection from: %s", request.RemoteAddr))
            }
        }
        if request.Header.Get("Connection") != "Upgrade" ||
            request.Header.Get("Upgrade") != "websocket" {
            writer.WriteHeader(http.StatusBadRequest)
            if debug {
                if logger != nil {
                    logger.Warn(fmt.Sprintf("failed websocket connection from: %s", request.RemoteAddr))
                }
            }
            return
        }

        upgrader.Subprotocols = websocket.Subprotocols(request)
        conn, err := upgrader.Upgrade(writer, request, nil)
        if err != nil {
            l.connectionsChannel <- RawConnResult{Err: err}

        } else {
            l.connectionsChannel <- RawConnResult{
                Conn: &WebSocketStompConnection{
                    WSCon: conn,
                },
            }
        }
    })

    var err error
    l.tcpConnectionListener, err = net.Listen("tcp", addr)
    if err != nil {
        return nil, err
    }

    go l.httpServer.Serve(l.tcpConnectionListener)
    return l, nil
}

func (l *webSocketConnectionListener) GetConnectionOpenChannel() chan *Connection {
    return l.openChannel
}

func (l *webSocketConnectionListener) GetConnectionCloseChannel() chan *Connection {
    return l.closeChannel
}

func (l *webSocketConnectionListener) checkOrigin(r *http.Request) bool {
    if len(l.allowedOrigins) == 0 {
        return true
    }

    origin := r.Header["Origin"]
    if len(origin) == 0 {
        return true
    }
    u, err := url.Parse(origin[0])
    if err != nil {
        return false
    }
    if strings.ToLower(u.Host) == strings.ToLower(r.Host) {
        return true
    }

    for _, allowedOrigin := range l.allowedOrigins {
        if strings.ToLower(u.Host) == strings.ToLower(allowedOrigin) {
            return true
        }
    }

    return false
}

func (l *webSocketConnectionListener) Accept() (RawConnection, error) {
    cr := <-l.connectionsChannel
    return cr.Conn, cr.Err
}

func (l *webSocketConnectionListener) Close() error {
    return l.httpServer.Close()
}

// SetIPBlockingChecker sets the IP blocking checker for this listener
func (l *webSocketConnectionListener) SetIPBlockingChecker(checker IPBlockingChecker) {
    l.ipBlockingChecker = checker
}
