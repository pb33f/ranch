// Copyright 2025 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: MIT

package ratelimit

import "net/http"

// ExtractIP gets client ip from request, checking X-Forwarded-For, X-Real-IP, then RemoteAddr
func ExtractIP(r *http.Request) string {
	// check x-forwarded-for first (from proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// take first ip in chain
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}

	// check x-real-ip
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// fallback to remote addr
	return r.RemoteAddr
}
