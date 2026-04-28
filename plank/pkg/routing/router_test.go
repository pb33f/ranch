package routing

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterMatchesMethodsAndPathValues(t *testing.T) {
	router := NewRouter()
	router.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})
	router.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wrong method", http.StatusMethodNotAllowed)
	})
	router.Path("/items/{id}").Methods(http.MethodGet).HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Path-ID", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("path value", func(t *testing.T) {
		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/items/42", nil))
		if rsp.Code != http.StatusNoContent {
			t.Fatalf("expected status %d, got %d", http.StatusNoContent, rsp.Code)
		}
		if rsp.Header().Get("X-Path-ID") != "42" {
			t.Fatalf("expected path value header, got %q", rsp.Header().Get("X-Path-ID"))
		}
	})

	t.Run("method mismatch", func(t *testing.T) {
		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodPost, "/items/42", nil))
		if rsp.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rsp.Code)
		}
	})

	t.Run("head requires explicit method", func(t *testing.T) {
		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodHead, "/items/42", nil))
		if rsp.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rsp.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/items/42/details", nil))
		if rsp.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d", http.StatusNotFound, rsp.Code)
		}
	})
}

func TestRouterCloneExcludingPreservesMiddlewareAndRemainingRoutes(t *testing.T) {
	router := NewRouter()
	router.NotFoundHandler = http.NotFoundHandler()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test-Middleware", "yes")
			next.ServeHTTP(w, r)
		})
	})
	router.Path("/old").Name("old").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("old"))
	})
	router.PathPrefix("/static/").Name("static*").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("static"))
	})

	clone := router.CloneExcluding(map[string]bool{"old": true})
	if clone.Get("old") != nil {
		t.Fatal("expected excluded route to be removed")
	}
	if clone.Get("static*") == nil {
		t.Fatal("expected remaining route to be preserved")
	}

	rsp := httptest.NewRecorder()
	clone.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	if rsp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rsp.Code)
	}
	if rsp.Body.String() != "static" {
		t.Fatalf("expected static route body, got %q", rsp.Body.String())
	}
	if rsp.Header().Get("X-Test-Middleware") != "yes" {
		t.Fatal("expected middleware to be preserved")
	}
}

func TestRouterUsesServeMuxPathSemantics(t *testing.T) {
	t.Run("prefix boundaries", func(t *testing.T) {
		router := NewRouter()
		router.PathPrefix("/api").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("prefix"))
		})

		for _, path := range []string{"/api", "/api/users"} {
			rsp := httptest.NewRecorder()
			router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, path, nil))
			if rsp.Code != http.StatusOK {
				t.Fatalf("expected %s to match prefix route with status %d, got %d", path, http.StatusOK, rsp.Code)
			}
			if rsp.Body.String() != "prefix" {
				t.Fatalf("expected prefix body for %s, got %q", path, rsp.Body.String())
			}
		}

		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/apix", nil))
		if rsp.Code != http.StatusNotFound {
			t.Fatalf("expected boundary miss status %d, got %d", http.StatusNotFound, rsp.Code)
		}
	})

	t.Run("exact route wins over prefix", func(t *testing.T) {
		router := NewRouter()
		router.PathPrefix("/api").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("prefix"))
		})
		router.Path("/api/status").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("exact"))
		})

		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		if rsp.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, rsp.Code)
		}
		if rsp.Body.String() != "exact" {
			t.Fatalf("expected exact route body, got %q", rsp.Body.String())
		}
	})

	t.Run("trailing slash exact route is not subtree", func(t *testing.T) {
		router := NewRouter()
		router.Path("/docs/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("docs"))
		})

		rsp := httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/docs/", nil))
		if rsp.Code != http.StatusOK {
			t.Fatalf("expected exact trailing slash status %d, got %d", http.StatusOK, rsp.Code)
		}

		rsp = httptest.NewRecorder()
		router.ServeHTTP(rsp, httptest.NewRequest(http.MethodGet, "/docs/page", nil))
		if rsp.Code != http.StatusNotFound {
			t.Fatalf("expected subtree miss status %d, got %d", http.StatusNotFound, rsp.Code)
		}
	})
}
