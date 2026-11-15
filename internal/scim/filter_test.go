package scim

import (
	"encoding/json"
	"strings"
	"testing"
)

func doc(t *testing.T, s string) map[string]any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

const userDoc = `{
	"id": "2819c223-7f76-453a-919d-413861904646",
	"externalId": "Ext-1",
	"userName": "Bjensen@Example.com",
	"displayName": "Babs Jensen",
	"active": true,
	"name": {"givenName": "Barbara", "familyName": "Jensen"},
	"emails": [{"value": "bjensen@example.com", "type": "work", "primary": true}, {"value": "babs@jensen.org", "type": "home"}],
	"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User": {"employeeNumber": "701984", "manager": {"value": "26118915"}},
	"meta": {"lastModified": "2026-05-13T04:42:34Z", "version": "W/\"3\""},
	"members": [{"value": "a"}, {"value": "b"}]
}`

func TestFilterEval(t *testing.T) {
	d := doc(t, userDoc)
	cases := map[string]bool{
		`userName eq "bjensen@example.com"`:                  true, // case-insensitive
		`USERNAME Eq "BJENSEN@EXAMPLE.COM"`:                  true,
		`externalId eq "ext-1"`:                              false, // caseExact
		`externalId eq "Ext-1"`:                              true,
		`id eq "2819c223-7f76-453a-919d-413861904646"`:       true,
		`emails.value eq "babs@jensen.org"`:                  true,
		`emails eq "babs@jensen.org"`:                        true,
		`emails[type eq "work" and value co "@example.com"]`: true,
		`emails[type eq "home" and primary eq true]`:         false,
		`userName eq "x" or displayName sw "Babs"`:           true,
		`not (active eq true)`:                               false,
		`active eq false`:                                    false,
		`name.familyName ew "sen"`:                           true,
		`title pr`:                                           false,
		`name pr and emails pr`:                              true,
		`title eq null`:                                      true,
		`meta.lastModified gt "2026-01-01T00:00:00Z"`:        true,
		`meta.lastModified lt "2026-01-01T00:00:00Z"`:        false,
		`urn:ietf:params:scim:schemas:core:2.0:User:userName eq "bjensen@example.com"`:                   true,
		`urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:employeeNumber eq "701984"`:          true,
		`urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager.value eq "26118915"`:         true,
		`id eq "2819c223-7f76-453a-919d-413861904646" and members[value eq "b"]`:                         true,
		`id eq "2819c223-7f76-453a-919d-413861904646" and members[value eq "c"]`:                         false,
		`(userName eq "x" or userName eq "bjensen@example.com") and not (displayName eq "Someone Else")`: true,
		`userName ne "x"`: true,
	}
	for f, want := range cases {
		n, err := parseFilter(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if got := n.eval(d); got != want {
			t.Errorf("%s = %v, want %v", f, got, want)
		}
	}
}

func TestFilterErrors(t *testing.T) {
	for _, f := range []string{
		`userName`, `userName eq`, `userName xx "a"`, `userName eq "a" and`, `(userName eq "a"`, `userName eq "unterminated`,
		`emails[type eq "work"`, `"a" eq "b"`, `userName eq bareword`, strings.Repeat("(", 40) + `a eq "b"` + strings.Repeat(")", 40),
	} {
		if _, err := parseFilter(f); err == nil || err.Type != typeInvalidFilter {
			t.Errorf("%q parsed: %v", f, err)
		}
	}
	if _, err := parseFilter(strings.Repeat("a", maxFilterLen+1)); err == nil {
		t.Error("an overlong filter parsed")
	}
}

func TestFilterHint(t *testing.T) {
	for f, want := range map[string]string{
		`userName eq "a"`:                      "name=a",
		`externalId eq "x" and active eq true`: "external_id=x",
		`active eq true and id eq "i"`:         "id=i",
		`userName co "a"`:                      "",
		`userName eq "a" or userName eq "b"`:   "",
	} {
		n, err := parseFilter(f)
		if err != nil {
			t.Fatal(err)
		}
		h := hintOf(TypeUser, n)
		got := ""
		if h != nil {
			got = h.Field + "=" + h.Value
		}
		if got != want {
			t.Errorf("%s: hint %q, want %q", f, got, want)
		}
	}
	n, _ := parseFilter(`displayName eq "Sales"`)
	if h := hintOf(TypeGroup, n); h == nil || h.Field != "name" {
		t.Errorf("group hint = %+v", h)
	}
}

func TestParsePath(t *testing.T) {
	for p, want := range map[string]string{
		`displayName`:                  "displayName||false",
		`name.givenName`:               "name|givenName|false",
		`emails[type eq "work"].value`: "emails|value|true",
		`members[value eq "2819c223"]`: "members||true",
		`urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager`:       SchemaEnterprise + ":manager||false",
		`urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager.value`: SchemaEnterprise + ":manager|value|false",
		`urn:ietf:params:scim:schemas:core:2.0:User:userName`:                      "userName||false",
	} {
		pp, err := parsePath(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		got := pp.attr + "|" + pp.sub + "|" + map[bool]string{true: "true", false: "false"}[pp.filter != nil]
		if got != want {
			t.Errorf("%s = %s, want %s", p, got, want)
		}
	}
	for _, p := range []string{``, `emails[type eq "work"`, `emails[type eq "work"]value`, `a b`} {
		if _, err := parsePath(p); err == nil {
			t.Errorf("%q parsed", p)
		}
	}
}
