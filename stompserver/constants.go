// Copyright 2025 Princess B33f Heavy Industries / Dave Shanley
// SPDX-License-Identifier: BSD-2-Clause

package stompserver

// HTTP Headers
const (
	HeaderXSessionID     = "X-Session-Id"
	HeaderConnection     = "Connection"
	HeaderUpgrade        = "Upgrade"
	HeaderUpgradeValue   = "websocket"
	CookieNameSession    = "session"
)

// HTTP Status Messages
const (
	StatusMessageTooManyRequests = "Too many connection attempts. Please try again later."
)

// Log Messages
const (
	LogBlockedIPAttempted = "[ranch] blocked IP attempted connection: %s (reason: %s)"
	LogWebSocketConnection = "[ranch] websocket connection from: %s"
	LogWebSocketClosed = "[ranch] websocket connection from: %s has been closed"
	LogWebSocketFailed = "[ranch] failed websocket connection from: %s"
)