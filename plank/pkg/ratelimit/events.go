// Copyright 2025 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: MIT

package ratelimit

import (
	"net/http"
	"time"
)

type Event struct {
	Key       string
	KeyType   string // "session", "apikey", "ip"
	Tier      Tier
	Path      string
	Method    string
	Timestamp time.Time

	// rate limit state
	Allowed   bool
	Remaining int
	Limit     int
	ResetAt   time.Time

	// request context for custom handlers
	RemoteAddr string
	UserAgent  string
}

func NewEvent(r *http.Request, key, keyType string, tier Tier, allowed bool, remaining, limit int, resetAt time.Time) Event {
	return Event{
		Key:        key,
		KeyType:    keyType,
		Tier:       tier,
		Path:       r.URL.Path,
		Method:     r.Method,
		Timestamp:  time.Now(),
		Allowed:    allowed,
		Remaining:  remaining,
		Limit:      limit,
		ResetAt:    resetAt,
		RemoteAddr: r.RemoteAddr,
		UserAgent:  r.UserAgent(),
	}
}

// EventHandler receives notifications on rate limit events.
// implementations can log, send emails, update databases, etc.
type EventHandler interface {
	OnLimited(event Event)     // called when request is rate limited (before 429 sent)
	OnApproaching(event Event) // called when usage crosses warning threshold
	OnBlocked(event Event)     // called for escalating blocks (future feature)
}

type NoopEventHandler struct{}

func (n *NoopEventHandler) OnLimited(event Event)     {}
func (n *NoopEventHandler) OnApproaching(event Event) {}
func (n *NoopEventHandler) OnBlocked(event Event)     {}
