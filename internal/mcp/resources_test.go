package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestResourcesAndPrompts fails if a listed resource or template does not
// return its GET operation's body, works without read, or a prompt names a
// tool or resource that does not exist (spec S-16).
func TestResourcesAndPrompts(t *testing.T) {
	apiH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/404") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"uri": r.URL.RequestURI(), "auth": r.Header.Get("Authorization") != ""})
	})
	ts, _ := newTestServer(t, apiH, fixtureOps(), nil)
	ctx := context.Background()
	cs := connect(t, ts.URL, tokRead, "2025-11-25")

	direct := func(path string) string {
		w := httptest.NewRecorder()
		apiH.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		var v any
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		v.(map[string]any)["auth"] = true
		b, _ := json.Marshal(v)
		return string(b)
	}
	read := func(uri string) (string, error) {
		res, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
		if err != nil {
			return "", err
		}
		if len(res.Contents) != 1 || res.Contents[0].MIMEType != "application/json" || res.Contents[0].URI != uri {
			t.Fatalf("%s: contents %+v", uri, res.Contents)
		}
		return res.Contents[0].Text, nil
	}

	rl, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rl.Resources) != len(resources) {
		t.Errorf("listed %d resources, want %d", len(rl.Resources), len(resources))
	}
	for _, d := range resources {
		if !slices.ContainsFunc(rl.Resources, func(r *sdk.Resource) bool { return r.URI == d.uri }) {
			t.Errorf("%s not listed", d.uri)
		}
		path := d.path
		if len(d.query) > 0 {
			path += "?" + d.query.Encode()
		}
		if got, err := read(d.uri); err != nil || got != direct(path) {
			t.Errorf("%s = %s, %v; want %s", d.uri, got, err, direct(path))
		}
	}
	tl, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.ResourceTemplates) != len(templates) {
		t.Errorf("listed %d templates, want %d", len(tl.ResourceTemplates), len(templates))
	}
	for _, d := range templates {
		uri := strings.ReplaceAll(d.uri, "{id}", "42")
		if got, err := read(uri); err != nil || got != direct(strings.ReplaceAll(d.path, "{id}", "42")) {
			t.Errorf("%s = %s, %v", uri, got, err)
		}
		if _, err := read(strings.ReplaceAll(d.uri, "{id}", "404")); err == nil {
			t.Errorf("%s: a missing item read without error", d.uri)
		}
	}

	t.Run("without read", func(t *testing.T) {
		resp, _ := post(t, ts.URL, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"hello://calls"}}`,
			map[string]string{"Authorization": "Bearer " + tokSecrets})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `scope="read"`) {
			t.Fatalf("resources/read without read = %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	})

	t.Run("operations", func(t *testing.T) {
		docOps := docOperations(t)
		for _, d := range append(slices.Clone(resources), templates...) {
			if got := docOps[d.op]; got != "GET "+d.path {
				t.Errorf("%s: operation %s is %q in the document, want GET %s", d.uri, d.op, got, d.path)
			}
		}
	})

	t.Run("prompts", func(t *testing.T) {
		pl, err := cs.ListPrompts(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, p := range pl.Prompts {
			names = append(names, p.Name)
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"onboard-user", "review-routing", "troubleshoot-call"}) {
			t.Fatalf("prompts %v", names)
		}
		tools := toolNames(t)
		toolRe := regexp.MustCompile("`([a-z][A-Za-z0-9]+)`")
		uriRe := regexp.MustCompile(`hello://[A-Za-z0-9/{}]+[A-Za-z0-9}]`)
		for _, c := range []struct {
			name string
			args map[string]string
		}{
			{"troubleshoot-call", map[string]string{"number": "201"}},
			{"troubleshoot-call", map[string]string{"cdrId": "42"}},
			{"onboard-user", map[string]string{"number": "201", "name": "Ann", "phoneMac": "001565aabbcc", "vendor": "yealink"}},
			{"review-routing", nil},
		} {
			res, err := cs.GetPrompt(ctx, &sdk.GetPromptParams{Name: c.name, Arguments: c.args})
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			text := res.Messages[0].Content.(*sdk.TextContent).Text
			named := toolRe.FindAllStringSubmatch(text, -1)
			if len(named) == 0 {
				t.Errorf("%s names no tool", c.name)
			}
			for _, m := range named {
				if !strings.ContainsAny(m[1], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && docOperations(t)[m[1]] == "" {
					continue // a parameter name such as `failed`
				}
				if !tools[m[1]] {
					t.Errorf("%s names tool %s, which is not exposed", c.name, m[1])
				}
			}
			for _, u := range uriRe.FindAllString(text, -1) {
				if !knownURI(u) {
					t.Errorf("%s names resource %s, which does not exist", c.name, u)
				}
			}
		}
		if _, err := cs.GetPrompt(ctx, &sdk.GetPromptParams{Name: "troubleshoot-call"}); err == nil {
			t.Error("troubleshoot-call without number or cdrId succeeded")
		}
	})
}

// docOperations maps the embedded document's operationIds to "METHOD path".
func docOperations(t *testing.T) map[string]string {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(api.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for path, item := range doc.Paths {
		for method, raw := range item {
			var op struct {
				OperationID string `json:"operationId"`
			}
			if json.Unmarshal(raw, &op) == nil && op.OperationID != "" {
				out[op.OperationID] = strings.ToUpper(method) + " " + path
			}
		}
	}
	return out
}

// toolNames are the tools the server exposes for the embedded document;
// until apispec fills its operations (plan Task 2) the document's
// operationIds stand in.
func toolNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	ops := loadSpec(t).Operations()
	if len(ops) == 0 {
		for id := range docOperations(t) {
			out[id] = true
		}
		return out
	}
	tools, _, err := buildTools(ops)
	if err != nil {
		t.Fatal(err)
	}
	for id := range tools {
		out[id] = true
	}
	return out
}

func knownURI(u string) bool {
	for _, d := range append(slices.Clone(resources), templates...) {
		if d.uri == u {
			return true
		}
		if id, ok := strings.CutPrefix(u, strings.TrimSuffix(d.uri, "{id}")); ok && strings.HasSuffix(d.uri, "{id}") && id != "" && !strings.Contains(id, "/") {
			return true
		}
	}
	return false
}
