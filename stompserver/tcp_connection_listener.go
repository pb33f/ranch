// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
    "github.com/go-stomp/stomp/v3/frame"
    "net"
    "time"
)

type tcpStompConnection struct {
    tcpCon net.Conn
}

func (c *tcpStompConnection) ReadFrame() (*frame.Frame, error) {
    frameR := frame.NewReader(c.tcpCon)
    f, e := frameR.Read()
    return f, e
}

func (c *tcpStompConnection) WriteFrame(f *frame.Frame) error {
    frameWr := frame.NewWriter(c.tcpCon)
    err := frameWr.Write(f)
    return err
}

func (c *tcpStompConnection) SetReadDeadline(t time.Time) {
    c.tcpCon.SetReadDeadline(t)
}

func (c *tcpStompConnection) GetRemoteAddr() string {
    return c.tcpCon.RemoteAddr().String()
}

func (c *tcpStompConnection) Close() error {
    return c.tcpCon.Close()
}

type tcpConnectionListener struct {
    listener     net.Listener
    closeChannel chan *Connection
    openChannel  chan *Connection
}

func NewTcpConnectionListener(addr string) (RawConnectionListener, error) {
    tcpListener, err := net.Listen("tcp", addr)
    if err != nil {
        return nil, err
    }
    return &tcpConnectionListener{listener: tcpListener, openChannel: make(chan *Connection), closeChannel: make(chan *Connection)}, nil
}

func (l *tcpConnectionListener) GetConnectionOpenChannel() chan *Connection {
    return l.openChannel
}

func (l *tcpConnectionListener) GetConnectionCloseChannel() chan *Connection {
    return l.closeChannel
}
func (l *tcpConnectionListener) Accept() (RawConnection, error) {
    conn, err := l.listener.Accept()

    if err != nil {
        return nil, err
    }

    return &tcpStompConnection{tcpCon: conn}, nil
}

func (l *tcpConnectionListener) Close() error {
    return l.listener.Close()
}
