package scim

// The request suite: SCIM requests shaped like the ones Microsoft Entra ID
// provisioning (and its SCIM validator) and Okta send, run through the
// handler, the directory engine and its in-memory store, so each one is
// checked down to the Hello user it makes, changes or disables. The live
// checks against real tenants are on the lab list (docs/scim.md).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
)

type env struct {
	t     *testing.T
	dm    *directory.Mem
	sm    *Mem
	eng   *directory.Engine
	svc   *Service
	c     directory.Connector
	token string
	now   time.Time
}

func newEnv(t *testing.T, cfg string) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, dm: directory.NewMem(), sm: NewMem(), now: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return e.now }
	e.sm.now = clock
	e.eng = &directory.Engine{Store: e.dm, Drivers: map[string]directory.Driver{directory.KindSCIM: Driver{Store: e.sm}}, Replica: "t", Now: clock}
	on := true
	name := "Entra"
	c, err := e.eng.Create(ctx, "admin", directory.ConnectorInput{Kind: directory.KindSCIM, Name: &name, Enabled: &on, Config: json.RawMessage(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	e.c = c
	e.sm.Kinds[c.ID] = "scim"
	e.svc = &Service{Store: e.sm, Engine: e.eng, PublicURL: "https://pbx.example.com/", Now: clock}
	_, tok, err := e.svc.IssueToken(ctx, "admin", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) < 50 {
		t.Fatalf("token = %q", tok)
	}
	e.token = tok
	return e
}

type resp struct {
	code int
	h    http.Header
	body map[string]any
	raw  string
}

func (e *env) do(method, path string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = strings.NewReader(string(raw))
	}
	r := httptest.NewRequest(method, "https://pbx.example.com"+Prefix+path, rd)
	r.Header.Set("Authorization", "Bearer "+e.token)
	if rd != nil {
		r.Header.Set("Content-Type", ContentType)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.svc.ServeHTTP(rec, r)
	out := resp{code: rec.Code, h: rec.Header(), raw: rec.Body.String()}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
			e.t.Fatalf("%s %s: not JSON: %s", method, path, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != ContentType {
			e.t.Fatalf("%s %s: content type %q", method, path, ct)
		}
	}
	return out
}

func (e *env) want(r resp, code int, what string) map[string]any {
	e.t.Helper()
	if r.code != code {
		e.t.Fatalf("%s: status %d, want %d: %s", what, r.code, code, r.raw)
	}
	return r.body
}

func (e *env) wantErr(r resp, code int, scimType, what string) {
	e.t.Helper()
	b := e.want(r, code, what)
	if b["status"] != strconv.Itoa(code) || (scimType != "" && b["scimType"] != scimType) {
		e.t.Fatalf("%s: error body %v, want scimType %q", what, b, scimType)
	}
	if s, _ := b["schemas"].([]any); len(s) != 1 || s[0] != schemaError {
		e.t.Fatalf("%s: error schemas %v", what, b["schemas"])
	}
}

// user returns the Hello user linked to a SCIM id.
func (e *env) user(id string) *directory.MemUser {
	e.t.Helper()
	for _, u := range e.dm.Users {
		if u.ExternalID == id && u.ConnectorID == e.c.ID {
			return u
		}
	}
	return nil
}

func list(b map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range b["Resources"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

// entraUser is the body Entra ID provisioning sends to create a user.
func entraUser(upn, objectID, mail, ext string) string {
	return `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User", "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"],
		"externalId": "` + objectID + `",
		"userName": "` + upn + `",
		"active": true,
		"displayName": "Adele Vance",
		"emails": [{"primary": true, "type": "work", "value": "` + mail + `"}],
		"meta": {"resourceType": "User"},
		"name": {"formatted": "Adele Vance", "familyName": "Vance", "givenName": "Adele"},
		"title": "Retail Manager",
		"preferredLanguage": "en-US",
		"phoneNumbers": [{"type": "work", "value": "+1 425 555 0109"}, {"type": "mobile", "value": "+1 425 555 0110"}],
		"addresses": [{"type": "work", "formatted": "Bellevue", "streetAddress": "205 108th Ave", "locality": "Bellevue", "country": "US", "primary": true}],
		"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User": {"employeeNumber": "1234", "department": "Retail"},
		"urn:hello:params:scim:schemas:extension:pbx:2.0:User": {"extension": "` + ext + `"}
	}`
}

func TestDiscovery(t *testing.T) {
	e := newEnv(t, `{}`)
	sp := e.want(e.do("GET", "/ServiceProviderConfig", nil), 200, "service provider config")
	if sp["patch"].(map[string]any)["supported"] != true || sp["bulk"].(map[string]any)["supported"] != false ||
		sp["filter"].(map[string]any)["maxResults"] != float64(200) || sp["etag"].(map[string]any)["supported"] != true {
		t.Fatalf("config = %v", sp)
	}
	sc := e.want(e.do("GET", "/Schemas", nil), 200, "schemas")
	if sc["totalResults"] != float64(4) {
		t.Fatalf("schemas = %v", sc["totalResults"])
	}
	pbx := e.want(e.do("GET", "/Schemas/"+SchemaPBX, nil), 200, "pbx schema")
	if attrs := pbx["attributes"].([]any); attrs[0].(map[string]any)["name"] != "extension" {
		t.Fatalf("pbx schema = %v", pbx)
	}
	rt := e.want(e.do("GET", "/ResourceTypes", nil), 200, "resource types")
	user := list(rt)[0]
	if user["endpoint"] != "/Users" || len(user["schemaExtensions"].([]any)) != 2 ||
		user["meta"].(map[string]any)["location"] != "https://pbx.example.com/scim/v2/ResourceTypes/User" {
		t.Fatalf("user type = %v", user)
	}
	e.want(e.do("GET", "/ResourceTypes/Group", nil), 200, "group type")
	e.wantErr(e.do("GET", "/ResourceTypes/Device", nil), 404, "", "unknown type")
	e.wantErr(e.do("GET", "/Schemas/urn:nope", nil), 404, "", "unknown schema")
	e.wantErr(e.do("POST", "/Bulk", `{}`), 501, "", "bulk")
	e.wantErr(e.do("GET", "/Me", nil), 501, "", "me")
	e.wantErr(e.do("GET", "/Devices", nil), 404, "", "unknown endpoint")
	e.wantErr(e.do("DELETE", "/ServiceProviderConfig", nil), 405, "", "method")
	e.wantErr(e.do("POST", "/Users/.search", `{}`), 501, "", ".search")
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t, `{}`)
	good := e.token
	for name, tok := range map[string]string{
		"none": "", "not a scim token": "hello_pat_abc", "unknown": TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		e.token = tok
		r := e.do("GET", "/Users", nil)
		e.wantErr(r, 401, "", name)
		if r.h.Get("WWW-Authenticate") == "" {
			t.Fatalf("%s: no WWW-Authenticate", name)
		}
	}
	e.token = good
	e.want(e.do("GET", "/Users", nil), 200, "good token")
	// A second token works too (rotation), at most two at once.
	_, second, err := e.svc.IssueToken(context.Background(), "admin", e.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.IssueToken(context.Background(), "admin", e.c.ID); !errors.Is(err, ErrTooMany) {
		t.Fatalf("third token: %v", err)
	}
	ts, _ := e.svc.ListTokens(context.Background(), e.c.ID)
	if _, err := e.svc.RevokeToken(context.Background(), "admin", ts[1].ID); err != nil {
		t.Fatal(err)
	}
	e.wantErr(e.do("GET", "/Users", nil), 401, "", "revoked token")
	e.token = second
	e.want(e.do("GET", "/Users", nil), 200, "second token")
	// A switched-off connector answers 403.
	off := false
	if _, err := e.eng.Update(context.Background(), "admin", e.c.ID, directory.ConnectorInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	e.wantErr(e.do("GET", "/Users", nil), 403, "", "connector off")
}

func TestEntraProvisioningCycle(t *testing.T) {
	e := newEnv(t, `{}`)
	e.dm.AddExt("2001", false)
	e.dm.AddExt("2002", false)
	// Entra first looks the user up, then creates it.
	b := e.want(e.do("GET", `/Users?filter=userName+eq+%22adelev%40contoso.com%22`, nil), 200, "lookup")
	if b["totalResults"] != float64(0) || len(b["Resources"].([]any)) != 0 {
		t.Fatalf("lookup = %v", b)
	}
	r := e.do("POST", "/Users", entraUser("AdeleV@contoso.com", "obj-1", "AdeleV@contoso.com", "2001"))
	u := e.want(r, 201, "create")
	id := u["id"].(string)
	if r.h.Get("Location") != "https://pbx.example.com/scim/v2/Users/"+id || r.h.Get("ETag") != `W/"1"` {
		t.Fatalf("headers = %v", r.h)
	}
	if u["userName"] != "AdeleV@contoso.com" || u["externalId"] != "obj-1" || u["active"] != true ||
		u["meta"].(map[string]any)["version"] != `W/"1"` || len(u["schemas"].([]any)) != 3 {
		t.Fatalf("created = %v", u)
	}
	hu := e.user(id)
	if hu == nil || hu.Username != "AdeleV@contoso.com" || hu.Email != "AdeleV@contoso.com" || hu.DisplayName != "Adele Vance" ||
		hu.Role != "user" || !hu.Enabled || hu.OwnedExtension != e.dm.Exts["2001"].ID {
		t.Fatalf("hello user = %+v", hu)
	}
	// The lookups Entra makes find it, by userName (any case), externalId
	// and e-mail.
	for _, f := range []string{`userName eq "adelev@contoso.com"`, `externalId eq "obj-1"`, `emails.value eq "adelev@contoso.com"`,
		`userName eq "AdeleV@contoso.com" and active eq true`} {
		b := e.want(e.do("GET", "/Users?filter="+urlEncode(f), nil), 200, f)
		if b["totalResults"] != float64(1) || list(b)[0]["id"] != id {
			t.Fatalf("%s = %v", f, b)
		}
	}
	// Entra's update: PATCH with capitalised ops, filtered paths, string
	// booleans and the enterprise extension.
	r = e.do("PATCH", "/Users/"+id, `{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [
			{"op": "Replace", "path": "displayName", "value": "Adele V."},
			{"op": "Replace", "path": "emails[type eq \"work\"].value", "value": "adele.vance@contoso.com"},
			{"op": "Add", "path": "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:manager", "value": "obj-9"},
			{"op": "Replace", "path": "urn:hello:params:scim:schemas:extension:pbx:2.0:User:extension", "value": "2002"}
		]}`)
	u = e.want(r, 200, "patch")
	if u["displayName"] != "Adele V." || r.h.Get("ETag") != `W/"2"` {
		t.Fatalf("patched = %v", u)
	}
	hu = e.user(id)
	if hu.DisplayName != "Adele V." || hu.Email != "adele.vance@contoso.com" || hu.OwnedExtension != e.dm.Exts["2002"].ID {
		t.Fatalf("hello user after patch = %+v", hu)
	}
	// Disable with Entra's string boolean, then enable again.
	e.want(e.do("PATCH", "/Users/"+id, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`), 200, "disable")
	if hu = e.user(id); hu.Enabled || !hu.DisabledByDirectory {
		t.Fatalf("after active False = %+v", hu)
	}
	e.want(e.do("PATCH", "/Users/"+id, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"True"}]}`), 200, "enable")
	if hu = e.user(id); !hu.Enabled {
		t.Fatalf("after active True = %+v", hu)
	}
	// DELETE disables, never deletes; the resource is gone for SCIM.
	r = e.do("DELETE", "/Users/"+id, nil)
	if r.code != 204 || r.raw != "" {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	if hu = e.user(id); hu == nil || hu.Enabled {
		t.Fatalf("after delete = %+v", hu)
	}
	e.wantErr(e.do("GET", "/Users/"+id, nil), 404, "", "deleted")
	e.wantErr(e.do("DELETE", "/Users/"+id, nil), 404, "", "deleted twice")
	// Created again (a restore in Entra): same id, same Hello user, enabled.
	u = e.want(e.do("POST", "/Users", entraUser("AdeleV@contoso.com", "obj-1", "adele.vance@contoso.com", "2002")), 201, "recreate")
	if u["id"] != id {
		t.Fatalf("recreated with a new id %v", u["id"])
	}
	if hu = e.user(id); !hu.Enabled || len(e.dm.Users) != 1 {
		t.Fatalf("after recreate = %+v (%d users)", hu, len(e.dm.Users))
	}
	// The changes are on the connector's SCIM run of the hour.
	runs, _ := e.eng.ListRuns(context.Background(), e.c.ID, 10)
	if len(runs) != 1 || runs[0].Kind != directory.RunSCIM || runs[0].Counts["create"] != 1 || runs[0].Counts["disable"] < 2 {
		t.Fatalf("runs = %+v", runs)
	}
}

func urlEncode(s string) string {
	r := strings.NewReplacer(" ", "+", `"`, "%22", "@", "%40", "[", "%5B", "]", "%5D")
	return r.Replace(s)
}

func TestEntraGroupsAndRules(t *testing.T) {
	e := newEnv(t, `{}`)
	ann := e.want(e.do("POST", "/Users", entraUser("ann@contoso.com", "o-ann", "ann@contoso.com", "")), 201, "ann")["id"].(string)
	bob := e.want(e.do("POST", "/Users", entraUser("bob@contoso.com", "o-bob", "bob@contoso.com", "")), 201, "bob")["id"].(string)
	g := e.want(e.do("POST", "/Groups", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"externalId":"grp-sales","displayName":"Sales","members":[]}`), 201, "group")
	gid := g["id"].(string)
	// A group rule names the group by its externalId: members become
	// operators (never admin: allowAdminFromDirectory is off).
	rules := directory.GroupRules{Roles: []directory.RoleRule{{GroupID: "grp-sales", Role: "operator"}, {GroupID: gid, Role: "admin"}}}
	if _, err := e.eng.Update(context.Background(), "admin", e.c.ID, directory.ConnectorInput{GroupRules: &rules}); err != nil {
		t.Fatal(err)
	}
	e.want(e.do("PATCH", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"Add","path":"members","value":[{"value":"`+ann+`"},{"value":"`+bob+`"}]}]}`), 200, "add members")
	if e.user(ann).Role != "operator" || e.user(bob).Role != "operator" {
		t.Fatalf("roles = %s, %s", e.user(ann).Role, e.user(bob).Role)
	}
	// Entra's membership check, and the group lookup without members.
	b := e.want(e.do("GET", "/Groups?filter="+urlEncode(`id eq "`+gid+`" and members[value eq "`+bob+`"]`)+"&excludedAttributes=members", nil), 200, "member check")
	if b["totalResults"] != float64(1) || list(b)[0]["members"] != nil {
		t.Fatalf("member check = %v", b)
	}
	b = e.want(e.do("GET", "/Groups?filter="+urlEncode(`displayName eq "sales"`), nil), 200, "group lookup")
	if len(list(b)[0]["members"].([]any)) != 2 {
		t.Fatalf("group = %v", b)
	}
	u := e.want(e.do("GET", "/Users/"+ann, nil), 200, "ann")
	if gs := u["groups"].([]any); len(gs) != 1 || gs[0].(map[string]any)["value"] != gid {
		t.Fatalf("ann groups = %v", u["groups"])
	}
	// Entra removes a member by listing it.
	e.want(e.do("PATCH", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"Remove","path":"members","value":[{"value":"`+bob+`"}]}]}`), 200, "remove member")
	if e.user(bob).Role != "user" || e.user(ann).Role != "operator" {
		t.Fatalf("roles after removal = %s, %s", e.user(ann).Role, e.user(bob).Role)
	}
	e.wantErr(e.do("PATCH", "/Groups/"+gid, `{"Operations":[{"op":"Add","path":"members","value":[{"value":"nobody"}]}]}`), 400, typeInvalidValue, "unknown member")
	// Deleting the group takes the role back.
	if r := e.do("DELETE", "/Groups/"+gid, nil); r.code != 204 {
		t.Fatalf("delete group = %d", r.code)
	}
	if e.user(ann).Role != "user" {
		t.Fatalf("ann after group delete = %s", e.user(ann).Role)
	}
	e.wantErr(e.do("GET", "/Groups/"+gid, nil), 404, "", "deleted group")
}

func TestOktaProvisioningCycle(t *testing.T) {
	e := newEnv(t, `{}`)
	// Okta's import check and create.
	b := e.want(e.do("GET", `/Users?filter=userName%20eq%20%22dave%40example.com%22&startIndex=1&count=100`, nil), 200, "okta lookup")
	if b["totalResults"] != float64(0) || b["startIndex"] != float64(1) {
		t.Fatalf("lookup = %v", b)
	}
	u := e.want(e.do("POST", "/Users", `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName": "dave@example.com",
		"name": {"givenName": "Dave", "familyName": "Lee"},
		"emails": [{"primary": true, "value": "dave@example.com", "type": "work"}],
		"displayName": "Dave Lee",
		"locale": "en-US",
		"externalId": "00u1abcd",
		"groups": [],
		"password": "Sup3r-secret!",
		"active": true
	}`), 201, "okta create")
	id := u["id"].(string)
	if _, ok := u["password"]; ok {
		t.Fatal("the password came back")
	}
	if r, _ := e.sm.GetResource(context.Background(), e.c.ID, TypeUser, id, false); strings.Contains(stringify(r.Data), "Sup3r") {
		t.Fatal("the password was stored")
	}
	// Okta's profile push is a PUT of the whole user.
	u = e.want(e.do("PUT", "/Users/"+id, `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"], "id": "`+id+`",
		"userName": "dave@example.com", "name": {"givenName": "David", "familyName": "Lee"},
		"emails": [{"primary": true, "value": "david.lee@example.com", "type": "work"}],
		"displayName": "David Lee", "active": true
	}`), 200, "okta put")
	if u["displayName"] != "David Lee" || u["locale"] != nil || u["externalId"] != nil {
		t.Fatalf("put = %v", u)
	}
	if hu := e.user(id); hu.Email != "david.lee@example.com" || hu.DisplayName != "David Lee" {
		t.Fatalf("hello user = %+v", hu)
	}
	e.wantErr(e.do("PUT", "/Users/"+id, `{"id":"other","userName":"dave@example.com"}`), 400, typeMutability, "put with another id")
	e.wantErr(e.do("PUT", "/Users/"+id, `{"displayName":"no userName"}`), 400, typeInvalidValue, "put without userName")
	// Okta's deactivation: a no-path replace.
	e.want(e.do("PATCH", "/Users/"+id, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":false}}]}`), 200, "deactivate")
	if e.user(id).Enabled {
		t.Fatal("not disabled")
	}
	// Okta groups: create, rename with id in the value, members by add and
	// by a filtered remove.
	g := e.want(e.do("POST", "/Groups", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Support","members":[{"value":"`+id+`","display":"dave@example.com"}]}`), 201, "okta group")
	gid := g["id"].(string)
	if ms := g["members"].([]any); len(ms) != 1 || ms[0].(map[string]any)["display"] != "dave@example.com" {
		t.Fatalf("group = %v", g)
	}
	g = e.want(e.do("PATCH", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"id":"`+gid+`","displayName":"Support EMEA"}}]}`), 200, "rename")
	if g["displayName"] != "Support EMEA" {
		t.Fatalf("renamed = %v", g)
	}
	g = e.want(e.do("PATCH", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove","path":"members[value eq \"`+id+`\"]"}]}`), 200, "remove member")
	if g["members"] != nil && len(g["members"].([]any)) != 0 {
		t.Fatalf("members = %v", g["members"])
	}
	// Okta's group push may PUT the group.
	g = e.want(e.do("PUT", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Support","members":[{"value":"`+id+`"}]}`), 200, "put group")
	if len(g["members"].([]any)) != 1 {
		t.Fatalf("put group = %v", g)
	}
}

func stringify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPagingProjectionAndETags(t *testing.T) {
	e := newEnv(t, `{}`)
	var ids []string
	for i := 0; i < 5; i++ {
		e.now = e.now.Add(time.Second)
		n := strconv.Itoa(i)
		ids = append(ids, e.want(e.do("POST", "/Users", `{"userName":"u`+n+`@corp.org","displayName":"User `+n+`"}`), 201, "create")["id"].(string))
	}
	b := e.want(e.do("GET", "/Users?startIndex=3&count=2", nil), 200, "page")
	items := list(b)
	if b["totalResults"] != float64(5) || b["startIndex"] != float64(3) || b["itemsPerPage"] != float64(2) || items[0]["id"] != ids[2] || items[1]["id"] != ids[3] {
		t.Fatalf("page = %v", b)
	}
	b = e.want(e.do("GET", "/Users?count=0", nil), 200, "count 0")
	if b["totalResults"] != float64(5) || len(b["Resources"].([]any)) != 0 {
		t.Fatalf("count 0 = %v", b)
	}
	b = e.want(e.do("GET", "/Users?count=1000&startIndex=0", nil), 200, "count capped")
	if len(list(b)) != 5 || b["startIndex"] != float64(1) {
		t.Fatalf("capped = %v", b)
	}
	e.wantErr(e.do("GET", "/Users?count=x", nil), 400, typeInvalidValue, "bad count")
	e.wantErr(e.do("GET", "/Users?filter="+urlEncode(`userName xx "a"`), nil), 400, typeInvalidFilter, "bad filter")
	e.wantErr(e.do("GET", "/Users?sortBy=userName", nil), 400, typeInvalidValue, "sort")
	// attributes and excludedAttributes.
	u := e.want(e.do("GET", "/Users/"+ids[0]+"?attributes=userName", nil), 200, "attributes")
	if len(u) != 3 || u["userName"] != "u0@corp.org" || u["id"] != ids[0] {
		t.Fatalf("projected = %v", u)
	}
	u = e.want(e.do("GET", "/Users/"+ids[0]+"?excludedAttributes=displayName,meta", nil), 200, "excluded")
	if _, ok := u["displayName"]; ok || u["meta"] != nil || u["userName"] == nil {
		t.Fatalf("excluded = %v", u)
	}
	// ETags: If-None-Match, and If-Match on a write.
	r := e.do("GET", "/Users/"+ids[0], nil, "If-None-Match", `W/"1"`)
	if r.code != 304 {
		t.Fatalf("if-none-match = %d", r.code)
	}
	e.want(e.do("PATCH", "/Users/"+ids[0], `{"Operations":[{"op":"replace","path":"title","value":"x"}]}`, "If-Match", `W/"1"`), 200, "if-match")
	e.wantErr(e.do("PATCH", "/Users/"+ids[0], `{"Operations":[{"op":"replace","path":"title","value":"y"}]}`, "If-Match", `W/"1"`), 412, "", "stale if-match")
	e.wantErr(e.do("DELETE", "/Users/"+ids[0], nil, "If-Match", `W/"1"`), 412, "", "stale delete")
}

func TestConflictsAndErrors(t *testing.T) {
	e := newEnv(t, `{}`)
	e.dm.AddUser(directory.LocalUser{Username: "carl@corp.org", Email: "carl@corp.org", Role: "user", Enabled: true})
	e.dm.AddUser(directory.LocalUser{Username: "root", Email: "root@corp.org", Role: "admin", Enabled: true})
	e.wantErr(e.do("POST", "/Users", `{"userName":"carl@corp.org"}`), 409, typeUniqueness, "local user, no adoption")
	e.wantErr(e.do("POST", "/Users", `{"userName":"root2","emails":[{"value":"root@corp.org","primary":true}]}`), 409, typeUniqueness, "local admin")
	e.wantErr(e.do("POST", "/Users", `{"userName":"x","urn:hello:params:scim:schemas:extension:pbx:2.0:User":{"extension":"2999"}}`), 400, typeInvalidValue, "missing extension")
	e.wantErr(e.do("POST", "/Users", `{"userName":"x","urn:hello:params:scim:schemas:extension:pbx:2.0:User":{"extension":"12"}}`+"x"), 400, typeInvalidSyntax, "not JSON")
	e.wantErr(e.do("POST", "/Users", `{"userName":"bad name!"}`), 400, typeInvalidValue, "username")
	e.wantErr(e.do("POST", "/Users", `{"userName":""}`), 400, typeInvalidValue, "empty userName")
	e.wantErr(e.do("POST", "/Users", `{"userName":"x","active":"perhaps"}`), 400, typeInvalidValue, "bad boolean")
	e.want(e.do("POST", "/Users", `{"userName":"dup@corp.org","externalId":"e1"}`), 201, "first")
	e.wantErr(e.do("POST", "/Users", `{"userName":"DUP@corp.org"}`), 409, typeUniqueness, "duplicate userName")
	e.wantErr(e.do("POST", "/Users", `{"userName":"other@corp.org","externalId":"e1"}`), 409, typeUniqueness, "duplicate externalId")
	e.wantErr(e.do("PATCH", "/Users/nope", `{"Operations":[{"op":"replace","path":"title","value":"x"}]}`), 404, "", "patch missing")
	e.wantErr(e.do("PATCH", "/Users/nope", `{"schemas":["urn:x"],"Operations":[]}`), 400, typeInvalidSyntax, "not a PatchOp")
	// A user that is inactive on creation is kept as a resource, without a
	// Hello user, until it becomes active.
	u := e.want(e.do("POST", "/Users", `{"userName":"later@corp.org","active":false}`), 201, "inactive create")
	if e.user(u["id"].(string)) != nil {
		t.Fatal("an inactive user was created in Hello")
	}
	e.want(e.do("PATCH", "/Users/"+u["id"].(string), `{"Operations":[{"op":"replace","value":{"active":true}}]}`), 200, "activate")
	if e.user(u["id"].(string)) == nil {
		t.Fatal("not created on activation")
	}
	// With adoptExisting the local user is taken over (never the admin).
	if _, err := e.eng.Update(context.Background(), "admin", e.c.ID, directory.ConnectorInput{Config: json.RawMessage(`{"adoptExisting":true}`)}); err != nil {
		t.Fatal(err)
	}
	carl := e.want(e.do("POST", "/Users", `{"userName":"carl@corp.org","displayName":"Carl"}`), 201, "adopt")
	if hu := e.user(carl["id"].(string)); hu == nil || hu.ID != 1 || hu.DisplayName != "Carl" {
		t.Fatalf("adopted = %+v", hu)
	}
	e.wantErr(e.do("POST", "/Users", `{"userName":"root"}`), 409, typeUniqueness, "admin with adoption on")
	// A bad config is refused by the connector validation.
	if _, err := e.eng.Update(context.Background(), "admin", e.c.ID, directory.ConnectorInput{Config: json.RawMessage(`{"url":"x"}`)}); err == nil {
		t.Fatal("an unknown config key was accepted")
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, `{}`)
	e.svc.RatePerMinute = 3
	for i := 0; i < 3; i++ {
		e.want(e.do("GET", "/Users", nil), 200, "within")
	}
	r := e.do("GET", "/Users", nil)
	e.wantErr(r, 429, "", "over")
	if r.h.Get("Retry-After") != "60" {
		t.Fatalf("retry-after = %q", r.h.Get("Retry-After"))
	}
	e.now = e.now.Add(time.Minute)
	e.want(e.do("GET", "/Users", nil), 200, "next minute")
	// The token's use is recorded (at most once a minute).
	st, err := e.svc.Status(context.Background(), e.c.ID)
	if err != nil || st.LastRequestAt == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestMassDeprovisionGuard(t *testing.T) {
	e := newEnv(t, `{}`)
	var ids []string
	for i := 0; i < 8; i++ {
		n := strconv.Itoa(i)
		ids = append(ids, e.want(e.do("POST", "/Users", `{"userName":"u`+n+`@corp.org"}`), 201, "create")["id"].(string))
	}
	e.now = e.now.Add(time.Minute)
	for i := 0; i < 5; i++ {
		if r := e.do("DELETE", "/Users/"+ids[i], nil); r.code != 204 {
			t.Fatalf("delete %d = %d %s", i, r.code, r.raw)
		}
	}
	// The sixth disable within the hour is over the guard (20 percent of
	// the linked users, at least 5): 429, the connector pauses.
	r := e.do("PATCH", "/Users/"+ids[5], `{"Operations":[{"op":"replace","path":"active","value":false}]}`)
	e.wantErr(r, 429, "", "over the guard")
	if r.h.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	if !e.user(ids[5]).Enabled {
		t.Fatal("the refused disable was applied")
	}
	e.wantErr(e.do("POST", "/Users", `{"userName":"new@corp.org"}`), 429, "", "paused create")
	e.want(e.do("GET", "/Users", nil), 200, "reads still work")
	st, err := e.svc.Status(context.Background(), e.c.ID)
	if err != nil || st.PausedAt == nil || st.PausedRunID == nil || st.Guard.Disables != 5 || st.Resources.Users != 3 || st.Resources.DeletedUsers != 5 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// An administrator confirms in the console; the IdP's retry goes through.
	e.now = e.now.Add(time.Minute)
	if _, err := e.eng.Confirm(context.Background(), "admin", *st.PausedRunID, directory.Decisions{}); err != nil {
		t.Fatal(err)
	}
	e.want(e.do("PATCH", "/Users/"+ids[5], `{"Operations":[{"op":"replace","path":"active","value":false}]}`), 200, "after confirm")
	if e.user(ids[5]).Enabled {
		t.Fatal("not disabled after confirm")
	}
}

func TestDriverFeedsManualRuns(t *testing.T) {
	e := newEnv(t, `{}`)
	e.dm.AddExt("3001", false)
	id := e.want(e.do("POST", "/Users", `{"userName":"pat@corp.org","phoneNumbers":[{"type":"work","value":"3001"}]}`), 201, "create")["id"].(string)
	if hu := e.user(id); hu.OwnedExtension != 0 {
		t.Fatalf("linked before the mapping names phoneNumbers: %+v", hu)
	}
	// The administrator maps the work phone to the extension and applies a
	// manual run: the stored resources are re-mapped.
	m := DefaultMappings()
	m.Extension.Sources = []string{AttrPBXExtension, "phoneNumbers[work]"}
	if _, err := e.eng.Update(context.Background(), "admin", e.c.ID, directory.ConnectorInput{Mappings: &m}); err != nil {
		t.Fatal(err)
	}
	res, err := e.eng.Test(context.Background(), "admin", e.c.ID, nil)
	if err != nil || !res.OK || res.MatchedUsers != 1 || res.Sample[0].Mapped.Extension != "3001" {
		t.Fatalf("test = %+v, %v", res, err)
	}
	run, err := e.eng.RequestRun(context.Background(), "admin", e.c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	e.eng.TickOnce(context.Background())
	run, _ = e.eng.GetRun(context.Background(), run.ID)
	if run.Status != directory.StatusSucceeded || run.Counts["update"] != 1 {
		t.Fatalf("run = %+v", run)
	}
	if hu := e.user(id); hu.OwnedExtension != e.dm.Exts["3001"].ID {
		t.Fatalf("after the run = %+v", hu)
	}
}
