package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// TestValidateVoicemailGroup covers the field checks that need no database.
func TestValidateVoicemailGroup(t *testing.T) {
	good := func() store.VoicemailGroupInput {
		return store.VoicemailGroupInput{Number: "700", Name: "Sales", Enabled: true, MaxSeconds: 180, Members: []int64{1, 2}}
	}
	if f := validateVoicemailGroup(ptr(good())); len(f) > 0 {
		t.Fatalf("a good group failed: %v", f)
	}
	cases := map[string]func(in *store.VoicemailGroupInput){
		"short number":    func(in *store.VoicemailGroupInput) { in.Number = "7" },
		"letters":         func(in *store.VoicemailGroupInput) { in.Number = "70a" },
		"star":            func(in *store.VoicemailGroupInput) { in.Number = "*63" },
		"no name":         func(in *store.VoicemailGroupInput) { in.Name = "" },
		"long name":       func(in *store.VoicemailGroupInput) { in.Name = string(make([]byte, 65)) },
		"control in name": func(in *store.VoicemailGroupInput) { in.Name = "a\nb" },
		"too short":       func(in *store.VoicemailGroupInput) { in.MaxSeconds = 9 },
		"too long":        func(in *store.VoicemailGroupInput) { in.MaxSeconds = 601 },
		"no members":      func(in *store.VoicemailGroupInput) { in.Members = nil },
		"dup member":      func(in *store.VoicemailGroupInput) { in.Members = []int64{1, 1} },
		"bad member":      func(in *store.VoicemailGroupInput) { in.Members = []int64{0} },
		"too many members": func(in *store.VoicemailGroupInput) {
			in.Members = nil
			for i := int64(1); i <= store.MaxVoicemailGroupMembers+1; i++ {
				in.Members = append(in.Members, i)
			}
		},
	}
	for name, mut := range cases {
		in := good()
		mut(&in)
		if f := validateVoicemailGroup(&in); len(f) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	// The limits themselves are accepted.
	in := good()
	in.MaxSeconds = store.MaxVoicemailGroupSeconds
	in.Members = nil
	for i := int64(1); i <= store.MaxVoicemailGroupMembers; i++ {
		in.Members = append(in.Members, i)
	}
	if f := validateVoicemailGroup(&in); len(f) > 0 {
		t.Errorf("limits refused: %v", f)
	}
}

// TestVoicemailGroupCRUD: create, list, replace and delete a voicemail group.
// It fails if a duplicate number or name, a number
// that is an extension or ring group, or a group an IVR menu points at is
// accepted or removed, or if a replace leaves the old members behind.
func TestVoicemailGroupCRUD(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	m1 := json.Number(fmt.Sprint(e.ext101["id"]))
	m2 := json.Number(fmt.Sprint(e.ext102["id"]))

	body := func(number, name string, members ...json.Number) map[string]any {
		return map[string]any{"number": number, "name": name, "members": members}
	}
	created := c.must(http.StatusCreated, "POST", "/api/v1/voicemail-groups", body("700", "Sales", m1, m2)).json(t)
	id := fmt.Sprint(int64(created["id"].(float64)))
	if ms := created["members"].([]any); len(ms) != 2 || created["maxSeconds"].(float64) != 180 || created["enabled"] != true {
		t.Fatalf("created = %v", created)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/voicemail-groups", body("700", "Other", m1))
	c.must(http.StatusConflict, "POST", "/api/v1/voicemail-groups", body("701", "Sales", m1))
	// The number is in the dial space: it cannot be an extension number.
	if r := c.do("POST", "/api/v1/voicemail-groups", body("101", "Clash", m1)); r.code != http.StatusBadRequest {
		t.Errorf("number of an extension: status %d, want 400", r.code)
	}
	if r := c.do("POST", "/api/v1/extensions", map[string]string{"number": "700", "name": "Clash"}); r.code != http.StatusBadRequest {
		t.Errorf("extension on a group number: status %d, want 400", r.code)
	}
	for name, b := range map[string]map[string]any{
		"no members":     body("702", "A"),
		"unknown member": {"number": "702", "name": "A", "members": []int{999999}},
		"bad number":     body("7", "A", m1),
		"missing name":   {"number": "702", "members": []json.Number{m1}},
		"extra":          {"number": "702", "name": "A", "members": []json.Number{m1}, "extra": 1},
	} {
		if r := c.do("POST", "/api/v1/voicemail-groups", b); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/voicemail-groups", nil).body, &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list = %v, %v", list.Items, err)
	}

	replaced := c.must(http.StatusOK, "PUT", "/api/v1/voicemail-groups/"+id, map[string]any{
		"number": "700", "name": "Support", "members": []json.Number{m2}, "maxSeconds": 600, "enabled": false}).json(t)
	if ms := replaced["members"].([]any); replaced["name"] != "Support" || len(ms) != 1 || ms[0].(map[string]any)["number"] != "102" ||
		replaced["maxSeconds"].(float64) != 600 || replaced["enabled"] != false {
		t.Fatalf("replaced = %v", replaced)
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/voicemail-groups/987654", body("703", "X", m2))

	// A menu that sends calls to the group pins it.
	greet, err := e.st.CreateAnnouncement(context.Background(), "test", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	menu := c.must(http.StatusCreated, "POST", "/api/v1/ivr-menus", map[string]any{
		"name": "main", "number": "800", "greetingId": greet.ID,
		"options": []map[string]any{{"digit": "1", "kind": "voicemail_group", "target": "700"}},
	}).json(t)
	c.must(http.StatusConflict, "DELETE", "/api/v1/voicemail-groups/"+id, nil)
	if r := c.do("PUT", "/api/v1/voicemail-groups/"+id, map[string]any{"number": "710", "name": "Support", "members": []json.Number{m2}}); r.code != http.StatusConflict {
		t.Errorf("renumber of a referenced group: status %d, want 409", r.code)
	}
	if r := c.do("POST", "/api/v1/ivr-menus", map[string]any{"name": "bad", "greetingId": greet.ID,
		"options": []map[string]any{{"digit": "1", "kind": "voicemail_group", "target": "799"}}}); r.code != http.StatusBadRequest {
		t.Errorf("menu naming an unknown group: status %d, want 400", r.code)
	}
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ivr-menus/%v", menu["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/voicemail-groups/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/voicemail-groups/"+id, nil)
}
