package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// stubStore answers the version route and rejects every credential, so the
// routing test needs no database.
type stubStore struct {
	Store // nil: any other method panics
}

func (stubStore) ConfigRevision(context.Context) (int64, error) { return 42, nil }
func (stubStore) SessionActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}

func (stubStore) TokenActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}

// publicOps are the operations reachable without credentials, with the
// status an empty request gets.
var publicOps = map[string]int{
	"GET /api/v1/version":      http.StatusOK,
	"GET /api/v1/openapi.json": http.StatusOK,
	"POST /api/v1/auth/login":  http.StatusBadRequest,
}

// documentedOps lists "METHOD /path" for every operation in openapi.json.
func documentedOps(t *testing.T) []string {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPI, &doc); err != nil {
		t.Fatal(err)
	}
	var ops []string
	for p, methods := range doc.Paths {
		for m := range methods {
			ops = append(ops, strings.ToUpper(m)+" "+p)
		}
	}
	sort.Strings(ops)
	return ops
}

// concrete fills path parameters.
func concrete(path string) string { return strings.ReplaceAll(path, "{id}", "1") }

func TestVersionAndOpenAPI(t *testing.T) {
	h := Handler(Config{Store: stubStore{}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("version = %d %q: %v", rec.Code, rec.Body, err)
	}
	for _, k := range []string{"version", "commit", "configRevision"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("version response missing %q: %v", k, v)
		}
	}
	if v["configRevision"] != float64(42) {
		t.Fatalf("configRevision = %v, want the store's 42", v["configRevision"])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	var doc struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("openapi.json is not JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Fatalf("openapi version = %q, want 3.x", doc.OpenAPI)
	}

	// Every documented operation is routed: a protected one answers 401
	// (the mux matched and the middleware ran), a public one its own status;
	// an unrouted one would get the mux's 404 or 405.
	ops := documentedOps(t)
	if len(ops) < 20 {
		t.Fatalf("openapi.json documents only %d operations: %v", len(ops), ops)
	}
	for _, op := range ops {
		method, path, _ := strings.Cut(op, " ")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, concrete(path), strings.NewReader("")))
		want, public := publicOps[op]
		if !public {
			want = http.StatusUnauthorized
		}
		if rec.Code != want {
			t.Errorf("%s = %d, want %d (is it routed?)", op, rec.Code, want)
		}
	}
	for op := range publicOps {
		if !slices.Contains(ops, op) {
			t.Errorf("public operation %s is not documented", op)
		}
	}

	// An undocumented path is not routed.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/nope = %d, want 404", rec.Code)
	}
}
