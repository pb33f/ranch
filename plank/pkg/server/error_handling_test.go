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
	ps.router.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
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

	t.Run("prefix boundaries are respected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/workspacesx/missing", nil)
		rsp := httptest.NewRecorder()

		ps.router.ServeHTTP(rsp, req)

		assert.Equal(t, http.StatusNotFound, rsp.Code)
		assert.Contains(t, rsp.Body.String(), "root 404")
	})
}
