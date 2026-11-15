package scim

import (
	"encoding/json"
	"reflect"
	"testing"
)

func ops(t *testing.T, s string) []patchOp {
	t.Helper()
	var p patchBody
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return p.Operations
}

func normalized(t *testing.T, typ, s string) map[string]any {
	t.Helper()
	d, err := normalize(typ, doc(t, s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNormalize(t *testing.T) {
	d := normalized(t, TypeUser, `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
		"id": "ignored", "meta": {"version": "x"},
		"USERNAME": "ann@corp.org", "Active": "False", "password": "hunter2!",
		"emails": [{"Value": "ann@corp.org", "Primary": "true", "type": "work"}],
		"phoneNumbers": "+41 21 555 1000",
		"name": {"givenName": "Ann", "nonsense": 1},
		"unknownAttr": "dropped",
		"groups": [{"value": "g"}],
		"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User": {"employeeNumber": 42, "department": "Sales"},
		"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:costCenter": "CC1",
		"urn:hello:params:scim:schemas:extension:pbx:2.0:User": {"extension": "2001"}
	}`)
	want := map[string]any{
		"userName": "ann@corp.org", "active": false,
		"emails":         []any{map[string]any{"value": "ann@corp.org", "primary": true, "type": "work"}},
		"phoneNumbers":   []any{map[string]any{"value": "+41 21 555 1000"}},
		"name":           map[string]any{"givenName": "Ann"},
		SchemaEnterprise: map[string]any{"employeeNumber": "42", "department": "Sales", "costCenter": "CC1"},
		SchemaPBX:        map[string]any{"extension": "2001"},
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("normalized =\n%#v\nwant\n%#v", d, want)
	}
	for _, bad := range []string{`{"active": "yes"}`, `{"name": "x"}`, `{"emails": [{"primary": "maybe"}]}`, `{"externalId": {}}`} {
		if _, err := normalize(TypeUser, doc(t, bad)); err == nil {
			t.Errorf("%s normalized", bad)
		}
	}
}

func TestPatchEntraShapes(t *testing.T) {
	d := normalized(t, TypeUser, `{"userName": "ann@corp.org", "active": true, "displayName": "Ann",
		"emails": [{"value": "ann@corp.org", "type": "work", "primary": true}]}`)
	err := applyPatch(TypeUser, d, ops(t, `{"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"], "Operations": [
		{"op": "Replace", "path": "displayName", "value": "Ann Smith"},
		{"op": "Replace", "path": "emails[type eq \"work\"].value", "value": "ann.smith@corp.org"},
		{"op": "Replace", "path": "phoneNumbers[type eq \"work\"].value", "value": "+41 21 555 2001"},
		{"op": "Add", "path": "name.givenName", "value": "Ann"},
		{"op": "Add", "path": "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager", "value": "boss-id"},
		{"op": "Replace", "path": "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department", "value": "Sales"},
		{"op": "Replace", "path": "active", "value": "False"},
		{"op": "Add", "value": {"name.familyName": "Smith", "urn:hello:params:scim:schemas:extension:pbx:2.0:User:extension": "2001"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"userName": "ann@corp.org", "active": false, "displayName": "Ann Smith",
		"emails":         []any{map[string]any{"value": "ann.smith@corp.org", "type": "work", "primary": true}},
		"phoneNumbers":   []any{map[string]any{"value": "+41 21 555 2001", "type": "work"}},
		"name":           map[string]any{"givenName": "Ann", "familyName": "Smith"},
		SchemaEnterprise: map[string]any{"manager": map[string]any{"value": "boss-id"}, "department": "Sales"},
		SchemaPBX:        map[string]any{"extension": "2001"},
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("patched =\n%#v\nwant\n%#v", d, want)
	}
	// Remove the manager and the department, then the extension object's
	// attribute; removing a value that is not there is not an error.
	if err := applyPatch(TypeUser, d, ops(t, `{"Operations": [
		{"op": "Remove", "path": "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager"},
		{"op": "Remove", "path": "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department"},
		{"op": "Remove", "path": "title"},
		{"op": "Remove", "path": "emails[type eq \"home\"]"}
	]}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := d[SchemaEnterprise]; ok {
		t.Fatalf("enterprise left: %v", d[SchemaEnterprise])
	}
}

func TestPatchOktaShapes(t *testing.T) {
	d := normalized(t, TypeUser, `{"userName": "bob@corp.org", "active": true}`)
	if err := applyPatch(TypeUser, d, ops(t, `{"Operations": [{"op": "replace", "value": {"active": false}}]}`)); err != nil {
		t.Fatal(err)
	}
	if d["active"] != false {
		t.Fatalf("active = %v", d["active"])
	}
	g := normalized(t, TypeGroup, `{"displayName": "Sales"}`)
	g["members"] = []any{map[string]any{"value": "u1"}}
	err := applyPatch(TypeGroup, g, ops(t, `{"Operations": [
		{"op": "replace", "value": {"id": "g1", "displayName": "Sales EMEA"}},
		{"op": "add", "path": "members", "value": [{"value": "u2", "display": "Bob"}, {"value": "u1"}]},
		{"op": "remove", "path": "members[value eq \"u1\"]"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if g["displayName"] != "Sales EMEA" || !reflect.DeepEqual(membersOf(g), []string{"u2"}) {
		t.Fatalf("group = %v", g)
	}
	// Entra removes members by listing them; replace sets the whole list.
	if err := applyPatch(TypeGroup, g, ops(t, `{"Operations": [
		{"op": "Add", "path": "members", "value": [{"value": "u3"}, {"value": "u4"}]},
		{"op": "Remove", "path": "members", "value": [{"value": "u2"}, {"value": "u4"}]}
	]}`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(membersOf(g), []string{"u3"}) {
		t.Fatalf("members = %v", membersOf(g))
	}
	if err := applyPatch(TypeGroup, g, ops(t, `{"Operations": [{"op": "replace", "path": "members", "value": [{"value": "u9"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(membersOf(g), []string{"u9"}) {
		t.Fatalf("members = %v", membersOf(g))
	}
}

func TestPatchErrors(t *testing.T) {
	cases := map[string]string{
		`{"Operations": []}`: typeInvalidSyntax,
		`{"Operations": [{"op": "move", "path": "title", "value": "x"}]}`:                          typeInvalidSyntax,
		`{"Operations": [{"op": "remove"}]}`:                                                       typeNoTarget,
		`{"Operations": [{"op": "add", "value": "x"}]}`:                                            typeInvalidValue,
		`{"Operations": [{"op": "replace", "path": "id", "value": "x"}]}`:                          typeMutability,
		`{"Operations": [{"op": "add", "path": "groups", "value": []}]}`:                           typeMutability,
		`{"Operations": [{"op": "remove", "path": "userName"}]}`:                                   typeMutability,
		`{"Operations": [{"op": "replace", "path": "active", "value": "x"}]}`:                      typeInvalidValue,
		`{"Operations": [{"op": "add", "path": "title[value eq \"x\"]", "value": "x"}]}`:           typeInvalidPath,
		`{"Operations": [{"op": "add", "path": "displayName.x", "value": "x"}]}`:                   typeInvalidPath,
		`{"Operations": [{"op": "replace", "path": "emails[type co \"w\"].value", "value": "x"}]}`: typeNoTarget,
	}
	for body, want := range cases {
		d := normalized(t, TypeUser, `{"userName": "x"}`)
		err := applyPatch(TypeUser, d, ops(t, body))
		if err == nil || err.Type != want {
			t.Errorf("%s: %v, want %s", body, err, want)
		}
	}
	// An attribute Hello does not keep is ignored, like in a POST.
	d := normalized(t, TypeUser, `{"userName": "x"}`)
	if err := applyPatch(TypeUser, d, ops(t, `{"Operations": [{"op": "add", "path": "urn:example:custom:1.0:User:foo", "value": "x"}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestDedupePrimary(t *testing.T) {
	out := dedupe([]any{
		map[string]any{"value": "a", "primary": true},
		map[string]any{"value": "b", "primary": true},
		map[string]any{"value": "a", "type": "work"},
	})
	want := []any{map[string]any{"value": "a", "type": "work"}, map[string]any{"value": "b", "primary": true}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("dedupe = %#v", out)
	}
}
