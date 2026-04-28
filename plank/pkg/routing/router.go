package routing

import (
	"net/http"
	"strings"
	"sync"
)

type MiddlewareFunc func(http.Handler) http.Handler

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

type Route struct {
	router  *Router
	name    string
	path    string
	prefix  bool
	methods []string
	handler http.Handler
}

func NewRouter() *Router {
	return &Router{
		named: make(map[string]*Route),
		dirty: true,
	}
}

func (r *Router) Use(middleware ...MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, middleware...)
	r.markDirtyLocked()
}

func (r *Router) Handle(pattern string, handler http.Handler) {
	r.Path(pattern).Handler(handler)
}

func (r *Router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.Handle(pattern, http.HandlerFunc(handler))
}

func (r *Router) Path(path string) *Route {
	return r.addRoute(path, false)
}

func (r *Router) PathPrefix(prefix string) *Route {
	return r.addRoute(prefix, true)
}

func (r *Router) Get(name string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.named[name]
}

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

	if _, pattern := snapshot.dispatchMux.Handler(req); pattern != "" {
		snapshot.dispatchMux.ServeHTTP(w, req)
		return
	}

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

func (rt *Route) Handler(handler http.Handler) *Route {
	rt.router.mu.Lock()
	defer rt.router.mu.Unlock()
	rt.handler = handler
	rt.router.markDirtyLocked()
	return rt
}

func (rt *Route) HandlerFunc(handler func(http.ResponseWriter, *http.Request)) *Route {
	return rt.Handler(http.HandlerFunc(handler))
}

func (rt *Route) Methods(methods ...string) *Route {
	rt.router.mu.Lock()
	defer rt.router.mu.Unlock()
	rt.methods = append(rt.methods[:0], methods...)
	rt.router.markDirtyLocked()
	return rt
}

func (rt *Route) GetName() string {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return rt.name
}

func (rt *Route) GetPathTemplate() (string, error) {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return rt.path, nil
}

func (rt *Route) GetMethods() ([]string, error) {
	rt.router.mu.RLock()
	defer rt.router.mu.RUnlock()
	return append([]string(nil), rt.methods...), nil
}

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
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dirty || r.dispatchMux == nil || r.pathMux == nil {
		r.rebuildMuxLocked()
	}

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
