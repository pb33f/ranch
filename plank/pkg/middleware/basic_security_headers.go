// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package middleware

import (
    "github.com/gorilla/mux"
    "net/http"
)

func BasicSecurityHeaderMiddleware() mux.MiddlewareFunc {
    return func(handler http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            handler.ServeHTTP(w, r)
        })
    }
}
