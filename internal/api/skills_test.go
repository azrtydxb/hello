package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/skills"
)

// TestSkillsDownload fails if the list differs from the embedded skills, a
// download is not a zip whose files equal the skill folder, an unknown name
// is not 404, or the routes are reachable without credentials or documented
// below the read scope (spec ai-external-access S-18; scope enforcement for
// every route is TestScopeEnforcement's).
func TestSkillsDownload(t *testing.T) {
	h := Handler(Config{Store: tokenStore{}})
	get := func(path string, bearer bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer {
			req.Header.Set("Authorization", "Bearer anything")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	want, err := skills.List()
	if err != nil || len(want) == 0 {
		t.Fatalf("embedded skills = %v, %v", want, err)
	}
	rec := get("/api/v1/skills", true)
	var got struct{ Items []skills.Skill }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("GET /skills = %d %s", rec.Code, rec.Body)
	}
	if !reflect.DeepEqual(got.Items, want) {
		t.Fatalf("GET /skills = %+v, want %+v", got.Items, want)
	}

	for _, s := range want {
		rec := get("/api/v1/skills/"+s.Name+"/download", true)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("download %s = %d %q", s.Name, rec.Code, rec.Header().Get("Content-Type"))
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="`+s.Name+`.zip"`) {
			t.Fatalf("download %s Content-Disposition = %q", s.Name, cd)
		}
		zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if err != nil {
			t.Fatalf("download %s is not a zip: %v", s.Name, err)
		}
		inZip := map[string][]byte{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			inZip[f.Name], _ = io.ReadAll(rc)
			_ = rc.Close()
		}
		inFS := map[string][]byte{}
		_ = fs.WalkDir(skills.FS, s.Name, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				inFS[p], _ = fs.ReadFile(skills.FS, p)
			}
			return err
		})
		if len(inFS) < 2 || !reflect.DeepEqual(inZip, inFS) {
			t.Fatalf("download %s holds %d files, the folder %d, or their contents differ", s.Name, len(inZip), len(inFS))
		}
	}

	for _, name := range []string{"nope", "%2E%2E", "hello-setup%2FSKILL.md", "references"} {
		if rec := get("/api/v1/skills/"+name+"/download", true); rec.Code != http.StatusNotFound {
			t.Fatalf("download %q = %d, want 404", name, rec.Code)
		}
	}

	for _, p := range []string{"/api/v1/skills", "/api/v1/skills/hello-setup/download"} {
		if rec := get(p, false); rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without credentials = %d, want 401", p, rec.Code)
		}
	}
	for _, rt := range Routes() {
		if strings.HasPrefix(rt.Pattern, "/api/v1/skills") && (rt.Scope != auth.ScopeRead || rt.Public) {
			t.Fatalf("%s %s: scope %q public %v, want read and protected", rt.Method, rt.Pattern, rt.Scope, rt.Public)
		}
	}
}
