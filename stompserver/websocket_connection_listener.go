// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
	"errors"
	"fmt"
	"github.com/go-stomp/stomp/v3/frame"
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
	IsIPBlocked(ip string) (bool, string) // returns blocked status and reason
	TrackConnection(ip string, sessionID string)
	TrackDisconnection(ip string, sessionID string)
	ExtractRealIP(remoteAddr string, headers map[string][]string) string
}

// WebSocketStompConnection adapts a WebSocket to the RawConnection interface.
type WebSocketStompConnection struct {
	WSCon      *websocket.Conn
	writeMutex sync.Mutex
}

// ReadFrame reads a single STOMP frame from the WebSocket.
func (c *WebSocketStompConnection) ReadFrame() (*frame.Frame, error) {
	_, r, err := c.WSCon.NextReader()
	if err != nil {
		return nil, err
	}
	frameR := frame.NewReader(r)
	f, e := frameR.Read()
	return f, e
}

// WriteFrame writes a single STOMP frame to the WebSocket.
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

// SetReadDeadline sets the read deadline on the underlying WebSocket.
func (c *WebSocketStompConnection) SetReadDeadline(t time.Time) {
	_ = c.WSCon.SetReadDeadline(t)
}

// GetRemoteAddr returns the remote network address.
func (c *WebSocketStompConnection) GetRemoteAddr() string {
	return c.WSCon.RemoteAddr().String()
}

// Close closes the underlying WebSocket.
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
	ipBlockingChecker     IPBlockingChecker
	ipBlockingCheckerMu   sync.RWMutex
	blockedIPLogged       sync.Map // suppresses repeated log lines for the same blocked IP
}

// RawConnResult carries an accepted raw connection or the accept error.
type RawConnResult struct {
	Conn RawConnection
	Err  error
}

// HTTPHandlerRegistrar is the subset of HTTP mux APIs needed to register a WebSocket endpoint.
type HTTPHandlerRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

type websocketHandlerOptions struct {
	logger           *slog.Logger
	debug            bool
	customSocketFunc http.HandlerFunc
	emitLifecycle    bool
	asyncAccept      bool
}

// NewWebSocketConnectionFromExistingHttpServer registers a WebSocket STOMP endpoint on an existing HTTP server.
func NewWebSocketConnectionFromExistingHttpServer(httpServer *http.Server, handler HTTPHandlerRegistrar,
	endpoint string, allowedOrigins []string, logger *slog.Logger, debug bool, customSocketFunc http.HandlerFunc) (RawConnectionListener, error) {
	l := &webSocketConnectionListener{
		httpServer:         httpServer,
		connectionsChannel: make(chan RawConnResult),
		closeChannel:       make(chan *Connection),
		openChannel:        make(chan *Connection),
		allowedOrigins:     allowedOrigins,
	}

	l.registerHandler(handler, endpoint, websocketHandlerOptions{
		logger:           logger,
		debug:            debug,
		customSocketFunc: customSocketFunc,
		emitLifecycle:    true,
		asyncAccept:      true,
	})

	return l, nil
}

// NewWebSocketConnectionListener creates an HTTP server that accepts WebSocket STOMP connections.
func NewWebSocketConnectionListener(addr string, endpoint string, allowedOrigins []string, logger *slog.Logger, debug bool) (RawConnectionListener, error) {
	if logger == nil {
		logger = slog.Default()
	}
	rh := http.NewServeMux()
	l := &webSocketConnectionListener{
		requestHandler: rh,
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           rh,
			ReadHeaderTimeout: 5 * time.Second,
		},
		connectionsChannel: make(chan RawConnResult),
		allowedOrigins:     allowedOrigins,
	}

	l.registerHandler(rh, endpoint, websocketHandlerOptions{
		logger: logger,
		debug:  debug,
	})

	var err error
	l.tcpConnectionListener, err = net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	go func() {
		if err := l.httpServer.Serve(l.tcpConnectionListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("websocket listener stopped", "err", err)
		}
	}()
	return l, nil
}

func (l *webSocketConnectionListener) registerHandler(handler HTTPHandlerRegistrar, endpoint string, opts websocketHandlerOptions) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	upgrader.CheckOrigin = l.checkOrigin
	handler.HandleFunc(endpoint, l.websocketHandler(&upgrader, opts))
}

func (l *webSocketConnectionListener) websocketHandler(upgrader *websocket.Upgrader, opts websocketHandlerOptions) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !l.allowRequest(writer, request, opts.logger) {
			return
		}

		if opts.debug && opts.logger != nil {
			opts.logger.Info(fmt.Sprintf(LogWebSocketConnection, request.RemoteAddr))
		}
		if !isWebSocketUpgrade(request) {
			writer.WriteHeader(http.StatusBadRequest)
			if opts.debug && opts.logger != nil {
				opts.logger.Warn(fmt.Sprintf(LogWebSocketFailed, request.RemoteAddr))
			}
			return
		}

		upgrader.Subprotocols = websocket.Subprotocols(request)
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			l.connectionsChannel <- RawConnResult{Err: err}
			return
		}

		l.configureCloseHandler(conn, request, opts)
		accept := func() {
			l.connectionsChannel <- RawConnResult{
				Conn: &WebSocketStompConnection{WSCon: conn},
			}
			if opts.emitLifecycle {
				l.openChannel <- &Connection{Source: request.RemoteAddr}
			}
		}
		if opts.asyncAccept {
			go accept()
		} else {
			accept()
		}

		if opts.customSocketFunc != nil {
			opts.customSocketFunc.ServeHTTP(writer, request)
		}
	}
}

func (l *webSocketConnectionListener) allowRequest(writer http.ResponseWriter, request *http.Request, logger *slog.Logger) bool {
	checker := l.getIPBlockingChecker()
	if checker == nil {
		return true
	}

	realIP := checker.ExtractRealIP(request.RemoteAddr, request.Header)
	if blocked, reason := checker.IsIPBlocked(realIP); blocked {
		if logger != nil {
			if _, alreadyLogged := l.blockedIPLogged.LoadOrStore(realIP, true); !alreadyLogged {
				logger.Warn(fmt.Sprintf(LogBlockedIPAttempted, realIP, reason))
			}
		}
		writer.WriteHeader(http.StatusForbidden)
		return false
	}

	l.blockedIPLogged.Delete(realIP)
	checker.TrackConnection(realIP, extractSessionID(request))
	return true
}

func (l *webSocketConnectionListener) configureCloseHandler(
	conn *websocket.Conn, request *http.Request, opts websocketHandlerOptions) {
	conn.SetCloseHandler(func(code int, text string) error {
		if opts.debug && opts.logger != nil {
			opts.logger.Info(fmt.Sprintf(LogWebSocketClosed, request.RemoteAddr))
		}
		if closeChecker := l.getIPBlockingChecker(); closeChecker != nil {
			realIP := closeChecker.ExtractRealIP(request.RemoteAddr, request.Header)
			closeChecker.TrackDisconnection(realIP, extractSessionID(request))
		}
		if opts.emitLifecycle {
			l.closeChannel <- &Connection{Source: request.RemoteAddr}
		}
		return nil
	})
}

func isWebSocketUpgrade(request *http.Request) bool {
	return strings.Contains(request.Header.Get(HeaderConnection), HeaderUpgrade) &&
		request.Header.Get(HeaderUpgrade) == HeaderUpgradeValue
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
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}

	for _, allowedOrigin := range l.allowedOrigins {
		if strings.EqualFold(u.Host, allowedOrigin) {
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

// extractSessionID extracts a session ID from the request header or cookie.
func extractSessionID(r *http.Request) string {
	if id := r.Header.Get(HeaderXSessionID); id != "" {
		return id
	}
	if cookie, err := r.Cookie(CookieNameSession); err == nil {
		return cookie.Value
	}
	return ""
}

func (l *webSocketConnectionListener) getIPBlockingChecker() IPBlockingChecker {
	l.ipBlockingCheckerMu.RLock()
	defer l.ipBlockingCheckerMu.RUnlock()
	return l.ipBlockingChecker
}

// SetIPBlockingChecker sets the IP blocking checker for this listener
func (l *webSocketConnectionListener) SetIPBlockingChecker(checker IPBlockingChecker) {
	l.ipBlockingCheckerMu.Lock()
	defer l.ipBlockingCheckerMu.Unlock()
	l.ipBlockingChecker = checker
}
