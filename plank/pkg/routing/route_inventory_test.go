package routing

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
)

func TestRoutesIncludesEveryRegistration(t *testing.T) {
	router := NewRouter()
	if got := router.Routes(); len(got) != 0 {
		t.Fatalf("empty router has routes: %#v", got)
	}
	router.HandleFunc("/socket", func(http.ResponseWriter, *http.Request) {})
	router.PathPrefix("/assets/").Methods(http.MethodGet, http.MethodHead).
		Name("assets").Handler(http.NotFoundHandler())
	router.Path("/pending").Methods(http.MethodPost)
	// duplicate registrations must remain visible even when dispatch selects one.
	router.Path("/socket").Name("duplicate").Handler(http.NotFoundHandler())
	want := []RouteInfo{
		{Path: "/socket", HasHandler: true},
		{Path: "/assets/", Prefix: true, Methods: []string{http.MethodGet, http.MethodHead}, Name: "assets", HasHandler: true},
		{Path: "/pending", Methods: []string{http.MethodPost}},
		{Path: "/socket", Name: "duplicate", HasHandler: true},
	}
	if got := router.Routes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations = %#v, want %#v", got, want)
	}
}

func TestRoutesSnapshotIsIndependentOfRouteChanges(t *testing.T) {
	router := NewRouter()
	route := router.Path("/items").Methods(http.MethodGet, http.MethodHead).Name("read").Handler(http.NotFoundHandler())
	before := router.Routes()
	route.Methods(http.MethodPost).Name("write").Handler(nil)
	router.Path("/later")
	if want := (RouteInfo{Path: "/items", Methods: []string{http.MethodGet, http.MethodHead}, Name: "read", HasHandler: true}); len(before) != 1 || !reflect.DeepEqual(before[0], want) {
		t.Fatalf("route changes altered an existing snapshot: %#v", before)
	}
	after := router.Routes()
	if want := (RouteInfo{Path: "/items", Methods: []string{http.MethodPost}, Name: "write"}); len(after) != 2 || !reflect.DeepEqual(after[0], want) {
		t.Fatalf("new snapshot did not include route changes: %#v", after)
	}

	before[0].Methods[0] = http.MethodDelete
	after[0].Methods[0] = http.MethodPatch
	after[0].Name = "changed"
	after[0].Path = "/changed"
	after[0].Prefix = true
	after[0].HasHandler = true
	if want := (RouteInfo{Path: "/items", Methods: []string{http.MethodPost}, Name: "write"}); !reflect.DeepEqual(router.Routes()[0], want) {
		t.Fatal("caller mutation changed router metadata")
	}
}

func TestRoutesConcurrentConfiguration(t *testing.T) {
	router := NewRouter()
	route := router.Path("/items").Methods(http.MethodGet, http.MethodHead).Name("read")
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 1000; i++ {
			route.Methods(http.MethodPost, http.MethodPatch).Name("write").Handler(http.NotFoundHandler())
			route.Methods(http.MethodGet, http.MethodHead).Name("read").Handler(nil)
		}
	}()
	defer workers.Wait()
	close(start)
	for i := 0; i < 1000; i++ {
		got := router.Routes()
		if len(got) != 1 || got[0].Path != "/items" || got[0].Prefix {
			t.Fatalf("invalid registration snapshot: %#v", got)
		}
		if got[0].Name != "read" && got[0].Name != "write" {
			t.Fatalf("invalid route name: %q", got[0].Name)
		}
		if !reflect.DeepEqual(got[0].Methods, []string{http.MethodGet, http.MethodHead}) &&
			!reflect.DeepEqual(got[0].Methods, []string{http.MethodPost, http.MethodPatch}) {
			t.Fatalf("torn method snapshot: %#v", got[0].Methods)
		}
		got[0].Methods[0] = "CALLER-ONLY"
	}
}
