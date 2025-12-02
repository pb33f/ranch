// Copyright 2025 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: MIT

package ratelimit

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// error response structure returned when rate limit is exceeded
type ErrorResponse struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	Limit      int    `json:"limit"`
	Remaining  int    `json:"remaining"`
	ResetAt    int64  `json:"reset_at"`
	RetryAfter int    `json:"retry_after"`
}

// wraps an http.Handler to enforce rate limits.
// sets standard rate limit headers on all responses and returns 429 with json body when limit is exceeded.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, event := l.Allow(r)

		// always set rate limit headers
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(event.Limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(event.Remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(event.ResetAt.Unix(), 10))

		if !allowed {
			l.handleLimited(w, event)
			return
		}

		// check if approaching limit and fire event
		if l.config.EventHandler != nil {
			warningThreshold := int(float64(event.Limit) * l.config.WarningThreshold)
			if event.Remaining <= warningThreshold {
				l.config.EventHandler.OnApproaching(event)
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (l *Limiter) handleLimited(w http.ResponseWriter, event Event) {
	// fire event handler first so it can do db work, send emails, etc.
	if l.config.EventHandler != nil {
		l.config.EventHandler.OnLimited(event)
	}

	retryAfter := int(time.Until(event.ResetAt).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}

	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)

	response := ErrorResponse{
		Error:      "rate_limit_exceeded",
		Message:    "you have exceeded your rate limit",
		Limit:      event.Limit,
		Remaining:  event.Remaining,
		ResetAt:    event.ResetAt.Unix(),
		RetryAfter: retryAfter,
	}
	json.NewEncoder(w).Encode(response)
}
