// Copyright 2019-2021 VMware, Inc.
// SPDX-License-Identifier: BSD-2-Clause

package server

import (
	"io"
	"net/http"
	"path"
	"path/filepath"
	"sort"

	"github.com/pb33f/ranch/plank/pkg/middleware"
	"github.com/pb33f/ranch/plank/pkg/routing"
	"github.com/pb33f/ranch/plank/utils"
)

// SpaConfig shorthand for SinglePageApplication Config is used to configure routes for your SPAs like
// Angular or React. for example if your app index.html is served at /app and static contents like JS/CSS
// are served from /app/static, BaseUri can be set to /app and StaticAssets to "/app/assets". see config.json
// for details.
type SpaConfig struct {
	RootFolder        string                   `json:"root_folder"`         // location where Plank will serve SPA
	BaseUri           string                   `json:"base_uri"`            // base URI for the SPA
	StaticAssets      []string                 `json:"static_assets"`       // locations for static assets used by the SPA
	ExcludedPrefixes  []string                 `json:"excluded_prefixes"`   // paths that should never be handled by the SPA
	CacheControlRules map[string]string        `json:"cache_control_rules"` // map holding glob pattern - cache-control header value
	FallbackPredicate func(*http.Request) bool `json:"-"`

	cacheControlRulePairs []middleware.CacheControlRulePair
}

// NewSpaConfig takes location to where the SPA content is as an input and returns a sanitized
// instance of *SpaConfig.
func NewSpaConfig(input string) (spaConfig *SpaConfig, err error) {
	p, uri := utils.DeriveStaticURIFromPath(input)
	spaConfig = &SpaConfig{
		RootFolder:            p,
		BaseUri:               uri,
		CacheControlRules:     make(map[string]string),
		cacheControlRulePairs: make([]middleware.CacheControlRulePair, 0),
	}

	spaConfig.CollateCacheControlRules()
	return spaConfig, err
}

// CollateCacheControlRules compiles glob patterns into an ordered rule list, replacing any previously
// compiled rules so it is safe to call more than once. Longer patterns are tried first, so a specific
// rule such as "/assets/**" wins over a catch-all such as "/**"; equal lengths fall back to lexical order.
func (s *SpaConfig) CollateCacheControlRules() {
	patterns := make([]string, 0, len(s.CacheControlRules))
	for globP := range s.CacheControlRules {
		patterns = append(patterns, globP)
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) != len(patterns[j]) {
			return len(patterns[i]) > len(patterns[j])
		}
		return patterns[i] < patterns[j]
	})

	pairs := make([]middleware.CacheControlRulePair, 0, len(patterns))
	for _, globP := range patterns {
		pair, err := middleware.NewCacheControlRulePair(globP, s.CacheControlRules[globP])
		if err != nil {
			continue
		}
		pairs = append(pairs, pair)
	}
	s.cacheControlRulePairs = pairs
}

func (s *SpaConfig) matchCacheControlRule(requestPath string) (string, bool) {
	for _, pair := range s.cacheControlRulePairs {
		if pair.CompiledGlobPattern.Match(requestPath) {
			return pair.CacheControlRule, true
		}
	}
	return "", false
}

// CacheControlMiddleware returns the middleware func to be used in route configuration. Rules match
// against the request path (no query string). The matched header is only sent on successful responses,
// so a missing asset's 404 is never cached by a CDN under a long-lived rule, and a Cache-Control header
// the handler sets itself always wins.
func (s *SpaConfig) CacheControlMiddleware() routing.MiddlewareFunc {
	return s.cacheControlMiddleware(func(r *http.Request) string { return r.URL.Path })
}

// spaCacheControlMiddleware matches extensionless SPA routes as the index file they serve, so a
// navigation such as /assets/foo never picks up a long-lived asset rule for index.html.
func (s *SpaConfig) spaCacheControlMiddleware() routing.MiddlewareFunc {
	indexPath := path.Join("/", s.BaseUri, "index.html")
	return s.cacheControlMiddleware(func(r *http.Request) string {
		if filepath.Ext(r.URL.Path) == "" {
			return indexPath
		}
		return r.URL.Path
	})
}

func (s *SpaConfig) cacheControlMiddleware(matchPath func(*http.Request) string) routing.MiddlewareFunc {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rule, ok := s.matchCacheControlRule(matchPath(r))
			if !ok {
				handler.ServeHTTP(w, r)
				return
			}
			handler.ServeHTTP(&cacheControlResponseWriter{ResponseWriter: w, rule: rule}, r)
		})
	}
}

// cacheControlResponseWriter sets Cache-Control once the final status code is known.
type cacheControlResponseWriter struct {
	http.ResponseWriter
	rule        string
	wroteHeader bool
}

func (w *cacheControlResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader && code >= http.StatusOK {
		w.wroteHeader = true
		switch code {
		case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
			if w.Header().Get("Cache-Control") == "" {
				w.Header().Set("Cache-Control", w.rule)
			}
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// Flush passes through to the underlying writer so streaming handlers keep working.
func (w *cacheControlResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *cacheControlResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// ReadFrom keeps the underlying writer's sendfile fast path used by http.ServeFile.
func (w *cacheControlResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *cacheControlResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
