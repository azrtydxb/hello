package directory

import "testing"

func TestAttrMapTransforms(t *testing.T) {
	u := SourceUser{Attrs: map[string][]string{
		"ipphone":         {""},
		"telephonenumber": {"+41 (21) 555-2001"},
		"mobile":          {"ext 4711 "},
	}}
	cases := []struct {
		name string
		m    AttrMap
		want string
	}{
		{"fallback to the next attribute", AttrMap{Sources: []string{"ipPhone", "telephoneNumber"}, Transforms: []Transform{{Kind: TransformDigits}, {Kind: TransformLast, N: 4}}}, "2001"},
		{"regex first capture", AttrMap{Sources: []string{"mobile"}, Transforms: []Transform{{Kind: TransformRegex, Pattern: `ext (\d+)`}}}, "4711"},
		{"a regex that does not match counts as empty", AttrMap{Sources: []string{"mobile", "telephoneNumber"}, Transforms: []Transform{{Kind: TransformRegex, Pattern: `\(21\) \d+-(\d{4})$`}}}, "2001"},
		{"no source", AttrMap{}, ""},
	}
	for _, c := range cases {
		if got := c.m.eval(u); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMappedExtensionValidity(t *testing.T) {
	m := Mappings{Extension: AttrMap{Sources: []string{"n"}}}
	for v, bad := range map[string]bool{"2001": false, "1": true, "12345678901": true, "20a1": true, "": false} {
		got := m.Apply(SourceUser{Attrs: map[string][]string{"n": {v}}})
		if (got.ExtensionError != "") != bad {
			t.Errorf("%q: error %q", v, got.ExtensionError)
		}
	}
}

func TestActiveMapping(t *testing.T) {
	at := func(mode, v string) bool {
		return ActiveMap{Attribute: "a", Mode: mode}.eval(SourceUser{Attrs: map[string][]string{"a": {v}}})
	}
	if at(ActiveUAC, "514") || !at(ActiveUAC, "512") || !at(ActiveUAC, "66048") {
		t.Error("uac bit 2")
	}
	if at(ActiveBool, "FALSE") || !at(ActiveBool, "true") || at(ActiveNotBool, "true") || !at(ActiveNotBool, "false") {
		t.Error("bool")
	}
	// A directory that does not say is never a reason to disable.
	if !(ActiveMap{Attribute: "missing", Mode: ActiveUAC}).eval(SourceUser{}) || !at(ActiveUAC, "garbage") || !at(ActiveBool, "maybe") {
		t.Error("unknown reads as active")
	}
}

func TestGroupRules(t *testing.T) {
	g := GroupRules{
		Roles:      []RoleRule{{GroupID: "a", Role: "viewer"}, {GroupID: "b", Role: "admin"}, {GroupID: "c", Role: "operator"}},
		RingGroups: []RingGroupRule{{GroupID: "a", RingGroupID: 9}, {GroupID: "c", RingGroupID: 3}, {GroupID: "c", RingGroupID: 9}},
	}
	if got := g.Role([]string{"a", "c"}, false); got != "operator" {
		t.Errorf("highest wins: %q", got)
	}
	if got := g.Role([]string{"b"}, false); got != "user" {
		t.Errorf("admin without the switch: %q", got)
	}
	if got := g.Role([]string{"b", "a"}, false); got != "viewer" {
		t.Errorf("admin rule skipped, next highest: %q", got)
	}
	if got := g.Role([]string{"b", "a"}, true); got != "admin" {
		t.Errorf("admin with the switch: %q", got)
	}
	if got := g.Role(nil, false); got != "user" {
		t.Errorf("default: %q", got)
	}
	if got := g.RingGroupIDs([]string{"a", "c"}); len(got) != 2 || got[0] != 3 || got[1] != 9 {
		t.Errorf("ring groups: %v", got)
	}
}

func TestManagedFields(t *testing.T) {
	m := Mappings{Email: AttrMap{Sources: []string{"mail"}}, Extension: AttrMap{Sources: []string{"x"}}}
	got := m.Managed(GroupRules{})
	if len(got) != 2 || got[0] != "email" || got[1] != "extension" {
		t.Errorf("managed = %v", got)
	}
	if got := m.Managed(GroupRules{Roles: []RoleRule{{GroupID: "g", Role: "viewer"}}}); got[len(got)-1] != "role" {
		t.Errorf("managed with rules = %v", got)
	}
}
