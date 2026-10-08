package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/azrtydxb/hello/internal/apispec"
)

// conformanceExempt lists documented statuses the package suite cannot
// produce through its fakes, as "operationId status", each with the reason.
// 401, 500 and 503 are exempt everywhere (spec ai-external-access S-2): 401
// is the middleware's and TestVersionAndOpenAPI covers it for every
// operation, 500 is a store failure, and 503 is a dependency being down.
var conformanceExempt = func() map[string]string {
	const why = "the proposal routes need the database-backed store; internal/ai/proposal drives them against PostgreSQL"
	m := map[string]string{}
	for _, k := range []string{"listAIProposals 200", "listAIProposals 400", "getAIProposal 200", "getAIProposal 404",
		"applyAIProposal 200", "applyAIProposal 404", "applyAIProposal 409",
		"dismissAIProposal 200", "dismissAIProposal 400", "dismissAIProposal 404", "dismissAIProposal 409"} {
		m[k] = why
	}
	return m
}()

// conformance validates every request the package suite sends through
// Handler, and every response, against openapi.json (spec S-2).
type conformance struct {
	spec *apispec.Spec
	ops  map[string]apispec.Operation // by "METHOD /pattern"
	mux  *http.ServeMux               // matches a request to its pattern
	// pending is the "METHOD /pattern" of every route whose handler has not
	// landed (answers 501): not validated, and its statuses not required.
	pending map[string]bool

	mu         sync.Mutex
	schemas    map[string]*jsonschema.Resolved
	observed   map[string]bool // "operationId status"
	violations map[string]bool
}

func newConformance(doc []byte) (*conformance, error) {
	spec, err := apispec.Load(doc)
	if err != nil {
		return nil, err
	}
	c := &conformance{
		spec:       spec,
		ops:        map[string]apispec.Operation{},
		mux:        http.NewServeMux(),
		pending:    pendingRoutes(),
		schemas:    map[string]*jsonschema.Resolved{},
		observed:   map[string]bool{},
		violations: map[string]bool{},
	}
	for _, op := range spec.Operations() {
		key := op.Method + " " + op.Path
		c.ops[key] = op
		c.mux.Handle(key, http.NotFoundHandler())
	}
	return c, nil
}

// conf is the validator TestMain installs; nil when it is not installed.
var conf *conformance

func TestMain(m *testing.M) {
	flag.Parse()
	c, err := newConformance(openAPI)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi.json:", err)
		os.Exit(1)
	}
	conf = c
	wrapForTest = c.wrap
	code := m.Run()
	if code == 0 && !testing.RunTests(func(_, _ string) (bool, error) { return true, nil },
		[]testing.InternalTest{{Name: "TestOpenAPIConformance", F: c.check}}) {
		code = 1
	}
	os.Exit(code)
}

// check is TestOpenAPIConformance, run after the rest of the suite: it fails
// on every violation the validator recorded and, when the whole suite ran
// with its databases, on every documented status nothing produced.
func (c *conformance) check(t *testing.T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range sortedKeys(c.violations) {
		t.Error(v)
	}
	full := os.Getenv("HELLO_TEST_DATABASE_URL") != "" && os.Getenv("HELLO_TEST_MINIO_ENDPOINT") != "" &&
		os.Getenv("HELLO_TEST_VALKEY_ADDR") != "" && flag.Lookup("test.run").Value.String() == "" &&
		flag.Lookup("test.skip").Value.String() == "" && !testing.Short()
	if !full {
		t.Log("coverage of documented statuses not checked: a partial run, or the test databases are not set")
		return
	}
	for _, op := range c.spec.Operations() {
		if c.pending[op.Method+" "+op.Path] {
			continue
		}
		for status := range c.spec.Responses(op.ID) {
			key := op.ID + " " + status
			switch {
			case status == "401", status == "500", status == "503", status == "default":
			case conformanceExempt[key] != "":
			case !c.observed[key]:
				t.Errorf("%s %s (%s): documented %s never observed", op.Method, op.Path, op.ID, status)
			}
		}
	}
}

func (c *conformance) violate(format string, args ...any) {
	c.mu.Lock()
	c.violations[fmt.Sprintf(format, args...)] = true
	c.mu.Unlock()
}

// wrap is Handler's test wrapper.
func (c *conformance) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := c.mux.Handler(r)
		op, ok := c.ops[pattern]
		if !ok || c.pending[pattern] {
			// Not a documented operation: the mux answers it (404/405),
			// or it is a route still pending its stream's handler, which
			// answers 501 whatever the document says.
			next.ServeHTTP(w, r)
			return
		}
		sent, err := io.ReadAll(r.Body)
		if err != nil {
			c.violate("%s: reading the request body: %v", op.ID, err)
		}
		_ = r.Body.Close()
		body := &readTracker{Reader: bytes.NewReader(sent)}
		r.Body = body
		rec := &teeWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		c.mu.Lock()
		c.observed[op.ID+" "+strconv.Itoa(status)] = true
		c.mu.Unlock()

		content := c.spec.RequestContent(op.ID)
		if body.read && len(content) == 0 {
			c.violate("%s: the handler reads a request body but none is documented", op.ID)
		}
		if len(sent) > 0 && len(content) > 0 && status >= 200 && status < 300 {
			// A body the API accepted must be one the document allows;
			// a rejected one is the suite probing validation.
			c.checkRequest(op, r.Header.Get("Content-Type"), sent, content)
		}
		c.checkResponse(op, status, rec)
	})
}

func (c *conformance) checkRequest(op apispec.Operation, contentType string, sent []byte, content map[string]map[string]any) {
	mt, params, _ := mime.ParseMediaType(contentType)
	if mt == "" {
		// The handlers read JSON (or their one documented form) whatever
		// the header says; the suite's JSON client sends none.
		if _, ok := content["application/json"]; ok || len(content) != 1 {
			mt = "application/json"
		} else {
			mt = sortedKeys(content)[0]
		}
	}
	schema, ok := content[mt]
	if !ok {
		c.violate("%s: request content type %q is not documented (%s)", op.ID, mt, strings.Join(sortedKeys(content), ", "))
		return
	}
	switch mt {
	case "application/json":
		var v any
		if err := json.Unmarshal(sent, &v); err != nil {
			c.violate("%s: accepted a request body that is not JSON: %v", op.ID, err)
			return
		}
		if err := c.validate(op.ID+" request", schema, v); err != nil {
			c.violate("%s: accepted request body %s: %v", op.ID, clip(sent), err)
		}
	case "multipart/form-data":
		props, _ := schema["properties"].(map[string]any)
		mr := multipart.NewReader(bytes.NewReader(sent), params["boundary"])
		var names []string
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			names = append(names, p.FormName())
			if _, ok := props[p.FormName()]; !ok {
				c.violate("%s: accepted undocumented multipart part %q", op.ID, p.FormName())
			}
		}
		req, _ := schema["required"].([]any)
		for _, n := range req {
			if !slices.Contains(names, n.(string)) {
				c.violate("%s: accepted a multipart body without the required part %q", op.ID, n)
			}
		}
	}
}

func (c *conformance) checkResponse(op apispec.Operation, status int, rec *teeWriter) {
	responses := c.spec.Responses(op.ID)
	code := strconv.Itoa(status)
	schema, ok := responses[code]
	if !ok {
		schema, ok = responses["default"]
	}
	if !ok {
		c.violate("%s: status %d is not documented: %s", op.ID, status, clip(rec.body.Bytes()))
		return
	}
	mt, _, _ := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if mt != "application/json" {
		return
	}
	if schema == nil {
		c.violate("%s: %d answers a JSON body the document does not describe", op.ID, status)
		return
	}
	if rec.clipped {
		return
	}
	var v any
	if err := json.Unmarshal(rec.body.Bytes(), &v); err != nil {
		c.violate("%s: %d body is not JSON: %v", op.ID, status, err)
		return
	}
	if err := c.validate(op.ID+" "+code, schema, v); err != nil {
		c.violate("%s: %d body %s: %v", op.ID, status, clip(rec.body.Bytes()), err)
	}
}

// validate checks v against an inlined schema, resolving each schema once.
func (c *conformance) validate(key string, schema map[string]any, v any) error {
	c.mu.Lock()
	rs, ok := c.schemas[key]
	c.mu.Unlock()
	if !ok {
		raw, err := json.Marshal(schema)
		if err != nil {
			return err
		}
		var s jsonschema.Schema
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
		if rs, err = s.Resolve(nil); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
		c.mu.Lock()
		c.schemas[key] = rs
		c.mu.Unlock()
	}
	return rs.Validate(v)
}

// readTracker records whether the handler read the request body.
type readTracker struct {
	io.Reader
	read bool
}

func (r *readTracker) Read(p []byte) (int, error) {
	r.read = true
	return r.Reader.Read(p)
}

func (r *readTracker) Close() error { return nil }

// maxTee is how much of a response body the validator keeps; audio and
// exports beyond it are not JSON.
const maxTee = 1 << 20

// teeWriter passes a response through and keeps its status and the start
// of its body.
type teeWriter struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	clipped bool
}

func (w *teeWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *teeWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if room := maxTee - w.body.Len(); room > 0 {
		w.body.Write(p[:min(len(p), room)])
	}
	if len(p) > maxTee-w.body.Len() {
		w.clipped = true
	}
	return w.ResponseWriter.Write(p)
}

func (w *teeWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *teeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var spaces = regexp.MustCompile(`\s+`)

func clip(b []byte) string {
	s := spaces.ReplaceAllString(string(b), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestConformanceValidator fails if the validator lets through an
// undocumented status, a response body that does not match its schema, a
// body read by an operation without a requestBody, an accepted request
// body that does not match, or an undocumented multipart part; or if it
// flags a conforming exchange.
func TestConformanceValidator(t *testing.T) {
	doc := `{"openapi": "3.1.0", "paths": {
	  "/a": {"post": {"operationId": "a",
	    "requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {"n": {"type": "integer"}}}},
	                                "multipart/form-data": {"schema": {"type": "object", "properties": {"f": {"type": "string"}}}}}},
	    "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"type": "object", "required": ["ok"]}}}}}}},
	  "/b": {"get": {"operationId": "b", "responses": {"204": {"description": "none"}}}}}}`
	run := func(method, path, ct, body string, h http.HandlerFunc) []string {
		t.Helper()
		c, err := newConformance([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		c.wrap(h).ServeHTTP(httptest.NewRecorder(), req)
		return sortedKeys(c.violations)
	}
	okJSON := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
	multi := "--B\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\nv\r\n--B--\r\n"
	for _, tc := range []struct {
		name, method, path, ct, body string
		h                            http.HandlerFunc
		want                         string // a substring of the one violation; empty for none
	}{
		{"conforming", "POST", "/a", "", `{"n": 1}`, okJSON, ""},
		{"conforming multipart", "POST", "/a", "multipart/form-data; boundary=B", fmt.Sprintf(multi, "f"), okJSON, ""},
		{"rejected bad body", "POST", "/a", "", `{"n": "x"}`, func(w http.ResponseWriter, _ *http.Request) {
			badRequest(w, "no")
		}, "status 400 is not documented"},
		{"accepted bad body", "POST", "/a", "", `{"n": "x"}`, okJSON, "accepted request body"},
		{"undocumented part", "POST", "/a", "multipart/form-data; boundary=B", fmt.Sprintf(multi, "g"), okJSON, `undocumented multipart part "g"`},
		{"undocumented type", "POST", "/a", "text/plain", "x", okJSON, `content type "text/plain"`},
		{"bad response", "POST", "/a", "", `{}`, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]bool{"nope": true})
		}, "200 body"},
		{"body read without requestBody", "GET", "/b", "", "", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
		}, "reads a request body"},
		{"undocumented JSON body", "GET", "/b", "", "", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNoContent, map[string]bool{})
		}, "the document does not describe"},
		{"undocumented route", "GET", "/c", "", "", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}, ""},
	} {
		got := run(tc.method, tc.path, tc.ct, tc.body, tc.h)
		switch {
		case tc.want == "" && len(got) != 0:
			t.Errorf("%s: violations %v, want none", tc.name, got)
		case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)):
			t.Errorf("%s: violations %v, want one containing %q", tc.name, got, tc.want)
		}
	}
}
