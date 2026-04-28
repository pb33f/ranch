// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package middleware

import (
	"net/http"

	"github.com/pb33f/ranch/plank/pkg/routing"
)

func BasicSecurityHeaderMiddleware() routing.MiddlewareFunc {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Frame-Options", "allow-from https://pb33f.io/")
			handler.ServeHTTP(w, r)
		})
	}
}
