// Package kittest is the fake CRM server the connector tests share: routes
// by "METHOD /path", every request recorded.
package kittest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Call is one recorded request.
type Call struct {
	Method, Path, Query, Body string
	Header                    http.Header
}

// Handler answers a request; body is what the client sent.
type Handler func(w http.ResponseWriter, r *http.Request, body string)

// Server is the fake CRM.
type Server struct {
	URL    string
	Client *http.Client
	mu     sync.Mutex
	calls  []Call
	routes map[string]Handler
}

// New starts a server that closes with the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{routes: map[string]Handler{}}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL, s.Client = srv.URL, srv.Client()
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls = append(s.calls, Call{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
	h := s.routes[r.Method+" "+r.URL.Path]
	s.mu.Unlock()
	if h == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no route"}`))
		return
	}
	h(w, r, string(b))
}

// Handle routes "METHOD /path" to h.
func (s *Server) Handle(route string, h Handler) {
	s.mu.Lock()
	s.routes[route] = h
	s.mu.Unlock()
}

// JSON routes "METHOD /path" to a fixed answer.
func (s *Server) JSON(route string, status int, body string) {
	s.Handle(route, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// Calls returns the requests to a route ("" for all), oldest first.
func (s *Server) Calls(route string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if route == "" || c.Method+" "+c.Path == route {
			out = append(out, c)
		}
	}
	return out
}

// Contains reports whether every needle is in hay.
func Contains(hay string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(hay, n) {
			return false
		}
	}
	return true
}
