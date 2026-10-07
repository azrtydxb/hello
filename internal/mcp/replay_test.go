package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// apiStore is the API's store with the methods the replayed operations use;
// any other method panics, as the fakes of internal/api do.
type apiStore struct {
	api.Store
	mu      sync.Mutex
	secrets []string
	actors  []string
}

func (*apiStore) SessionActor(ctx context.Context, h []byte) (auth.Actor, error) {
	return fakeLookup{}.SessionActor(ctx, h)
}

func (*apiStore) TokenActor(ctx context.Context, h []byte) (auth.Actor, error) {
	return fakeLookup{}.TokenActor(ctx, h)
}

func (*apiStore) ListExtensions(context.Context) ([]store.Extension, error) {
	return []store.Extension{{ID: 7, Number: "201", Name: "Ann"}}, nil
}

func (*apiStore) GetExtension(_ context.Context, id int64) (store.Extension, error) {
	if id != 7 {
		return store.Extension{}, store.ErrNotFound
	}
	return store.Extension{ID: 7, Number: "201", Name: "Ann"}, nil
}

func (s *apiStore) CreateDevice(_ context.Context, actor string, in store.NewDevice) (store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = append(s.secrets, in.Secret)
	s.actors = append(s.actors, actor)
	return store.Device{ID: 9, ExtensionID: in.ExtensionID, SIPUsername: in.SIPUsername, Enabled: in.Enabled}, nil
}

// TestToolReplay fails if a tool call does not reach the handler with the
// caller's credentials and the replay marker, an external request carrying
// any header can pose as a replay, a replay can replay, an API error is not
// returned as isError with code and fields, or an x-hello-secret value
// (device create included) appears in a result (spec S-15).
func TestToolReplay(t *testing.T) {
	st := &apiStore{}
	rec := &recordingAPI{next: api.Handler(api.Config{Store: st, SIPDomain: "pbx.test"})}
	ts, s := newTestServer(t, rec, fixtureOps(), nil)
	ctx := context.Background()

	t.Run("credentials and marker", func(t *testing.T) {
		cs := connect(t, ts.URL, tokOAuth, "2026-07-28")
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "listExtensions", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("listExtensions = %+v, %v", res, err)
		}
		got := rec.last(t)
		if got.header.Get("Authorization") != "Bearer "+tokOAuth {
			t.Errorf("replayed Authorization = %q", got.header.Get("Authorization"))
		}
		if !got.replayed || got.replay.ClientID != "https://client.test/cimd" {
			t.Errorf("replay marker = %+v, %v; want the OAuth client", got.replay, got.replayed)
		}
		items, _ := res.StructuredContent.(map[string]any)["items"].([]any)
		if len(items) != 1 || !strings.Contains(resultText(t, res), `"number":"201"`) {
			t.Errorf("structuredContent = %v, text %s", res.StructuredContent, resultText(t, res))
		}
	})
	t.Run("only Authorization is copied", func(t *testing.T) {
		resp, _ := post(t, ts.URL, callBody("getExtension", map[string]any{"id": 7}), map[string]string{
			"Authorization": "Bearer " + tokRead, "Cookie": "hello_session=x", "X-Hello-Replay": "1",
			"X-Forwarded-For": "10.0.0.1", "Mcp-Replay": "client",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		got := rec.last(t)
		for k := range got.header {
			if k != "Authorization" {
				t.Errorf("replay copied header %s", k)
			}
		}
		if got.uri != "/api/v1/extensions/7" || got.replay.ClientID != "" || !got.replayed {
			t.Errorf("replayed %s marker %+v", got.uri, got.replay)
		}
	})
	t.Run("external request cannot pose as a replay", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/extensions", nil)
		for _, h := range []string{"X-Hello-Replay", "Mcp-Replay", "X-Mcp-Replay", "Replay", "Via"} {
			req.Header.Set(h, "https://client.test/cimd")
		}
		req.Header.Set("Authorization", "Bearer "+tokOAuth)
		rec.ServeHTTP(httptest.NewRecorder(), req)
		if got := rec.last(t); got.replayed {
			t.Fatalf("an external request with replay-looking headers carried the marker %+v", got.replay)
		}
	})
	t.Run("a replay cannot replay", func(t *testing.T) {
		nested := auth.WithReplay(ctx, auth.Replay{ClientID: "c"})
		if _, err := s.replay(nested, caller{authz: "Bearer " + tokRead}, "GET", "/api/v1/extensions", nil, nil); !errors.Is(err, errNestedReplay) {
			t.Errorf("nested replay = %v", err)
		}
		req := httptest.NewRequestWithContext(nested, http.MethodPost, "/mcp", strings.NewReader(callBody("listExtensions", nil)))
		req.Header.Set("Authorization", "Bearer "+tokRead)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("/mcp inside a replay = %d, want 403", w.Code)
		}
	})
	t.Run("api errors", func(t *testing.T) {
		cs := connect(t, ts.URL, tokWrite, "2025-11-25")
		for _, c := range []struct {
			tool   string
			args   map[string]any
			code   string
			fields bool
		}{
			{"createExtension", map[string]any{"body": map[string]any{"number": "x", "name": ""}}, "bad_request", true},
			{"getExtension", map[string]any{"id": 8}, "not_found", false},
			{"createDevice", map[string]any{"body": map[string]any{"extensionId": 7, "sipUsername": "bad name!"}}, "bad_request", false},
			{"getExtension", map[string]any{}, "bad_request", false},
			{"getExtension", map[string]any{"id": 7, "extra": 1}, "bad_request", false},
		} {
			res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: c.tool, Arguments: c.args})
			if err != nil {
				t.Fatal(err)
			}
			var env struct {
				Error struct {
					Code, Message string
					Fields        []any
				}
			}
			if !res.IsError || json.Unmarshal([]byte(resultText(t, res)), &env) != nil || env.Error.Code != c.code ||
				env.Error.Message == "" || (len(env.Error.Fields) > 0) != c.fields {
				t.Errorf("%s %v = isError %v %s, want %s fields %v", c.tool, c.args, res.IsError, resultText(t, res), c.code, c.fields)
			}
		}
	})
	t.Run("device create withholds the secret", func(t *testing.T) {
		cs := connect(t, ts.URL, tokWrite, "2025-11-25")
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "createDevice", Arguments: map[string]any{
			"body": map[string]any{"extensionId": 7, "sipUsername": "ann-desk"}}})
		if err != nil || res.IsError {
			t.Fatalf("createDevice = %+v, %v", res, err)
		}
		st.mu.Lock()
		secret := st.secrets[len(st.secrets)-1]
		st.mu.Unlock()
		b, _ := json.Marshal(res)
		if secret == "" || strings.Contains(string(b), secret) {
			t.Fatalf("the device secret %q left through MCP: %s", secret, b)
		}
		if res.StructuredContent.(map[string]any)["secret"] != Withheld || !strings.Contains(resultText(t, res), "withheld") {
			t.Fatalf("secret not replaced by the withheld note: %s", b)
		}
		if got := rec.last(t); got.body != `{"extensionId":7,"sipUsername":"ann-desk"}` || got.header.Get("Content-Type") != "application/json" {
			t.Errorf("replayed body %s (%s)", got.body, got.header.Get("Content-Type"))
		}
	})
	t.Run("escaped path and query", func(t *testing.T) {
		cs := connect(t, ts.URL, tokAdmin, "2025-11-25")
		_, _ = cs.CallTool(ctx, &sdk.CallToolParams{Name: "drainNode", Arguments: map[string]any{"id": "a/../b c", "force": true}})
		if got := rec.last(t); got.uri != "/api/v1/cluster/nodes/a%2F..%2Fb%20c/drain?force=true" {
			t.Errorf("replayed %s", got.uri)
		}
	})
	t.Run("panic and large text", func(t *testing.T) {
		boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
		_, ps := newTestServer(t, boom, fixtureOps(), nil)
		r, err := ps.replay(ctx, caller{authz: "Bearer " + tokRead}, "GET", "/api/v1/extensions", nil, nil)
		if err != nil || r.fail != "internal" || !toolResult(r, nil).IsError {
			t.Errorf("panicking handler = %+v, %v", r, err)
		}
		big := strings.Repeat("é", textCap)
		if got := capText(big); len(got) > textCap+200 || !strings.Contains(got, "truncated") || !strings.HasPrefix(big, strings.SplitN(got, "\n", 2)[0]) {
			t.Errorf("capText kept %d bytes", len(got))
		}
	})
}
