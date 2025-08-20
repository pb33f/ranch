// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
    "fmt"
    "github.com/go-stomp/stomp/v3"
    "github.com/go-stomp/stomp/v3/frame"
    "github.com/google/uuid"
    "log"
    "strconv"
    "strings"
    "sync"
    "sync/atomic"
    "time"
)

type MiddlewareRegistry map[string][]MiddlewareFunc

// FrameHandlerFunc is a function that processes a STOMP frame.
type FrameHandlerFunc func(conn StompConn, f *frame.Frame) error

// MiddlewareFunc is a function that wraps a FrameHandlerFunc.
type MiddlewareFunc func(FrameHandlerFunc) FrameHandlerFunc

type Subscription struct {
    id          string
    destination string
}

// ChainMiddleware applies the list of middleware in order so that the first in the
// slice is the outermost middleware.
func ChainMiddleware(mws []MiddlewareFunc, final FrameHandlerFunc) FrameHandlerFunc {
    // Apply middleware in reverse order (the first middleware becomes the outermost)
    for i := len(mws) - 1; i >= 0; i-- {
        final = mws[i](final)
    }
    return final
}

// ChainCommandMiddleware returns a FrameHandlerFunc that wraps the provided core handler with
// both global middleware (key "*") and command-specific middleware.
func ChainCommandMiddleware(reg MiddlewareRegistry, command string, coreHandler FrameHandlerFunc) FrameHandlerFunc {
    // Start with any global middleware.
    var middlewareChain []MiddlewareFunc
    if global, ok := reg["*"]; ok {
        middlewareChain = append(middlewareChain, global...)
    }
    // Append command-specific middleware.
    if cmdMiddleware, ok := reg[command]; ok {
        middlewareChain = append(middlewareChain, cmdMiddleware...)
    }
    // Chain them.
    return ChainMiddleware(middlewareChain, coreHandler)
}

// AuthzMiddleware returns a MiddlewareFunc that performs an authorization check.
// It expects that the frame has a Destination header. The `action` is a label (e.g., "send" or "subscribe")
// used for logging or error messages.
func AuthzMiddleware(action string) MiddlewareFunc {
    return func(next FrameHandlerFunc) FrameHandlerFunc {
        return func(conn StompConn, f *frame.Frame) error {
            // Extract the destination from the frame.
            //dest, ok := f.Header.Contains(frame.Destination)
            //if !ok {
            //    err := fmt.Errorf("destination not provided in frame")
            //    conn.sendError(err)
            //    return err
            //}
            //// Use the connection's AuthorizeAction method.
            //if err := conn.AuthorizeAction(conn, action, dest); err != nil {
            //    conn.sendError(err)
            //    return err
            //}
            //// If authorized, continue with the next handler.
            return next(conn, f)
        }
    }
}

// AuthInfo holds authentication details for the user.
type AuthInfo struct {
    Username string
    Id       int
    Roles    []string
}

// HasRole returns true if the user has the specified role.
func (a *AuthInfo) HasRole(role string) bool {
    for _, r := range a.Roles {
        if r == role {
            return true
        }
    }
    return false
}

type StompConn interface {
    // Return unique connection Id string
    GetId() string
    // Return IP address of the connection
    GetIPAddress() string
    SendFrameToSubscription(f *frame.Frame, sub *Subscription)
    Close()
    GetSubscriptions() map[string]*Subscription
    GetEventsChannel() chan *ConnEvent
    SendError(err error)
    SendMessage(msg string)
}

const (
    maxHeartBeatDuration = time.Duration(999999999) * time.Millisecond
)

const (
    connecting int32 = iota
    connected
    closed
)

type stompConn struct {
    rawConnection    RawConnection
    state            int32
    version          stomp.Version
    inFrames         chan *frame.Frame
    outFrames        chan *frame.Frame
    readTimeoutMs    int64
    writeTimeout     time.Duration
    id               string
    ipAddress        string
    events           chan *ConnEvent
    config           StompConfig
    subscriptions    map[string]*Subscription
    currentMessageId uint64
    closeOnce        sync.Once
    authInfo         *AuthInfo
}

func NewStompConn(rawConnection RawConnection, config StompConfig, events chan *ConnEvent) StompConn {
    conn := &stompConn{
        rawConnection: rawConnection,
        state:         connecting,
        inFrames:      make(chan *frame.Frame, 32),
        outFrames:     make(chan *frame.Frame, 32),
        config:        config,
        id:            uuid.New().String(),
        ipAddress:     rawConnection.GetRemoteAddr(),
        events:        events,
        subscriptions: make(map[string]*Subscription),
    }

    go conn.run()
    go conn.readInFrames()

    return conn
}

func (conn *stompConn) GetSubscriptions() map[string]*Subscription {
    return conn.subscriptions
}

func (conn *stompConn) GetEventsChannel() chan *ConnEvent {
    return conn.events
}

func (conn *stompConn) SendFrameToSubscription(f *frame.Frame, sub *Subscription) {
    f.Header.Add(frame.Subscription, sub.id)
    conn.outFrames <- f
}

func (conn *stompConn) Close() {
    conn.closeOnce.Do(func() {
        atomic.StoreInt32(&conn.state, closed)
        conn.rawConnection.Close()

        conn.events <- &ConnEvent{
            ConnId:    conn.GetId(),
            eventType: ConnectionClosed,
            conn:      conn,
        }
    })
}

func (conn *stompConn) GetId() string {
    return conn.id
}

func (conn *stompConn) GetIPAddress() string {
    return conn.ipAddress
}

func (conn *stompConn) run() {
    defer conn.Close()

    var timerChannel <-chan time.Time
    var timer *time.Timer

    for {

        if atomic.LoadInt32(&conn.state) == closed {
            return
        }

        if timer == nil && conn.writeTimeout > 0 {
            timer = time.NewTimer(conn.writeTimeout)
            timerChannel = timer.C
        }

        select {
        case f, ok := <-conn.outFrames:
            if !ok {
                // close connection
                return
            }

            // reset heart-beat timer
            if timer != nil {
                timer.Stop()
                timer = nil
            }

            conn.populateMessageIdHeader(f)

            // write the frame to the client
            err := conn.rawConnection.WriteFrame(f)
            if err != nil || f.Command == frame.ERROR {
                return
            }

        case f, ok := <-conn.inFrames:
            if !ok {
                return
            }

            if err := conn.handleIncomingFrame(f); err != nil {
                conn.SendError(err)
                return
            }

        case _ = <-timerChannel:
            // write a heart-beat
            err := conn.rawConnection.WriteFrame(nil)
            if err != nil {
                return
            }
            if timer != nil {
                timer.Stop()
                timer = nil
            }
        }
    }
}

func (conn *stompConn) handleIncomingFrame(f *frame.Frame) error {
    switch f.Command {

    case frame.CONNECT, frame.STOMP:
        return conn.handleConnect(f)

    case frame.DISCONNECT:
        return conn.handleDisconnect(f)

    case frame.SEND:
        return conn.handleSend(f)

    case frame.SUBSCRIBE:
        return conn.handleSubscribe(f)

    case frame.UNSUBSCRIBE:
        return conn.handleUnsubscribe(f)
    }

    return unsupportedStompCommandError
}

// Returns true if the frame contains ANY of the specified
// headers
func containsHeader(f *frame.Frame, headers ...string) bool {
    for _, h := range headers {
        if _, ok := f.Header.Contains(h); ok {
            return true
        }
    }
    return false
}

func (conn *stompConn) handleConnect(f *frame.Frame) error {
    if atomic.LoadInt32(&conn.state) == connected {
        return unexpectedStompCommandError
    }

    if containsHeader(f, frame.Receipt) {
        return invalidHeaderError
    }

    var err error
    conn.version, err = determineVersion(f)
    if err != nil {
        log.Println("cannot determine version")
        return err
    }

    if conn.version == stomp.V10 {
        return unsupportedStompVersionError
    }

    cxDuration, cyDuration, err := getHeartBeat(f)
    if err != nil {
        log.Println("invalid heart-beat")
        return err
    }

    min := time.Duration(conn.config.HeartBeat()) * time.Millisecond
    if min > maxHeartBeatDuration {
        min = maxHeartBeatDuration
    }

    // apply a minimum heartbeat
    if cxDuration > 0 {
        if min == 0 || cxDuration < min {
            cxDuration = min
        }
    }
    if cyDuration > 0 {
        if min == 0 || cyDuration < min {
            cyDuration = min
        }
    }

    conn.writeTimeout = cyDuration

    cx, cy := int64(cxDuration/time.Millisecond), int64(cyDuration/time.Millisecond)
    atomic.StoreInt64(&conn.readTimeoutMs, cx)

    response := frame.New(frame.CONNECTED,
        frame.Version, string(conn.version),
        frame.Server, "pb33f-ranch/0.0.1",
        frame.HeartBeat, fmt.Sprintf("%d,%d", cy, cx))

    err = conn.rawConnection.WriteFrame(response)
    if err != nil {
        return err
    }

    atomic.StoreInt32(&conn.state, connected)

    conn.events <- &ConnEvent{
        ConnId:    conn.GetId(),
        eventType: ConnectionEstablished,
        conn:      conn,
    }

    return nil
}

func (conn *stompConn) handleDisconnect(f *frame.Frame) error {
    if atomic.LoadInt32(&conn.state) == connecting {
        return notConnectedStompError
    }

    conn.sendReceiptResponse(f)
    conn.Close()

    return nil
}

func (conn *stompConn) handleSubscribe(f *frame.Frame) error {
    switch atomic.LoadInt32(&conn.state) {
    case connecting:
        return notConnectedStompError
    case closed:
        return nil
    }

    subId, ok := f.Header.Contains(frame.Id)
    if !ok {
        return invalidSubscriptionError
    }

    dest, ok := f.Header.Contains(frame.Destination)
    if !ok {
        return invalidFrameError
    }

    // Define the core Subscription handler.
    coreSubscribeHandler := func(conn StompConn, f *frame.Frame) error {
        subs := conn.GetSubscriptions()
        if _, exists := subs[subId]; exists {
            // Subscription already exists; nothing more to do.
            return nil
        }

        subs[subId] = &Subscription{
            id:          subId,
            destination: dest,
        }
        evts := conn.GetEventsChannel()
        evts <- &ConnEvent{
            ConnId:      conn.GetId(),
            eventType:   SubscribeToTopic,
            destination: dest,
            conn:        conn,
            sub:         subs[subId],
            frame:       f,
        }
        return nil
    }

    // Retrieve the middleware registry from the config.
    registry := conn.config.GetMiddlewareRegistry()
    handler := ChainCommandMiddleware(registry, frame.SUBSCRIBE, coreSubscribeHandler)
    return handler(conn, f)
}

func (conn *stompConn) handleUnsubscribe(f *frame.Frame) error {
    switch atomic.LoadInt32(&conn.state) {
    case connecting:
        return notConnectedStompError
    case closed:
        return nil
    }

    id, ok := f.Header.Contains(frame.Id)
    if !ok {
        return invalidSubscriptionError
    }

    conn.sendReceiptResponse(f)

    sub, ok := conn.subscriptions[id]
    if !ok {
        // Subscription already removed
        return nil
    }

    // remove the Subscription
    delete(conn.subscriptions, id)

    conn.events <- &ConnEvent{
        ConnId:      conn.GetId(),
        eventType:   UnsubscribeFromTopic,
        conn:        conn,
        sub:         sub,
        destination: sub.destination,
    }

    return nil
}

func (conn *stompConn) handleSend(f *frame.Frame) error {
    switch atomic.LoadInt32(&conn.state) {
    case connecting:
        return notConnectedStompError
    case closed:
        return nil
    }

    // TODO: Remove if we start supporting transactions
    if containsHeader(f, frame.Transaction) {
        return unsupportedStompCommandError
    }

    // no destination triggers an error
    dest, ok := f.Header.Contains(frame.Destination)
    if !ok {
        return invalidFrameError
    }

    // reject SENDing directly to non-request channels by clients
    if !conn.config.IsAppRequestDestination(f.Header.Get(frame.Destination)) {
        return invalidSendDestinationError
    }

    err := conn.sendReceiptResponse(f)
    if err != nil {
        return err
    }

    f.Command = frame.MESSAGE
    conn.events <- &ConnEvent{
        ConnId:      conn.GetId(),
        eventType:   IncomingMessage,
        destination: dest,
        frame:       f,
        conn:        conn,
    }

    return nil
}

func (conn *stompConn) sendReceiptResponse(f *frame.Frame) error {
    if receipt, ok := f.Header.Contains(frame.Receipt); ok {
        f.Header.Del(frame.Receipt)
        return conn.rawConnection.WriteFrame(frame.New(frame.RECEIPT, frame.ReceiptId, receipt))
    }
    return nil
}

func (conn *stompConn) readInFrames() {
    defer func() {
        close(conn.inFrames)
    }()

    // we never close the connection, even if the heartbeating is inaccurate.
    infiniteTimeout := time.Time{}
    for {
        conn.rawConnection.SetReadDeadline(infiniteTimeout)
        f, err := conn.rawConnection.ReadFrame()
        if err != nil {
            return
        }

        if f == nil {
            // heartbeat frame
            continue
        }

        conn.inFrames <- f
    }
}

func determineVersion(f *frame.Frame) (stomp.Version, error) {
    if acceptVersion, ok := f.Header.Contains(frame.AcceptVersion); ok {
        versions := strings.Split(acceptVersion, ",")
        for _, supportedVersion := range []stomp.Version{stomp.V12, stomp.V11, stomp.V10} {
            for _, v := range versions {
                if v == supportedVersion.String() {
                    // return the highest supported version
                    return supportedVersion, nil
                }
            }
        }
    } else {
        return stomp.V10, nil
    }

    var emptyVersion stomp.Version
    return emptyVersion, unsupportedStompVersionError
}

func getHeartBeat(f *frame.Frame) (cx, cy time.Duration, err error) {
    if heartBeat, ok := f.Header.Contains(frame.HeartBeat); ok {
        return frame.ParseHeartBeat(heartBeat)
    }
    return 0, 0, nil
}

func (conn *stompConn) SendError(err error) {
    errorFrame := frame.New(frame.ERROR,
        frame.Message, err.Error())

    conn.rawConnection.WriteFrame(errorFrame)
}

func (conn *stompConn) SendMessage(message string) {
    msgFrame := frame.New(frame.MESSAGE, frame.Message, message)
    conn.rawConnection.WriteFrame(msgFrame)
}

func (conn *stompConn) populateMessageIdHeader(f *frame.Frame) {
    if f.Command == frame.MESSAGE {
        // allocate the value of message-id for this frame
        conn.currentMessageId++
        messageId := strconv.FormatUint(conn.currentMessageId, 10)
        f.Header.Set(frame.MessageId, messageId)
        // remove the Ack header (if any) as we don't support those
        f.Header.Del(frame.Ack)
    }
}
