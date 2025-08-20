// Copyright 2019-2020 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

import (
    "github.com/go-stomp/stomp/v3/frame"
    "time"
)

type Connection struct {
    Source string
}

type RawConnection interface {
    // ReadFrame Reads a single frame object
    ReadFrame() (*frame.Frame, error)
    // WriteFrame Sends a single frame object
    WriteFrame(frame *frame.Frame) error
    // SetReadDeadline Set deadline for reading frames
    SetReadDeadline(t time.Time)
    // GetRemoteAddr Returns the remote address of the connection
    GetRemoteAddr() string
    // Close the connection
    Close() error
}

type RawConnectionListener interface {
    // Accept Blocks until a new RawConnection is established.
    Accept() (RawConnection, error)
    // Close Stops the connection listener.
    Close() error

    // GetConnectionOpenChannel will return a channel that emits connection results when clients connect.
    GetConnectionOpenChannel() chan *Connection

    // GetConnectionCloseChannel will return a channel the emits connection result when clients disconnect.
    GetConnectionCloseChannel() chan *Connection
}
