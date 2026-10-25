package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// TestValidateIVRMenu covers the field checks that need no database.
func TestValidateIVRMenu(t *testing.T) {
	good := func() store.IVRMenuInput {
		return store.IVRMenuInput{
			Name: "main", Number: "800", Enabled: true, GreetingID: 1, DigitTimeout: 5, InterDigitTimeout: 3, MaxRetries: 3,
			Timeout: store.IVRDestination{Kind: "hangup"}, Invalid: store.IVRDestination{Kind: "hangup"},
			Options: []store.IVROption{{Digit: "1", Kind: "extension", Target: "101"}, {Digit: "#", Kind: "paging", Target: "*60"}},
		}
	}
	if f := validateIVRMenu(ptr(good())); len(f) > 0 {
		t.Fatalf("a good menu failed: %v", f)
	}
	cases := map[string]func(in *store.IVRMenuInput){
		"bad name":            func(in *store.IVRMenuInput) { in.Name = "a b" },
		"bad number":          func(in *store.IVRMenuInput) { in.Number = "8x" },
		"no greeting":         func(in *store.IVRMenuInput) { in.GreetingID = 0 },
		"digit timeout":       func(in *store.IVRMenuInput) { in.DigitTimeout = 0 },
		"inter-digit timeout": func(in *store.IVRMenuInput) { in.InterDigitTimeout = 31 },
		"retries":             func(in *store.IVRMenuInput) { in.MaxRetries = 11 },
		"duplicate digit": func(in *store.IVRMenuInput) {
			in.Options = append(in.Options, store.IVROption{Digit: "1", Kind: "hangup"})
		},
		"bad digit":          func(in *store.IVRMenuInput) { in.Options[0].Digit = "12" },
		"bad kind":           func(in *store.IVRMenuInput) { in.Options[0].Kind = "fax" },
		"hangup with target": func(in *store.IVRMenuInput) { in.Options[0] = store.IVROption{Digit: "1", Kind: "hangup", Target: "1"} },
		"extension shape":    func(in *store.IVRMenuInput) { in.Options[0].Target = "x" },
		"page code shape":    func(in *store.IVRMenuInput) { in.Options[1].Target = "60" },
		"timeout shape":      func(in *store.IVRMenuInput) { in.Timeout = store.IVRDestination{Kind: "external", Target: "x"} },
		"too many options": func(in *store.IVRMenuInput) {
			for i := 0; i < 12; i++ {
				in.Options = append(in.Options, store.IVROption{Digit: "2", Kind: "hangup"})
			}
		},
	}
	for name, mut := range cases {
		in := good()
		mut(&in)
		if f := validateIVRMenu(&in); len(f) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestIVRMenuCRUD: create, read, replace and delete a menu; the references
// that must exist do, duplicate digits and loops are refused, and a menu
// that something names cannot be deleted or renamed. It fails if an unknown
// announcement or destination is stored, a timeout loop is accepted, or a
// referenced menu can be deleted.
func TestIVRMenuCRUD(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	ctx := context.Background()
	greet, err := e.st.CreateAnnouncement(ctx, "test", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := e.st.CreateAnnouncement(ctx, "test", "bad", "ann/bad.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	opt := func(d, kind, target string) map[string]any {
		return map[string]any{"digit": d, "kind": kind, "target": target}
	}
	good := map[string]any{
		"name": "main", "number": "800", "greetingId": greet.ID, "invalidPromptId": bad.ID,
		"options": []map[string]any{opt("1", "extension", "101"), opt("2", "voicemail", "102"), opt("3", "announcement", "bad")},
	}
	created := c.must(http.StatusCreated, "POST", "/api/v1/ivr-menus", good).json(t)
	id := fmt.Sprint(created["id"])
	if created["digitTimeout"] != float64(5) || created["maxRetries"] != float64(3) || created["enabled"] != true ||
		created["greetingName"] != "greet" || created["invalidPromptName"] != "bad" || created["timeoutPromptId"] != nil {
		t.Fatalf("created = %v", created)
	}
	if opts := created["options"].([]any); len(opts) != 3 {
		t.Fatalf("options = %v", opts)
	}
	if tm := created["timeout"].(map[string]any); tm["kind"] != "hangup" {
		t.Fatalf("timeout = %v", tm)
	}

	// Unique name and number.
	c.must(http.StatusConflict, "POST", "/api/v1/ivr-menus", good)
	c.must(http.StatusConflict, "POST", "/api/v1/ivr-menus", map[string]any{"name": "other", "number": "800", "greetingId": greet.ID})
	// A number that is an extension is refused.
	c.must(http.StatusBadRequest, "POST", "/api/v1/ivr-menus", map[string]any{"name": "x", "number": "101", "greetingId": greet.ID})

	// References must exist.
	for name, body := range map[string]map[string]any{
		"unknown greeting":  {"name": "a", "greetingId": 999999},
		"unknown invalid":   {"name": "a", "greetingId": greet.ID, "invalidPromptId": 999999},
		"unknown extension": {"name": "a", "greetingId": greet.ID, "options": []map[string]any{opt("1", "extension", "999")}},
		"unknown group":     {"name": "a", "greetingId": greet.ID, "options": []map[string]any{opt("1", "ring_group", "nope")}},
		"unknown page":      {"name": "a", "greetingId": greet.ID, "options": []map[string]any{opt("1", "paging", "*99")}},
		"unknown menu":      {"name": "a", "greetingId": greet.ID, "timeout": map[string]any{"kind": "ivr", "target": "ghost"}},
		"duplicate digit":   {"name": "a", "greetingId": greet.ID, "options": []map[string]any{opt("1", "hangup", ""), opt("1", "hangup", "")}},
		"unknown property":  {"name": "a", "greetingId": greet.ID, "extra": 1},
		"missing greeting":  {"name": "a"},
	} {
		if r := c.do("POST", "/api/v1/ivr-menus", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}

	// Nesting: a second menu reached from an option of the first; its
	// timeout may not lead back through automatic exits.
	sub := c.must(http.StatusCreated, "POST", "/api/v1/ivr-menus", map[string]any{
		"name": "sub", "greetingId": greet.ID, "timeout": map[string]any{"kind": "ivr", "target": "main"},
	}).json(t)
	subPath := "/api/v1/ivr-menus/" + fmt.Sprint(sub["id"])
	put := good
	put["timeout"] = map[string]any{"kind": "ivr", "target": "sub"}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/ivr-menus/"+id, put) // main -> sub -> main on silence
	put["timeout"] = map[string]any{"kind": "hangup", "target": ""}
	put["options"] = []map[string]any{opt("0", "ivr", "main"), opt("9", "ivr", "sub")} // keys may loop
	got := c.must(http.StatusOK, "PUT", "/api/v1/ivr-menus/"+id, put).json(t)
	if opts := got["options"].([]any); len(opts) != 2 {
		t.Fatalf("options after replace = %v", opts)
	}
	c.must(http.StatusBadRequest, "PUT", subPath, map[string]any{"name": "sub", "greetingId": greet.ID, "timeout": map[string]any{"kind": "ivr", "target": "sub"}})
	c.must(http.StatusNotFound, "PUT", "/api/v1/ivr-menus/987654", good)

	// A menu something names cannot be deleted or renamed.
	c.must(http.StatusConflict, "DELETE", subPath, nil)
	c.must(http.StatusConflict, "PUT", subPath, map[string]any{"name": "sub2", "greetingId": greet.ID})
	// An announcement a menu plays cannot be deleted.
	if _, err := e.st.DeleteAnnouncement(ctx, "test", greet.ID, nil); err == nil {
		t.Fatal("an announcement a menu plays was deleted")
	}

	// Ring group failover, feature codes and inbound routes name menus.
	member := []map[string]any{{"extensionId": e.ext101["id"], "position": 1}}
	c.must(http.StatusBadRequest, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "g", "members": member, "failureKind": "ivr", "failureTarget": "ghost"})
	rg := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "g", "members": member, "failureKind": "ivr", "failureTarget": "main"}).json(t)
	c.must(http.StatusConflict, "DELETE", "/api/v1/ivr-menus/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ring-groups/%v", rg["id"]), nil)
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*88", "action": "ivr", "argument": "ghost"}}})
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*88", "action": "ivr", "argument": "main"}}})
	c.must(http.StatusConflict, "DELETE", "/api/v1/ivr-menus/"+id, nil)
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{}})
	c.must(http.StatusBadRequest, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "in", "didKind": "any", "destinationKind": "ivr", "destination": "ghost"})
	c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "in", "didKind": "any", "destinationKind": "ivr", "destination": "main"})

	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/ivr-menus", nil).body, &list); err != nil || len(list.Items) != 2 {
		t.Fatalf("list = %v, %v", list.Items, err)
	}
	c.must(http.StatusOK, "GET", "/api/v1/ivr-menus/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/ivr-menus/987654", nil)
}
