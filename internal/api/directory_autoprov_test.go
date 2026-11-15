package api

// Range auto-provisioning on the directory routes (spec integrations I-11,
// P12): the connector setting and the range usage.

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/azrtydxb/hello/internal/directory"
)

func TestDirectoryAutoProvision(t *testing.T) {
	e := newDirEnv(t)
	c := e.connector(t, map[string]any{"autoProvision": map[string]any{"enabled": true, "rangeStart": "2000", "rangeEnd": "2009"}})
	ap := c["autoProvision"].(map[string]any)
	if ap["enabled"] != true || ap["device"] != true || ap["voicemail"] != true || ap["rangeStart"] != "2000" {
		t.Fatalf("autoProvision = %v", ap)
	}
	e.mem.AddExt("2000", false)
	e.drv.res = &directory.Result{Users: []directory.SourceUser{srcUser("a", "ann@corp", "ann@corp.org", "Ann")}}
	pre := e.run(t, 1, true)
	changes := wantJSON(t, e.admin("GET", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/changes?action=create", nil), http.StatusOK, "changes")
	row := changes["items"].([]any)[0].(map[string]any)
	diffs := row["fieldDiffs"].(map[string]any)
	if row["reason"] != "auto_provisioned" || diffs["extension"].(map[string]any)["after"] != "2001" || diffs["voicemail"].(map[string]any)["after"] != "ann@corp.org" {
		t.Fatalf("row = %v", row)
	}
	wantJSON(t, e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/apply", map[string]any{}), http.StatusOK, "apply")

	u := wantJSON(t, e.admin("GET", "/api/v1/directory/connectors/1/range", nil), http.StatusOK, "range")
	if u["size"] != float64(10) || u["used"] != float64(2) || u["free"] != float64(8) || u["nextFree"] != "2002" {
		t.Fatalf("range = %v", u)
	}
	prov := u["provisioned"].([]any)
	if len(prov) != 1 || prov[0].(map[string]any)["number"] != "2001" || prov[0].(map[string]any)["username"] != "ann@corp" {
		t.Fatalf("provisioned = %v", prov)
	}
	if rec := e.admin("GET", "/api/v1/directory/connectors/9/range", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("range of unknown: %d", rec.Code)
	}
	// Turning it off keeps the range; a bad range or an unknown key is refused.
	off := wantJSON(t, e.admin("PATCH", "/api/v1/directory/connectors/1", map[string]any{"autoProvision": map[string]any{"enabled": false, "rangeStart": "2000", "rangeEnd": "2009"}}), http.StatusOK, "off")
	if off["autoProvision"].(map[string]any)["enabled"] != false {
		t.Fatalf("off = %v", off)
	}
	for name, ap := range map[string]map[string]any{
		"reversed": {"enabled": true, "rangeStart": "2009", "rangeEnd": "2000"},
		"width":    {"enabled": true, "rangeStart": "200", "rangeEnd": "2000"},
		"unknown":  {"enabled": true, "rangeStart": "2000", "rangeEnd": "2009", "nope": true},
	} {
		if rec := e.admin("PATCH", "/api/v1/directory/connectors/1", map[string]any{"autoProvision": ap}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
