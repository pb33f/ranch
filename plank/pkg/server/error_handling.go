package server

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gorilla/mux"
)

func BrowserNavigationFallback() func(*http.Request) bool {
	return func(r *http.Request) bool {
		if r.Method != http.MethodGet {
			return false
		}
		if len(filepath.Ext(r.URL.Path)) > 0 {
			return false
		}

		accept := r.Header.Get("Accept")
		return strings.Contains(accept, "text/html")
	}
}

func (ps *platformServer) configureRouterErrorHandlers(router *mux.Router) {
	router.NotFoundHandler = http.HandlerFunc(ps.handleNotFound)
	router.MethodNotAllowedHandler = http.HandlerFunc(ps.handleMethodNotAllowed)
}

func (ps *platformServer) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if ps.tryServeSPA(w, r) {
		return
	}

	ps.getNotFoundHandler(r.URL.Path).ServeHTTP(w, r)
}

func (ps *platformServer) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	ps.getMethodNotAllowedHandler(r.URL.Path).ServeHTTP(w, r)
}

func (ps *platformServer) getNotFoundHandler(requestPath string) http.Handler {
	if policy := ps.getRouteErrorPolicy(requestPath); policy != nil && policy.NotFoundHandler != nil {
		return policy.NotFoundHandler
	}
	if ps.serverConfig.DefaultNotFoundHandler != nil {
		return ps.serverConfig.DefaultNotFoundHandler
	}
	return http.NotFoundHandler()
}

func (ps *platformServer) getMethodNotAllowedHandler(requestPath string) http.Handler {
	if policy := ps.getRouteErrorPolicy(requestPath); policy != nil && policy.MethodNotAllowedHandler != nil {
		return policy.MethodNotAllowedHandler
	}
	if ps.serverConfig.DefaultMethodNotAllowedHandler != nil {
		return ps.serverConfig.DefaultMethodNotAllowedHandler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	})
}

func (ps *platformServer) getRouteErrorPolicy(requestPath string) *RouteErrorPolicy {
	var matched *RouteErrorPolicy
	longestPrefix := -1

	for _, policy := range ps.serverConfig.RouteErrorPolicies {
		if policy == nil || !pathMatchesPrefix(requestPath, policy.PathPrefix) {
			continue
		}
		if len(policy.PathPrefix) > longestPrefix {
			matched = policy
			longestPrefix = len(policy.PathPrefix)
		}
	}

	return matched
}

func (ps *platformServer) tryServeSPA(w http.ResponseWriter, r *http.Request) bool {
	spaConfig := ps.serverConfig.SpaConfig
	if spaConfig == nil || !pathMatchesPrefix(r.URL.Path, spaConfig.BaseUri) {
		return false
	}

	if spaPathExcluded(r.URL.Path, spaConfig.ExcludedPrefixes) {
		return false
	}

	if len(filepath.Ext(r.URL.Path)) > 0 {
		ps.serveSPAResource(w, r)
		return true
	}

	if spaConfig.FallbackPredicate != nil && !spaConfig.FallbackPredicate(r) {
		return false
	}

	ps.serveSPAResource(w, r)
	return true
}

func spaPathExcluded(requestPath string, excludedPrefixes []string) bool {
	for _, prefix := range excludedPrefixes {
		if pathMatchesPrefix(requestPath, prefix) {
			return true
		}
	}
	return false
}

func pathMatchesPrefix(requestPath, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return strings.HasPrefix(requestPath, "/")
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if strings.HasSuffix(prefix, "/") {
		prefix = strings.TrimSuffix(prefix, "/")
	}
	if requestPath == prefix {
		return true
	}
	if !strings.HasPrefix(requestPath, prefix) {
		return false
	}

	next := requestPath[len(prefix)]
	return next == '/'
}
