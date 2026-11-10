package configbatch

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/flow"
)

func loadSpec(t *testing.T) map[string]apispec.Operation {
	t.Helper()
	raw, err := os.ReadFile("../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := apispec.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]apispec.Operation{}
	for _, op := range s.Operations() {
		out[op.ID] = op
	}
	return out
}

// TestBatchableOps fails if a batchable operation is missing from the API
// document, is not a JSON write, returns a secret or needs a credential
// scope, if an object operation is not shaped as the batch expects ({id}
// path, a GET at the object's path, a delete and a create for the inverse),
// or if a refused operation is batchable.
func TestBatchableOps(t *testing.T) {
	spec := loadSpec(t)
	for _, id := range Sorted() {
		info := Ops[id]
		op, ok := spec[id]
		if !ok {
			t.Errorf("%s is batchable but not in openapi.json", id)
			continue
		}
		if _, _, err := Lookup(spec, id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		if op.Method == "GET" {
			t.Errorf("%s is a GET", id)
		}
		if info.Node == "" {
			if info.Read == "" {
				t.Errorf("%s has neither a node nor a list to read", id)
			} else if !hasGet(spec, info.Read) {
				t.Errorf("%s: no GET %s to read before and after", id, info.Read)
			}
			continue
		}
		if !info.Create && !strings.Contains(op.Path, "{"+IDParam+"}") {
			t.Errorf("%s: path %s has no {%s}", id, op.Path, IDParam)
		}
		if Deleters[info.Node] == "" {
			t.Errorf("%s: node %s has no delete operation, so a create cannot be reverted", id, info.Node)
		} else if d := spec[Deleters[info.Node]]; !hasGet(spec, d.Path) {
			t.Errorf("%s: no GET %s to read the object", id, d.Path)
		}
		if info.Delete && Creators[info.Node] == "" && id != "deleteDevice" && id != "deletePhone" {
			t.Errorf("%s: node %s has no create operation, so the delete cannot be reverted", id, info.Node)
		}
	}
	for id, why := range Refused {
		if _, ok := Ops[id]; ok {
			t.Errorf("%s is refused (%s) but listed as batchable", id, why)
		}
		if _, _, err := Lookup(spec, id); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("Lookup(%s) = %v, want the reason %q", id, err, why)
		}
	}
	// Every operation whose response carries a secret, takes multipart input
	// or needs a credential scope is unbatchable, whatever the table says.
	for id, op := range spec {
		if _, in := Ops[id]; in {
			continue
		}
		if _, _, err := Lookup(spec, id); err == nil {
			t.Errorf("%s is not in the table but Lookup accepts it", id)
		}
		_ = op
	}
	for _, id := range []string{"rotateDeviceSecret", "revealPhoneAdminPassword", "createToken", "createUser", "uploadFirmware",
		"createAnnouncement", "generateAnnouncement", "setQueueAgent", "controlConference", "updateExtensionVoicemail",
		"setTimeConditionOverride", "importCalendar", "putFlowLayout", "applyConfigBatch", "applyAIProposal"} {
		if _, _, err := Lookup(spec, id); err == nil {
			t.Errorf("%s must not be batchable", id)
		}
	}
}

func hasGet(spec map[string]apispec.Operation, path string) bool {
	for _, op := range spec {
		if op.Method == "GET" && op.Path == path {
			return true
		}
	}
	return false
}

func TestPlaceholders(t *testing.T) {
	refs := Refs{}
	if err := refs.Set("rg", json.RawMessage(`{"id": 42, "name": "sales"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := refs.Body(json.RawMessage(`{"trunkId":"${rg.id}","destination":"${rg.name}","label":"group ${rg.id} (${rg.name})","n":7,"list":["${rg.id}"]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"destination":"sales","label":"group 42 (sales)","list":[42],"n":7,"trunkId":42}`
	if string(got) != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
	if s, err := refs.String("ringGroup:${rg.id}"); err != nil || s != "ringGroup:42" {
		t.Fatalf("String = %q, %v", s, err)
	}
	for _, bad := range []string{"${nope.id}", "${rg.missing}"} {
		if _, err := refs.String(bad); err == nil {
			t.Errorf("String(%q) resolved", bad)
		}
		if _, err := refs.Body(json.RawMessage(`{"x":"` + bad + `"}`)); err == nil {
			t.Errorf("Body with %q resolved", bad)
		}
	}
}

func TestCheck(t *testing.T) {
	spec := loadSpec(t)
	one := int64(1)
	ok := Request{BaseRevision: &one, Ops: []Op{
		{Ref: "rg", OperationID: "createRingGroup", Body: json.RawMessage(`{"name":"sales"}`)},
		{OperationID: "updateRingGroup", PathParams: map[string]string{"id": "${rg.id}"}, Body: json.RawMessage(`{"name":"${rg.name}"}`)},
	}}
	if refused, problems := Check(spec, ok); refused != nil || len(problems) != 0 {
		t.Fatalf("Check(valid) = %v, %v", refused, problems)
	}
	cases := map[string]struct {
		ops     []Op
		refused bool
		want    string
	}{
		"refused op":       {[]Op{{OperationID: "createDevice", Body: json.RawMessage(`{}`)}}, true, "SIP secret"},
		"unknown op":       {[]Op{{OperationID: "nope"}}, true, "cannot be part of a batch"},
		"missing id":       {[]Op{{OperationID: "deleteRingGroup"}}, false, "pathParams.id"},
		"extra param":      {[]Op{{OperationID: "createRingGroup", PathParams: map[string]string{"x": "1"}, Body: json.RawMessage(`{}`)}}, false, "not a path parameter"},
		"bad param":        {[]Op{{OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "../x"}}}, false, "must be 1-64"},
		"missing body":     {[]Op{{OperationID: "createRingGroup"}}, false, "is required by"},
		"unknown ref":      {[]Op{{OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "${x.id}"}}}, false, "no earlier operation"},
		"ref after use":    {[]Op{{OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "${x.id}"}}, {Ref: "x", OperationID: "createRingGroup", Body: json.RawMessage(`{}`)}}, false, "no earlier operation"},
		"unknown field":    {[]Op{{Ref: "x", OperationID: "createRingGroup", Body: json.RawMessage(`{}`)}, {OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "${x.nope}"}}}, false, "has no \"nope\""},
		"duplicate ref":    {[]Op{{Ref: "x", OperationID: "createRingGroup", Body: json.RawMessage(`{}`)}, {Ref: "x", OperationID: "createRingGroup", Body: json.RawMessage(`{}`)}}, false, "already used"},
		"bad ref":          {[]Op{{Ref: "1x", OperationID: "createRingGroup", Body: json.RawMessage(`{}`)}}, false, "must be a letter"},
		"ref on no result": {[]Op{{Ref: "x", OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "1"}}}, false, "returns no object"},
		"empty":            {nil, false, "at least one"},
	}
	for name, c := range cases {
		refused, problems := Check(spec, Request{BaseRevision: &one, Ops: c.ops})
		var msgs []string
		if refused != nil {
			msgs = append(msgs, refused.Message)
		}
		for _, p := range problems {
			msgs = append(msgs, p.Path+": "+p.Message)
		}
		if (refused != nil) != c.refused || !strings.Contains(strings.Join(msgs, "\n"), c.want) {
			t.Errorf("%s: refused=%v problems=%q, want refused=%v containing %q", name, refused != nil, msgs, c.refused, c.want)
		}
	}
}

func TestRedact(t *testing.T) {
	got := string(Redact(json.RawMessage(`{"name":"t","password":"hunter2","nested":{"userPIN":"1234","ok":1},"list":[{"secret":"s"}],"empty":null}`)))
	for _, leak := range []string{"hunter2", "1234", `"s"`} {
		if strings.Contains(got, leak) {
			t.Fatalf("redacted body still holds %s: %s", leak, got)
		}
	}
	if !strings.Contains(got, `"name":"t"`) || !strings.Contains(got, `"ok":1`) {
		t.Fatalf("redacted body lost its other fields: %s", got)
	}
	if !IsCredentialKey("sipPassword") || IsCredentialKey("hasPass") || IsCredentialKey("name") {
		t.Fatal("IsCredentialKey is wrong")
	}
}

func applied(op Op, r Result) Applied {
	if r.OperationID == "" {
		r.OperationID = op.OperationID
	}
	return Applied{Op: op, Result: r}
}

// TestBuildInverse fails if an update is not restored from its before (only
// the fields it set, for a PATCH), a create is not deleted, a delete is not
// made again with later references pointing at the new id, a reorder is not
// replayed from its before, an update of an object the batch created is
// kept, or a credential the batch set is silently restored.
func TestBuildInverse(t *testing.T) {
	spec := loadSpec(t)
	rg := func(id string) json.RawMessage {
		return json.RawMessage(`{"id":` + id + `,"name":"sales","strategy":"ringall","members":[],"ringTimeout":20}`)
	}
	batch := []Applied{
		applied(Op{Ref: "a", OperationID: "createRingGroup", Body: json.RawMessage(`{"name":"new"}`)},
			Result{ID: "9", Status: "created", After: rg("9")}),
		applied(Op{OperationID: "updateRingGroup", PathParams: map[string]string{"id": "9"}, Body: json.RawMessage(`{"ringTimeout":30}`)},
			Result{ID: "9", Status: "updated", Before: rg("9"), After: rg("9")}),
		applied(Op{OperationID: "updateRingGroup", PathParams: map[string]string{"id": "5"}, Body: json.RawMessage(`{"ringTimeout":45,"name":"x"}`)},
			Result{ID: "5", Status: "updated", Before: rg("5"), After: rg("5")}),
		applied(Op{OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "5"}},
			Result{ID: "5", Status: "deleted", Before: rg("5")}),
		applied(Op{OperationID: "reorderInboundRoutes", Body: json.RawMessage(`{"ids":[2,1]}`)},
			Result{Status: "updated", Before: json.RawMessage(`{"items":[{"id":1},{"id":2}]}`), After: json.RawMessage(`{"items":[{"id":2},{"id":1}]}`)}),
	}
	inv, err := BuildInverse(spec, batch)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range inv.Ops {
		got = append(got, o.OperationID+" "+o.PathParams["id"]+" "+string(o.Body))
	}
	want := []string{
		`reorderInboundRoutes  {"ids":[1,2]}`,
		`createRingGroup  {"name":"sales","ringTimeout":20}`,
		`updateRingGroup ${r3.id} {"name":"sales","ringTimeout":20}`,
		`deleteRingGroup 9 `,
	}
	if len(got) != len(want) {
		t.Fatalf("inverse =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if i == 1 {
			// The recreated group's body carries the writable fields of the
			// ring group schema only; check the shape, not every field.
			if !strings.HasPrefix(got[i], "createRingGroup  {") || !strings.Contains(got[i], `"name":"sales"`) || strings.Contains(got[i], `"id"`) {
				t.Errorf("inverse[1] = %s", got[i])
			}
			continue
		}
		if i == 2 {
			if !strings.HasPrefix(got[i], "updateRingGroup ${r3.id} {") || !strings.Contains(got[i], `"ringTimeout":20`) || !strings.Contains(got[i], `"name":"sales"`) {
				t.Errorf("inverse[2] = %s", got[i])
			}
			continue
		}
		if got[i] != want[i] {
			t.Errorf("inverse[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if inv.Ops[1].Ref != "r3" || inv.Revive["r3"].Node != "ringGroup" {
		t.Errorf("the recreate is not tracked for its layout: ref %q, revive %v", inv.Ops[1].Ref, inv.Revive)
	}

	// A PATCH that set a credential cannot put it back.
	inv, err = BuildInverse(spec, []Applied{applied(
		Op{OperationID: "updateTrunk", PathParams: map[string]string{"id": "3"}, Body: json.RawMessage(`{"password":"[withheld]"}`)},
		Result{ID: "3", Status: "updated", Before: json.RawMessage(`{"id":3,"name":"t"}`), After: json.RawMessage(`{"id":3,"name":"t"}`)})})
	if err != nil || len(inv.Ops) != 0 || !slices.ContainsFunc(inv.Skipped, func(s string) bool { return strings.HasPrefix(s, "password") }) {
		t.Errorf("credential revert = %v, %v, skipped %v", inv.Ops, err, inv.Skipped)
	}

	// A delete that recorded nothing, or of an object that cannot be made
	// again, cannot be reverted.
	if _, err := BuildInverse(spec, []Applied{applied(Op{OperationID: "deleteRingGroup", PathParams: map[string]string{"id": "1"}}, Result{ID: "1", Status: "deleted"})}); err == nil {
		t.Error("reverting a delete without its before succeeded")
	}
	if _, err := BuildInverse(spec, []Applied{applied(Op{OperationID: "deleteDevice", PathParams: map[string]string{"id": "1"}}, Result{ID: "1", Status: "deleted", Before: json.RawMessage(`{"id":1}`)})}); err == nil {
		t.Error("reverting a device delete succeeded")
	}
}

func TestStaleChecks(t *testing.T) {
	spec := loadSpec(t)
	a := func(id, body string) Applied {
		return applied(Op{OperationID: "updateRingGroup", PathParams: map[string]string{"id": id}, Body: json.RawMessage(`{"ringTimeout":1}`)},
			Result{ID: id, Before: json.RawMessage(`{}`), After: json.RawMessage(body)})
	}
	cs := StaleChecks(spec, []Applied{a("1", `{"id":1,"ringTimeout":5}`), a("1", `{"id":1,"ringTimeout":6}`), a("2", `{"id":2}`)})
	if len(cs) != 2 || cs[0].OpIndex != 1 || cs[1].OpIndex != 2 || cs[0].Read != "/api/v1/ring-groups/1" {
		t.Fatalf("checks = %+v", cs)
	}
	if !cs[0].Same(json.RawMessage(`{"id":1,"ringTimeout":6}`)) {
		t.Error("the state the batch left is reported as changed")
	}
	if cs[0].Same(json.RawMessage(`{"id":1,"ringTimeout":7}`)) {
		t.Error("a changed writable field is not detected")
	}
	if !cs[0].Same(json.RawMessage(`{"id":1,"ringTimeout":6,"updatedAt":"2026-01-01T00:00:00Z"}`)) {
		t.Error("a field outside the request schema is compared")
	}
}

// TestBatchCoverage fails if a stored node kind of the catalogue has no
// update operation in a batch and is not on the reasoned NoBatchOps list, or
// if an entry of that list has gained an operation (the list is stale).
func TestBatchCoverage(t *testing.T) {
	covered := map[string]bool{}
	for _, info := range Ops {
		if info.Node != "" && !info.Create && !info.Delete {
			covered[info.Node] = true
		}
	}
	for _, n := range flow.Get().Nodes {
		if n.Pseudo || n.Terminal {
			continue
		}
		why, listed := NoBatchOps[n.Kind]
		switch {
		case !covered[n.Kind] && !listed:
			t.Errorf("node kind %s has no batchable update operation: add it to Ops (and the OpenAPI document) or list it in NoBatchOps with the reason", n.Kind)
		case covered[n.Kind] && listed:
			t.Errorf("node kind %s is listed in NoBatchOps (%s) but has an operation", n.Kind, why)
		}
	}
	for kind := range NoBatchOps {
		if _, ok := flow.Get().Node(kind); !ok {
			t.Errorf("NoBatchOps lists %s, which is not a catalogue node", kind)
		}
	}
}
