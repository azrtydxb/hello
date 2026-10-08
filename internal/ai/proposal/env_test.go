package proposal_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// env is the real API on a scratch database with the proposal store wired
// in, as hello-control wires it.
type env struct {
	t      *testing.T
	srv    *httptest.Server
	db     *sql.DB
	st     *store.Store
	val    *proposal.SchemaValidator
	ps     *proposal.DBStore
	tokens map[string]string // role -> bearer token
	users  map[string]int64
}

func loadSpec(t *testing.T) *apispec.Spec {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := apispec.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_proposal_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatal("HELLO_TEST_DATABASE_URL must be a postgres:// URL")
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	val, err := proposal.NewValidator(loadSpec(t), nil, st)
	if err != nil {
		t.Fatal(err)
	}
	ps := proposal.NewStore(db, val)
	h := api.Handler(api.Config{Store: st, SIPDomain: "hello.test", SessionTTL: time.Hour, Proposals: ps})
	val.SetHandler(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e := &env{t: t, srv: srv, db: db, st: st, val: val, ps: ps, tokens: map[string]string{}, users: map[string]int64{}}
	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer} {
		hash, err := auth.HashPassword("Password-1-" + string(role))
		if err != nil {
			t.Fatal(err)
		}
		uid, err := st.CreateUser(ctx, "test", string(role), hash, role)
		if err != nil {
			t.Fatal(err)
		}
		plain, th := auth.NewToken()
		if _, err := st.CreateToken(ctx, "test", uid, store.NewToken{Name: "t"}, th); err != nil {
			t.Fatal(err)
		}
		e.tokens[string(role)], e.users[string(role)] = plain, uid
	}
	return e
}

type resp struct {
	code int
	body []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("%d %s: %v", r.code, r.body, err)
	}
	return m
}

// do calls the API as role with a bearer token.
func (e *env) do(role, method, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.tokens[role])
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, b}
}

func (e *env) must(role string, want int, method, path string, body any) map[string]any {
	e.t.Helper()
	r := e.do(role, method, path, body)
	if r.code != want {
		e.t.Fatalf("%s %s = %d %s, want %d", method, path, r.code, r.body, want)
	}
	if len(r.body) == 0 {
		return nil
	}
	return r.json(e.t)
}

func id(m map[string]any) string { return fmt.Sprint(m["id"]) }

// ident is the user a validation reads as.
func (e *env) ident() proposal.Identity {
	return proposal.Identity{UserID: e.users["operator"], TaskID: "task-1"}
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// seed creates two extensions, a trunk, a ring group and an outbound route.
type seed struct {
	ext1, ext2, trunk, group, route string
}

func (e *env) seed() seed {
	e.t.Helper()
	var s seed
	s.ext1 = id(e.must("admin", 201, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk"}))
	s.ext2 = id(e.must("admin", 201, "POST", "/api/v1/extensions", map[string]any{"number": "102", "name": "Sales"}))
	s.trunk = id(e.must("admin", 201, "POST", "/api/v1/trunks", map[string]any{
		"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	}))
	s.group = id(e.must("admin", 201, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "sales", "strategy": "sequential", "ringTimeout": 25,
		"members": []map[string]any{
			{"extensionId": json.Number(s.ext1), "position": 1},
			{"extensionId": json.Number(s.ext2), "position": 2},
		},
	}))
	s.route = id(e.must("admin", 201, "POST", "/api/v1/routes/outbound", map[string]any{
		"name": "All", "matchKind": "prefix", "match": "0", "trunks": []json.Number{json.Number(s.trunk)},
	}))
	return s
}

func (e *env) revision() int64 {
	rev, err := e.st.ConfigRevision(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return rev
}
