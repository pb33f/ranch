package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSPAFallbackHonorsExcludedPrefixesAndNavigationChecks(t *testing.T) {
	tempDir := t.TempDir()
	assert.NoError(t, os.WriteFile(filepath.Join(tempDir, "index.html"), []byte("spa-index"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(tempDir, "app.js"), []byte("console.log('ok')"), 0o600))

	cfg := &PlatformServerConfig{
		RootDir:           tempDir,
		Host:              "localhost",
		Port:              0,
		ShutdownTimeout:   time.Minute,
		RestBridgeTimeout: time.Minute,
		SpaConfig: &SpaConfig{
			RootFolder:        tempDir,
			BaseUri:           "/",
			ExcludedPrefixes:  []string{"/api"},
			FallbackPredicate: BrowserNavigationFallback(),
		},
		DefaultNotFoundHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "root 404", http.StatusNotFound)
		}),
		RouteErrorPolicies: []*RouteErrorPolicy{
			{
				PathPrefix: "/api",
				NotFoundHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "api 404", http.StatusNotFound)
				}),
			},
			{
				PathPrefix: "/workspaces",
				NotFoundHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "workspace 404", http.StatusNotFound)
				}),
			},
		},
	}

	ps := NewPlatformServer(cfg).(*platformServer)
	ps.configureSPA()

	t.Run("browser navigation gets index", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		req.Header.Set("Accept", "text/html")
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusOK, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "spa-index")
	})

	t.Run("root assets still resolve from spa root", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusOK, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "console.log")
	})

	t.Run("excluded api prefixes do not fall into the spa", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/missing", nil)
		req.Header.Set("Accept", "text/html")
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "api 404")
	})

	t.Run("route specific 404 wins before spa fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/workspaces/missing", nil)
		req.Header.Set("Accept", "text/html")
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "workspace 404")
	})

	t.Run("non navigation requests get the default 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		req.Header.Set("Accept", "application/json")
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "root 404")
	})
}

func TestRouteErrorPoliciesUseLongestPrefixFor404And405(t *testing.T) {
	cfg := &PlatformServerConfig{
		RootDir:           t.TempDir(),
		Host:              "localhost",
		Port:              0,
		ShutdownTimeout:   time.Minute,
		RestBridgeTimeout: time.Minute,
		DefaultNotFoundHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "root 404", http.StatusNotFound)
		}),
		DefaultMethodNotAllowedHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "root 405", http.StatusMethodNotAllowed)
		}),
		RouteErrorPolicies: []*RouteErrorPolicy{
			{
				PathPrefix: "/workspaces",
				NotFoundHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "workspace 404", http.StatusNotFound)
				}),
				MethodNotAllowedHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "workspace 405", http.StatusMethodNotAllowed)
				}),
			},
			{
				PathPrefix: "/workspaces/admin",
				NotFoundHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "admin 404", http.StatusNotFound)
				}),
			},
		},
	}

	ps := NewPlatformServer(cfg).(*platformServer)
	ps.router.Path("/workspaces").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	t.Run("deepest not found policy wins", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/workspaces/admin/missing", nil)
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "admin 404")
	})

	t.Run("method not allowed policy uses matching prefix", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/workspaces", nil)
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "workspace 405")
	})

	t.Run("method not allowed falls back to parent prefix when child has no 405", func(t *testing.T) {
		ps.router.Path("/workspaces/admin").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).Methods(http.MethodGet)
		req := httptest.NewRequest(http.MethodPost, "/workspaces/admin", nil)
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "workspace 405")
	})

	t.Run("prefix boundaries are respected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/workspacesx/missing", nil)
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "root 404")
	})
}

func TestSPAFallbackAppliesCacheControlRules(t *testing.T) {
	tempDir := t.TempDir()
	assert.NoError(t, os.MkdirAll(filepath.Join(tempDir, "assets"), 0o700))
	assert.NoError(t, os.WriteFile(filepath.Join(tempDir, "index.html"), []byte("spa-index"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(tempDir, "assets", "index.abc123.js"), []byte("console.log('ok')"), 0o600))

	const immutable = "public, max-age=31536000, immutable"
	cfg := &PlatformServerConfig{
		RootDir:           tempDir,
		Host:              "localhost",
		ShutdownTimeout:   time.Minute,
		RestBridgeTimeout: time.Minute,
		SpaConfig: &SpaConfig{
			RootFolder:        tempDir,
			BaseUri:           "/",
			FallbackPredicate: BrowserNavigationFallback(),
			CacheControlRules: map[string]string{
				"/**":        "no-cache",
				"/assets/**": immutable,
			},
		},
		DefaultNotFoundHandler: http.NotFoundHandler(),
	}

	ps := NewPlatformServer(cfg).(*platformServer)
	ps.configureSPA()

	serve := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		rsp := httptest.NewRecorder()
		ps.router.ServeHTTP(rsp, req)
		return rsp
	}

	t.Run("hashed assets get the specific immutable rule", func(t *testing.T) {
		rsp := serve("/assets/index.abc123.js?v=1")
		assert.Equal(t, http.StatusOK, rsp.Code)
		assert.Equal(t, immutable, rsp.Header().Get("Cache-Control"))
		assert.Equal(t, "console.log('ok')", rsp.Body.String())
	})

	t.Run("navigation falls back to index with the catch-all rule", func(t *testing.T) {
		rsp := serve("/workspaces/abc")
		assert.Equal(t, http.StatusOK, rsp.Code)
		assert.Equal(t, "no-cache", rsp.Header().Get("Cache-Control"))
		assert.Contains(t, rsp.Body.String(), "spa-index")
	})

	t.Run("root gets the catch-all rule", func(t *testing.T) {
		rsp := serve("/")
		assert.Equal(t, http.StatusOK, rsp.Code)
		assert.Equal(t, "no-cache", rsp.Header().Get("Cache-Control"))
	})

	t.Run("missing assets are not cached", func(t *testing.T) {
		rsp := serve("/assets/index.gone999.js")
		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Empty(t, rsp.Header().Get("Cache-Control"))
	})

	t.Run("extensionless paths under assets use the index rule", func(t *testing.T) {
		rsp := serve("/assets/js/")
		assert.Equal(t, http.StatusOK, rsp.Code)
		assert.Equal(t, "no-cache", rsp.Header().Get("Cache-Control"))
		assert.Contains(t, rsp.Body.String(), "spa-index")
	})
}

func TestCacheControlMiddlewareKeepsHandlerHeaderAndIgnoresInformational(t *testing.T) {
	cfg := &SpaConfig{CacheControlRules: map[string]string{"/**": "public, max-age=60"}}
	cfg.CollateCacheControlRules()
	mw := cfg.CacheControlMiddleware()

	own := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	}))
	rsp := httptest.NewRecorder()
	own.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, "no-store", rsp.Header().Get("Cache-Control"))

	hints := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusOK)
	}))
	rsp = httptest.NewRecorder()
	hints.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, "public, max-age=60", rsp.Header().Get("Cache-Control"))

	flush := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		assert.True(t, ok)
		f.Flush()
	}))
	rsp = httptest.NewRecorder()
	flush.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.True(t, rsp.Flushed)
}

func TestCollateCacheControlRulesIsIdempotentAndOrdered(t *testing.T) {
	cfg := &SpaConfig{CacheControlRules: map[string]string{
		"/**":        "no-cache",
		"/assets/**": "immutable",
		"/a/**":      "short",
	}}
	cfg.CollateCacheControlRules()
	cfg.CollateCacheControlRules()

	assert.Len(t, cfg.cacheControlRulePairs, 3)
	assert.Equal(t, []string{"/assets/**", "/a/**", "/**"}, []string{
		cfg.cacheControlRulePairs[0].GlobPattern,
		cfg.cacheControlRulePairs[1].GlobPattern,
		cfg.cacheControlRulePairs[2].GlobPattern,
	})
}

func TestBrowserNavigationFallbackServesRootToAnyClient(t *testing.T) {
	fallback := BrowserNavigationFallback()
	newReq := func(method, path, accept string) *http.Request {
		req := httptest.NewRequest(method, path, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		return req
	}

	assert.True(t, fallback(newReq(http.MethodGet, "/", "")))
	assert.True(t, fallback(newReq(http.MethodGet, "/", "*/*")))
	assert.True(t, fallback(newReq(http.MethodHead, "/", "")))
	assert.True(t, fallback(newReq(http.MethodGet, "/workspaces/abc", "text/html")))
	assert.False(t, fallback(newReq(http.MethodGet, "/workspaces/abc", "*/*")))
	assert.False(t, fallback(newReq(http.MethodGet, "/missing", "application/json")))
	assert.False(t, fallback(newReq(http.MethodPost, "/", "text/html")))
}
