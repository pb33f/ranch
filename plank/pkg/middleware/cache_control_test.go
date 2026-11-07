// Copyright 2026 Princess Beef Heavy Industries, LLC / Dave Shanley
// SPDX-License-Identifier: BSD-2-Clause

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCacheControlMiddlewareServesUnmatchedRequestsOnce(t *testing.T) {
	calls := 0
	handler := CacheControlMiddleware([]string{"/assets/**"}, NewCacheControlDirective().Public())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
		}))

	rsp := httptest.NewRecorder()
	handler.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/other", nil))

	assert.Equal(t, 1, calls)
	assert.Empty(t, rsp.Header().Get("Cache-Control"))
}

func TestCacheControlMiddlewareSetsHeaderOnMatch(t *testing.T) {
	handler := CacheControlMiddleware([]string{"/assets/**"}, NewCacheControlDirective().Public())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rsp := httptest.NewRecorder()
	handler.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	assert.Equal(t, "public", rsp.Header().Get("Cache-Control"))
}
