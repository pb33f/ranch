package routing

import (
	"net/http"
	"strings"
	"sync"
)

// MiddlewareFunc wraps an HTTP handler with cross-cutting behavior.
type MiddlewareFunc func(http.Handler) http.Handler

// Router stores named routes and dispatches requests through a net/http ServeMux.
type Router struct {
	mu                      sync.RWMutex
	routes                  []*Route
	named                   map[string]*Route
	middleware              []MiddlewareFunc
	NotFoundHandler         http.Handler
	MethodNotAllowedHandler http.Handler
	dispatchMux             *http.ServeMux
	pathMux                 *http.ServeMux
	dirty                   bool
}

// Route is a registered path or prefix route.
type Route struct {
	router  *Router
	name    string
	path    string
	prefix  bool
	methods []string
	handler http.Handler
}

// NewRouter creates an empty Router.
func NewRouter() *Router {
	return &Router{
		named: make(map[string]*Route),
		dirty: true,
	}
}

// Use appends global middleware applied to all registered route handlers.
func (r *Router) Use(middleware ...MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, middleware...)
	r.markDirtyLocked()
}

// Handle registers handler for an exact path pattern.
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.Path(pattern).Handler(handler)
}

// HandleFunc registers handler for an exact path pattern.
func (r *Router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.Handle(pattern, http.HandlerFunc(handler))
}

// Path registers an exact route path and returns it for further configuration.
func (r *Router) Path(path string) *Route {
	return r.addRoute(path, false)
}

// PathPrefix registers a prefix route and returns it for further configuration.
func (r *Router) PathPrefix(prefix string) *Route {
	return r.addRoute(prefix, true)
}

// Get returns a named route, or nil when no route is registered under name.
func (r *Router) Get(name string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.named[name]
}

// CloneExcluding returns a router clone without the named routes in names.
func (r *Router) CloneExcluding(names map[string]bool) *Router {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clone := NewRouter()
	clone.NotFoundHandler = r.NotFoundHandler
	clone.MethodNotAllowedHandler = r.MethodNotAllowedHandler
	clone.middleware = append(clone.middleware, r.middleware...)

	for _, route := range r.routes {
		if names[route.name] {
			continue
		}
		routeClone := route.cloneFor(clone)
		clone.routes = append(clone.routes, routeClone)
		if routeClone.name != "" {
			clone.named[routeClone.name] = routeClone
		}
	}

	return clone
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	snapshot := r.snapshot()

	// Let ServeMux do the real dispatch so path values and canonical redirects
	// stay aligned with the standard library.
	if _, pattern := snapshot.dispatchMux.Handler(req); pattern != "" {
		snapshot.dispatchMux.ServeHTTP(w, req)
		return
	}

	// ServeMux does not expose method-mismatch separately from no-match. A
	// second path-only mux gives us that distinction without manual matching.
	if _, pattern := snapshot.pathMux.Handler(req); pattern != "" {
		serveMethodNotAllowed(snapshot.methodNotAllowedHandler, w, req)
		return
	}

	serveNotFound(snapshot.notFoundHandler, w, req)
}

func (r *Router) addRoute(path string, prefix bool) *Route {
	route := &Route{
		router: r,
		path:   normalizePath(path),
		prefix: prefix,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, route)
	r.markDirtyLocked()
	return route
}

// Name assigns a lookup name to the route.
func (rt *Route) Name(name string) *Route {
	rt.router.mu.Lock()
	defer rt.router.mu.Unlock()

	if rt.name != "" && rt.router.named[rt.name] == rt {
		delete(rt.router.named, rt.name)
	}
	rt.name = name
	if name != "" {
		rt.router.named[name] = rt
	}
	return rt
}

// Handler assigns the HTTP handler for the route.
func (rt *Route) Handler(handler http.Handler) *Route {
	rt.router.mu.Lock()
	defer rt.router.mu.Unlock()
	rt.handler = handler
	rt.router.markDirtyLocked()
	return rt
}

// HandlerFunc assigns the HTTP handler function for the route.
func (rt *Route) HandlerFunc(handler func(http.ResponseWriter, *http.Request)) *Route {
	return rt.Handler(http.HandlerFunc(handler))
}

// Methods restricts the route to the supplied HTTP methods.
func (rt *Route) Methods(methods ...string) *Route {
	rt.router.mu.Lock()
	defer rt.router.mu.Unlock()
	rt.methods = append(rt.methods[:0], methods...)
	rt.router.markDirtyLocked()
	return rt
}

// GetName returns the route name.
func (rt *Route) GetName() string {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return rt.name
}

// GetPathTemplate returns the route path template.
func (rt *Route) GetPathTemplate() (string, error) {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return rt.path, nil
}

// GetMethods returns the configured HTTP methods.
func (rt *Route) GetMethods() ([]string, error) {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return append([]string(nil), rt.methods...), nil
}

// GetHandler returns the route handler.
func (rt *Route) GetHandler() http.Handler {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return rt.handler
}

func (rt *Route) cloneFor(router *Router) *Route {
	return &Route{
		router:  router,
		name:    rt.name,
		path:    rt.path,
		prefix:  rt.prefix,
		methods: append([]string(nil), rt.methods...),
		handler: rt.handler,
	}
}

type routerSnapshot struct {
	dispatchMux             *http.ServeMux
	pathMux                 *http.ServeMux
	notFoundHandler         http.Handler
	methodNotAllowedHandler http.Handler
}

func (r *Router) snapshot() routerSnapshot {
	r.mu.RLock()
	if !r.dirty && r.dispatchMux != nil && r.pathMux != nil {
		snapshot := r.snapshotLocked()
		r.mu.RUnlock()
		return snapshot
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dirty || r.dispatchMux == nil || r.pathMux == nil {
		r.rebuildMuxLocked()
	}

	return r.snapshotLocked()
}

func (r *Router) snapshotLocked() routerSnapshot {
	return routerSnapshot{
		dispatchMux:             r.dispatchMux,
		pathMux:                 r.pathMux,
		notFoundHandler:         r.NotFoundHandler,
		methodNotAllowedHandler: r.MethodNotAllowedHandler,
	}
}

func (r *Router) rebuildMuxLocked() {
	dispatchMux := http.NewServeMux()
	pathMux := http.NewServeMux()
	dispatchPatterns := make(map[string]struct{})
	pathPatterns := make(map[string]struct{})

	for _, route := range r.routes {
		if route.handler == nil {
			continue
		}

		handler := routeHandler(r, route.methods, buildMiddlewareChain(r.middleware, route.handler))
		for _, pattern := range route.dispatchPatterns() {
			registerPattern(dispatchMux, dispatchPatterns, pattern, handler)
		}
		for _, pattern := range route.pathPatterns() {
			registerPattern(pathMux, pathPatterns, pattern, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		}
	}

	r.dispatchMux = dispatchMux
	r.pathMux = pathMux
	r.dirty = false
}

func (r *Router) markDirtyLocked() {
	r.dirty = true
}

func registerPattern(mux *http.ServeMux, registered map[string]struct{}, pattern string, handler http.Handler) {
	if _, exists := registered[pattern]; exists {
		return
	}
	registered[pattern] = struct{}{}
	mux.Handle(pattern, handler)
}

func routeHandler(router *Router, methods []string, handler http.Handler) http.Handler {
	allowedMethods := append([]string(nil), methods...)
	if len(allowedMethods) == 0 {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Standard ServeMux treats GET patterns as matching HEAD. Ranch bridges
		// have explicit AllowHead configuration, so keep method checks exact.
		if !methodAllowed(allowedMethods, req.Method) {
			serveMethodNotAllowed(router.methodNotAllowedHandler(), w, req)
			return
		}
		handler.ServeHTTP(w, req)
	})
}

func (r *Router) methodNotAllowedHandler() http.Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.MethodNotAllowedHandler
}

func (rt *Route) dispatchPatterns() []string {
	pathPatterns := rt.pathPatterns()
	if len(rt.methods) == 0 {
		return pathPatterns
	}

	patterns := make([]string, 0, len(pathPatterns)*len(rt.methods))
	for _, pathPattern := range pathPatterns {
		for _, method := range rt.methods {
			patterns = append(patterns, method+" "+pathPattern)
		}
	}
	return patterns
}

func (rt *Route) pathPatterns() []string {
	path := serveMuxPathTemplate(rt.path)
	if !rt.prefix {
		return []string{exactMuxPattern(path)}
	}
	if path == "/" {
		return []string{"/"}
	}
	if strings.HasSuffix(path, "/") {
		return []string{path}
	}
	// ServeMux subtree patterns require a trailing slash. Register the bare
	// prefix as well so PathPrefix("/api") matches both /api and /api/... .
	return []string{path, path + "/"}
}

func buildMiddlewareChain(middleware []MiddlewareFunc, handler http.Handler) http.Handler {
	for idx := len(middleware) - 1; idx >= 0; idx-- {
		handler = middleware[idx](handler)
	}
	return handler
}

func methodAllowed(methods []string, requestMethod string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, method := range methods {
		if method == requestMethod {
			return true
		}
	}
	return false
}

func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func serveMuxPathTemplate(path string) string {
	path = normalizePath(path)
	if path == "/" {
		return "/"
	}

	parts := strings.Split(path, "/")
	for idx, part := range parts {
		name, ok := pathVariableName(part)
		if ok {
			parts[idx] = "{" + name + "}"
		}
	}
	return strings.Join(parts, "/")
}

func exactMuxPattern(path string) string {
	if path == "/" {
		return "/{$}"
	}
	if strings.HasSuffix(path, "/") {
		return path + "{$}"
	}
	return path
}

func pathVariableName(segment string) (string, bool) {
	if len(segment) < 3 || segment[0] != '{' || segment[len(segment)-1] != '}' {
		return "", false
	}
	name := segment[1 : len(segment)-1]
	if idx := strings.IndexByte(name, ':'); idx >= 0 {
		name = name[:idx]
	}
	if name == "" {
		return "", false
	}
	return name, true
}

func serveMethodNotAllowed(handler http.Handler, w http.ResponseWriter, req *http.Request) {
	if handler != nil {
		handler.ServeHTTP(w, req)
		return
	}
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
}

func serveNotFound(handler http.Handler, w http.ResponseWriter, req *http.Request) {
	if handler != nil {
		handler.ServeHTTP(w, req)
		return
	}
	http.NotFound(w, req)
}
