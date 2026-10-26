package api

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/configbatch"
	"github.com/azrtydxb/hello/internal/flow"
)

// btOp builds one operation of a batch request.
func btOp(ref, id string, params map[string]string, body any) map[string]any {
	o := map[string]any{"operationId": id}
	if ref != "" {
		o["ref"] = ref
	}
	if params != nil {
		o["pathParams"] = params
	}
	if body != nil {
		o["body"] = body
	}
	return o
}

func btReq(rev float64, dry bool, ops ...map[string]any) map[string]any {
	return map[string]any{"baseRevision": rev, "dryRun": dry, "ops": ops}
}

func btRevision(t *testing.T, c *client) float64 {
	t.Helper()
	return c.must(http.StatusOK, "GET", "/api/v1/flow/revision", nil).json(t)["configRevision"].(float64)
}

// btError is the error object of a failed batch.
type btError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fields  []struct{ Path, Message string }
		Errors  []struct {
			OpIndex int    `json:"opIndex"`
			Node    string `json:"node"`
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"errors"`
		Conflicts []struct {
			OpIndex int    `json:"opIndex"`
			Node    string `json:"node"`
		} `json:"conflicts"`
	} `json:"error"`
}

func btFail(t *testing.T, r response, want int, code string) btError {
	t.Helper()
	var e btError
	if err := json.Unmarshal(r.body, &e); err != nil {
		t.Fatalf("response %d is not an error: %s", r.code, r.body)
	}
	if r.code != want || e.Error.Code != code {
		t.Fatalf("response = %d %s, want %d %s", r.code, r.body, want, code)
	}
	return e
}

// btState is what a batch must leave alone when it fails.
func (e *env) btState() string {
	e.t.Helper()
	var parts []string
	for _, tbl := range []string{"extensions", "ring_groups", "ring_group_members", "inbound_routes", "outbound_routes", "audit_events",
		"config_batches", "schema_info", "flow_canvas_nodes", "flow_canvases"} {
		var n int
		var h string
		if err := e.db.QueryRow(`SELECT count(*), md5(coalesce(string_agg(t::text, '|' ORDER BY t::text), '')) FROM `+tbl+` t`).Scan(&n, &h); err != nil {
			e.t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("%s=%d:%s", tbl, n, h))
	}
	return strings.Join(parts, " ")
}

func (e *env) btCount(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func btMember(e *env, ext map[string]any, pos int) map[string]any {
	return map[string]any{"extensionId": ext["id"], "position": pos}
}

// TestBatchApply fails if a batch of a ring group and an inbound route that
// names it through a placeholder does not apply as one change: one revision
// bump, one batch row, every audit row marked batch:<id>, the results and
// the stored batch carrying before, after and diff.
func TestBatchApply(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	rev := btRevision(t, c)
	audits := e.btCount(`SELECT count(*) FROM audit_events`)

	resp := c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(rev, false,
		btOp("rg", "createRingGroup", nil, map[string]any{"name": "sales", "members": []any{btMember(e, e.ext101, 1)}}),
		btOp("in", "createInboundRoute", nil, map[string]any{
			"name": "Main DID", "didKind": "exact", "did": "+97142000000",
			"destinationKind": "ring_group", "destination": "${rg.name}",
		}),
	)).json(t)
	if resp["applied"] != true || resp["revision"] != rev+1 || resp["baseRevision"] != rev || resp["batchId"] == nil {
		t.Fatalf("response = %v", resp)
	}
	results := resp["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	first := results[0].(map[string]any)
	if first["status"] != "created" || first["ref"] != "rg" || first["before"] != nil || first["after"] == nil ||
		first["node"] != "ringGroup:"+fmt.Sprint(first["id"]) || len(first["diff"].([]any)) == 0 {
		t.Fatalf("first result = %v", first)
	}
	if got := btRevision(t, c); got != rev+1 {
		t.Fatalf("revision = %v, want %v: the batch must move it once", got, rev+1)
	}
	id := resp["batchId"].(string)
	if n := e.btCount(`SELECT count(*) FROM audit_events WHERE via = $1`, "batch:"+id); n != 2 {
		t.Fatalf("%d audit rows carry via batch:%s, want 2", n, id)
	}
	if n := e.btCount(`SELECT count(*) FROM audit_events`); n != audits+2 {
		t.Fatalf("audit rows = %d, want %d (no extra rows for the batch)", n, audits+2)
	}
	in := c.must(http.StatusOK, "GET", "/api/v1/routes/inbound/"+fmt.Sprint(results[1].(map[string]any)["id"]), nil).json(t)
	if in["destinationKind"] != "ring_group" || in["destination"] != "sales" {
		t.Fatalf("inbound route = %v: the placeholder was not filled in", in)
	}

	list := c.must(http.StatusOK, "GET", "/api/v1/config/batches", nil).json(t)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id || list[0].(map[string]any)["opCount"] != float64(2) {
		t.Fatalf("batches = %v", list)
	}
	if _, has := list[0].(map[string]any)["ops"]; has {
		t.Fatalf("the list carries the operations: %v", list[0])
	}
	one := c.must(http.StatusOK, "GET", "/api/v1/config/batches/"+id, nil).json(t)
	if ops := one["ops"].([]any); len(ops) != 2 || ops[1].(map[string]any)["body"].(map[string]any)["destination"] != "sales" {
		t.Fatalf("stored ops = %v", one["ops"])
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/config/batches/00000000-0000-0000-0000-000000000000", nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/config/batches/nope", nil)
	c.must(http.StatusBadRequest, "GET", "/api/v1/config/batches?limit=0", nil)
	c.must(http.StatusBadRequest, "GET", "/api/v1/config/batches?before=yesterday", nil)
	c.must(http.StatusOK, "GET", "/api/v1/config/batches?limit=1&before=2999-01-01T00:00:00Z", nil)
}

// TestBatchAtomic fails if a batch whose later operation fails leaves any
// change, audit row, batch row or revision bump, if a dry run commits, or if
// an error the whole-configuration check finds is not named by operation.
func TestBatchAtomic(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	rev := btRevision(t, c)
	before := e.btState()
	rg := func(name string) map[string]any {
		return map[string]any{"name": name, "members": []any{btMember(e, e.ext101, 1)}}
	}

	// Op 2 of 3 fails (no such group): the first two leave nothing.
	err := btFail(t, c.do("POST", "/api/v1/config/batch", btReq(rev, false,
		btOp("", "createRingGroup", nil, rg("a")),
		btOp("", "createRingGroup", nil, rg("b")),
		btOp("", "updateRingGroup", map[string]string{"id": "999999"}, map[string]any{"strategy": "weighted"}),
	)), http.StatusUnprocessableEntity, "validation_failed")
	if len(err.Error.Errors) != 1 || err.Error.Errors[0].OpIndex != 2 || !strings.Contains(err.Error.Message, "operation 2 (updateRingGroup)") {
		t.Fatalf("error = %+v", err.Error)
	}
	if after := e.btState(); after != before {
		t.Fatalf("a failed batch changed the database:\nbefore %s\nafter  %s", before, after)
	}

	// An operation the API refuses (a duplicate name) is named by its index.
	c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(rev, false, btOp("", "createRingGroup", nil, rg("taken"))))
	rev++
	before = e.btState()
	err = btFail(t, c.do("POST", "/api/v1/config/batch", btReq(rev, false,
		btOp("", "createRingGroup", nil, rg("fresh")),
		btOp("", "createRingGroup", nil, rg("taken")),
	)), http.StatusUnprocessableEntity, "validation_failed")
	if len(err.Error.Errors) != 1 || err.Error.Errors[0].OpIndex != 1 {
		t.Fatalf("duplicate: error = %+v", err.Error)
	}
	if e.btState() != before {
		t.Fatal("the duplicate batch changed the database")
	}

	// The whole-configuration check runs once at the end: an inbound route
	// to an extension that does not exist is found there, and names the
	// operation that made it.
	err = btFail(t, c.do("POST", "/api/v1/config/batch", btReq(rev, false,
		btOp("", "createRingGroup", nil, rg("fresh")),
		btOp("", "createInboundRoute", nil, map[string]any{"name": "Broken", "destinationKind": "extension", "destination": "555"}),
	)), http.StatusUnprocessableEntity, "validation_failed")
	if len(err.Error.Errors) == 0 || err.Error.Errors[0].OpIndex != 1 {
		t.Fatalf("whole-configuration error = %+v, want it on operation 1", err.Error)
	}
	if e.btState() != before {
		t.Fatal("the invalid batch changed the database")
	}

	// A dry run runs everything, answers what an apply would, and commits
	// nothing.
	dry := c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(rev, true,
		btOp("g", "createRingGroup", nil, rg("dry")),
		btOp("", "updateRingGroup", map[string]string{"id": "${g.id}"}, map[string]any{"strategy": "weighted"}),
	)).json(t)
	if dry["applied"] != false || dry["batchId"] != nil || dry["revision"] != rev {
		t.Fatalf("dry run = %v", dry)
	}
	if rs := dry["results"].([]any); len(rs) != 2 || rs[1].(map[string]any)["status"] != "updated" {
		t.Fatalf("dry run results = %v", rs)
	}
	if e.btState() != before {
		t.Fatal("a dry run changed the database")
	}
	if got := btRevision(t, c); got != rev {
		t.Fatalf("revision after a dry run = %v, want %v", got, rev)
	}
}

// TestBatchStale fails if a batch built against an older revision applies
// over an object that changed since (409 config_stale, nothing applied), if
// an unrelated change rejects it, or if an operation without expect is
// refused.
func TestBatchStale(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	mk := func(name string) map[string]any {
		return c.must(http.StatusCreated, "POST", "/api/v1/ring-groups",
			map[string]any{"name": name, "members": []any{btMember(e, e.ext101, 1)}}).json(t)
	}
	a, b := mk("alpha"), mk("beta")
	version := func(id any) string {
		g := c.must(http.StatusOK, "GET", "/api/v1/flow/graph", nil).json(t)
		for _, n := range g["nodes"].([]any) {
			if n.(map[string]any)["id"] == fmt.Sprintf("ringGroup:%v", id) {
				return n.(map[string]any)["version"].(string)
			}
		}
		t.Fatalf("no node for group %v", id)
		return ""
	}
	rev := btRevision(t, c)
	vA, vB := version(a["id"]), version(b["id"])

	// Someone else changes alpha.
	c.must(http.StatusOK, "PATCH", fmt.Sprintf("/api/v1/ring-groups/%v", a["id"]), map[string]any{"strategy": "weighted"})
	if btRevision(t, c) == rev {
		t.Fatal("the direct change did not move the revision")
	}
	upd := func(g map[string]any, expect string, timeout int) map[string]any {
		o := btOp("", "updateRingGroup", map[string]string{"id": fmt.Sprint(g["id"])}, map[string]any{"ringTimeout": timeout})
		if expect != "" {
			o["expect"] = expect
		}
		return o
	}
	before := e.btState()
	for _, dry := range []bool{false, true} {
		err := btFail(t, c.do("POST", "/api/v1/config/batch", btReq(rev, dry, upd(b, vB, 40), upd(a, vA, 41))), http.StatusConflict, "config_stale")
		if len(err.Error.Conflicts) != 1 || err.Error.Conflicts[0].OpIndex != 1 || err.Error.Conflicts[0].Node != fmt.Sprintf("ringGroup:%v", a["id"]) {
			t.Fatalf("dry=%v conflicts = %+v", dry, err.Error.Conflicts)
		}
	}
	if e.btState() != before {
		t.Fatal("a stale batch changed the database")
	}

	// An unrelated object, or an operation that names no version, applies
	// over the moved revision.
	got := c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(rev, false, upd(b, vB, 40))).json(t)
	if got["applied"] != true {
		t.Fatalf("unrelated change = %v", got)
	}
	c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(rev, false, upd(a, "", 42)))
	if g := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/ring-groups/%v", a["id"]), nil).json(t); g["ringTimeout"] != float64(42) {
		t.Fatalf("alpha = %v", g)
	}

	// The current revision never conflicts, whatever expect says.
	cur := btRevision(t, c)
	c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(cur, true, upd(a, "stale-version", 43)))
}

// TestBatchRoles fails if an operator's batch holding an admin-only
// operation applies anything, a viewer can apply, a batch carries an
// operation that returns a secret, takes multipart input or is a credential
// operation, or a batch is accepted over 200 operations.
func TestBatchRoles(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	admin := e.login()
	operator, _ := e.asRole("olive", auth.RoleOperator)
	viewer, _ := e.asRole("victor", auth.RoleViewer)
	rev := btRevision(t, admin)
	before := e.btState()
	rg := map[string]any{"name": "roles", "members": []any{btMember(e, e.ext101, 1)}}

	e403 := btFail(t, operator.do("POST", "/api/v1/config/batch", btReq(rev, false,
		btOp("", "createRingGroup", nil, rg),
		btOp("", "createPagingGroup", nil, map[string]any{"name": "page", "number": "*88", "members": []any{}}),
	)), http.StatusForbidden, "forbidden_role")
	if !strings.Contains(e403.Error.Message, "operation 1 (createPagingGroup)") {
		t.Fatalf("the refusal does not name the operation: %s", e403.Error.Message)
	}
	if e.btState() != before {
		t.Fatal("a refused batch applied part of itself")
	}
	btFail(t, viewer.do("POST", "/api/v1/config/batch", btReq(rev, true, btOp("", "createRingGroup", nil, rg))), http.StatusForbidden, "forbidden_role")
	// The operator may do everything else: the same batch without the
	// paging group applies.
	operator.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(rev, false, btOp("", "createRingGroup", nil, rg)))

	for _, id := range []string{"createDevice", "createPhone", "rotateDeviceSecret", "revealPhoneAdminPassword", "createToken", "createUser",
		"createAnnouncement", "uploadFirmware", "updateExtensionVoicemail", "controlConference", "setQueueAgent", "putFlowLayout",
		"applyConfigBatch", "applyAIProposal", "nope"} {
		before = e.btState()
		er := btFail(t, admin.do("POST", "/api/v1/config/batch", btReq(btRevision(t, admin), false, btOp("", "createRingGroup", nil, map[string]any{
			"name": "x" + id, "members": []any{btMember(e, e.ext101, 1)}}), btOp("", id, map[string]string{"id": "1"}, map[string]any{}))),
			http.StatusBadRequest, "op_not_batchable")
		if len(er.Error.Errors) != 1 || er.Error.Errors[0].OpIndex != 1 {
			t.Errorf("%s: error = %+v", id, er.Error)
		}
		if e.btState() != before {
			t.Errorf("%s: a refused batch changed the database", id)
		}
	}

	var tooMany []map[string]any
	for range configbatch.MaxOps + 1 {
		tooMany = append(tooMany, btOp("", "deleteRingGroup", map[string]string{"id": "1"}, nil))
	}
	btFail(t, admin.do("POST", "/api/v1/config/batch", btReq(btRevision(t, admin), false, tooMany...)), http.StatusRequestEntityTooLarge, "too_large")

	// Request errors: no baseRevision, no operations, unknown properties,
	// an unresolvable placeholder.
	admin.must(http.StatusBadRequest, "POST", "/api/v1/config/batch", map[string]any{"ops": []any{btOp("", "createRingGroup", nil, rg)}})
	admin.must(http.StatusBadRequest, "POST", "/api/v1/config/batch", map[string]any{"baseRevision": 1, "ops": []any{}, "extra": 1})
	btFail(t, admin.do("POST", "/api/v1/config/batch", btReq(rev, false)), http.StatusUnprocessableEntity, "validation_failed")
	btFail(t, admin.do("POST", "/api/v1/config/batch", btReq(rev, false,
		btOp("", "deleteRingGroup", map[string]string{"id": "${ghost.id}"}, nil))), http.StatusUnprocessableEntity, "validation_failed")
	btFail(t, admin.do("POST", "/api/v1/config/batch", map[string]any{"baseRevision": rev, "canvasId": nil,
		"ops": []any{btOp("", "createRingGroup", nil, rg)}, "layout": []any{map[string]any{"node": "ringGroup:1", "x": 1, "y": 2}}}),
		http.StatusUnprocessableEntity, "validation_failed")
}

func btCanvas(t *testing.T, c *client, name string) string {
	t.Helper()
	return fmt.Sprint(c.must(http.StatusCreated, "POST", "/api/v1/flow/canvases", map[string]any{"name": name}).json(t)["id"])
}

func btNodes(t *testing.T, c *client, canvas string) map[string][2]float64 {
	t.Helper()
	out := map[string][2]float64{}
	for _, n := range c.must(http.StatusOK, "GET", "/api/v1/flow/canvases/"+canvas, nil).json(t)["nodes"].([]any) {
		m := n.(map[string]any)
		out[m["node"].(string)] = [2]float64{m["x"].(float64), m["y"].(float64)}
	}
	return out
}

// TestBatchLayout fails if a batch does not place its new nodes on the
// canvas, bumps the revision twice for layout, or leaves the position of a
// node it deletes.
func TestBatchLayout(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	canvas := btCanvas(t, c, "main")
	rev := btRevision(t, c)
	req := btReq(rev, false, btOp("rg", "createRingGroup", nil, map[string]any{"name": "placed", "members": []any{btMember(e, e.ext101, 1)}}))
	req["canvasId"] = json.Number(canvas)
	req["layout"] = []any{map[string]any{"node": "ringGroup:${rg.id}", "x": 120, "y": 340}}
	got := c.must(http.StatusOK, "POST", "/api/v1/config/batch", req).json(t)
	id := fmt.Sprint(got["results"].([]any)[0].(map[string]any)["id"])
	if pos, ok := btNodes(t, c, canvas)["ringGroup:"+id]; !ok || pos != [2]float64{120, 340} {
		t.Fatalf("canvas nodes = %v", btNodes(t, c, canvas))
	}
	if r := btRevision(t, c); r != rev+1 {
		t.Fatalf("revision = %v, want %v", r, rev+1)
	}

	// A canvas that does not exist, or a node of no catalogue kind, fails the
	// batch and the object with it.
	before := e.btState()
	bad := btReq(btRevision(t, c), false, btOp("", "createRingGroup", nil, map[string]any{"name": "ghost", "members": []any{btMember(e, e.ext101, 1)}}))
	bad["canvasId"] = 999999
	bad["layout"] = []any{map[string]any{"node": "ringGroup:1", "x": 1, "y": 1}}
	btFail(t, c.do("POST", "/api/v1/config/batch", bad), http.StatusUnprocessableEntity, "validation_failed")
	bad["canvasId"] = json.Number(canvas)
	bad["layout"] = []any{map[string]any{"node": "bogus:1", "x": 1, "y": 1}}
	btFail(t, c.do("POST", "/api/v1/config/batch", bad), http.StatusUnprocessableEntity, "validation_failed")
	if e.btState() != before {
		t.Fatal("a batch with bad layout changed the database")
	}

	// Deleting the node prunes its position.
	c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(btRevision(t, c), false, btOp("", "deleteRingGroup", map[string]string{"id": id}, nil)))
	if n := btNodes(t, c, canvas); len(n) != 0 {
		t.Fatalf("canvas nodes after the delete = %v", n)
	}
}

// TestBatchRevert fails if a revert's dry run commits, a stale revert
// applies, a batch reverts twice, an update is not restored to its before,
// a create is not deleted, or a deleted object comes back without its
// members or its place on the canvas.
func TestBatchRevert(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	rg := func(name string) map[string]any {
		return map[string]any{"name": name, "strategy": "sequential", "ringTimeout": 25,
			"members": []any{btMember(e, e.ext101, 1), map[string]any{"extensionId": e.ext102["id"], "position": 2, "delay": 3}}}
	}
	apply := func(ops ...map[string]any) map[string]any {
		t.Helper()
		return c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(btRevision(t, c), false, ops...)).json(t)
	}

	// Create, then revert: a dry run first.
	made := apply(
		btOp("g", "createRingGroup", nil, rg("made")),
		btOp("", "createInboundRoute", nil, map[string]any{"name": "To made", "destinationKind": "ring_group", "destination": "${g.name}"}),
	)
	batch := made["batchId"].(string)
	gid := fmt.Sprint(made["results"].([]any)[0].(map[string]any)["id"])
	before := e.btState()
	dry := c.must(http.StatusOK, "POST", "/api/v1/config/batches/"+batch+"/revert", map[string]any{"dryRun": true}).json(t)
	if dry["applied"] != false || len(dry["results"].([]any)) != 2 || dry["results"].([]any)[0].(map[string]any)["status"] != "deleted" {
		t.Fatalf("revert dry run = %v", dry)
	}
	if e.btState() != before {
		t.Fatal("a revert dry run changed the database")
	}
	rev := btRevision(t, c)
	done := c.must(http.StatusOK, "POST", "/api/v1/config/batches/"+batch+"/revert", nil).json(t)
	if done["applied"] != true || done["revision"] != rev+1 || done["batchId"] == nil {
		t.Fatalf("revert = %v", done)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/ring-groups/"+gid, nil)
	if n := len(listItems(t, c.must(http.StatusOK, "GET", "/api/v1/routes/inbound", nil))); n != 0 {
		t.Fatalf("%d inbound routes after the revert", n)
	}
	list := c.must(http.StatusOK, "GET", "/api/v1/config/batches", nil).json(t)["items"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["reverts"] != batch || list[1].(map[string]any)["revertedBy"] != done["batchId"] {
		t.Fatalf("batches = %v", list)
	}
	btFail(t, c.do("POST", "/api/v1/config/batches/"+batch+"/revert", nil), http.StatusConflict, "conflict")
	c.must(http.StatusNotFound, "POST", "/api/v1/config/batches/00000000-0000-0000-0000-000000000000/revert", nil)
	c.must(http.StatusNotFound, "POST", "/api/v1/config/batches/nope/revert", nil)
	c.must(http.StatusBadRequest, "POST", "/api/v1/config/batches/"+batch+"/revert", "{not json")
	c.must(http.StatusBadRequest, "POST", "/api/v1/config/batches/"+batch+"/revert", map[string]any{"dryRunn": true})

	// An update is restored to what it was.
	orig := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", rg("steady")).json(t)
	oid := fmt.Sprint(orig["id"])
	upd := apply(btOp("", "updateRingGroup", map[string]string{"id": oid}, map[string]any{"strategy": "weighted", "ringTimeout": 40}))
	if g := c.must(http.StatusOK, "GET", "/api/v1/ring-groups/"+oid, nil).json(t); g["strategy"] != "weighted" || g["ringTimeout"] != float64(40) {
		t.Fatalf("after the update: %v", g)
	}

	// A revert over a change made since is refused and changes nothing.
	c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+oid, map[string]any{"ringTimeout": 50})
	before = e.btState()
	err := btFail(t, c.do("POST", "/api/v1/config/batches/"+upd["batchId"].(string)+"/revert", nil), http.StatusConflict, "config_stale")
	if len(err.Error.Conflicts) != 1 || err.Error.Conflicts[0].OpIndex != 0 || err.Error.Conflicts[0].Node != "ringGroup:"+oid {
		t.Fatalf("conflicts = %+v", err.Error.Conflicts)
	}
	if e.btState() != before {
		t.Fatal("a stale revert changed the database")
	}
	c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+oid, map[string]any{"ringTimeout": 40})
	c.must(http.StatusOK, "POST", "/api/v1/config/batches/"+upd["batchId"].(string)+"/revert", nil)
	if g := c.must(http.StatusOK, "GET", "/api/v1/ring-groups/"+oid, nil).json(t); g["strategy"] != "sequential" || g["ringTimeout"] != float64(25) {
		t.Fatalf("after reverting the update: %v", g)
	}

	// A batch that deleted something a batch cannot make again (a device,
	// whose secret is shown once) cannot be reverted: 422, nothing changes.
	dev := c.must(http.StatusCreated, "POST", "/api/v1/devices", map[string]any{"extensionId": e.ext101["id"], "sipUsername": "desk101"}).json(t)
	gone := apply(btOp("", "deleteDevice", map[string]string{"id": fmt.Sprint(dev["id"])}, nil))
	before = e.btState()
	btFail(t, c.do("POST", "/api/v1/config/batches/"+gone["batchId"].(string)+"/revert", nil), http.StatusUnprocessableEntity, "validation_failed")
	if e.btState() != before {
		t.Fatal("a revert that cannot be built changed the database")
	}

	// A delete is reverted by making the object again, with its members, and
	// its place on the canvas moves to the new id.
	canvas := btCanvas(t, c, "layout")
	c.must(http.StatusOK, "PUT", "/api/v1/flow/canvases/"+canvas+"/layout", map[string]any{
		"layoutVersion": 0, "nodes": []any{map[string]any{"node": "ringGroup:" + oid, "x": 77, "y": 88}}})
	del := apply(btOp("", "deleteRingGroup", map[string]string{"id": oid}, nil))
	c.must(http.StatusNotFound, "GET", "/api/v1/ring-groups/"+oid, nil)
	if n := btNodes(t, c, canvas); len(n) != 0 {
		t.Fatalf("the deleted node kept its position: %v", n)
	}
	back := c.must(http.StatusOK, "POST", "/api/v1/config/batches/"+del["batchId"].(string)+"/revert", nil).json(t)
	nid := fmt.Sprint(back["results"].([]any)[0].(map[string]any)["id"])
	if nid == oid {
		t.Fatalf("the re-created group has the old id %s", nid)
	}
	g := c.must(http.StatusOK, "GET", "/api/v1/ring-groups/"+nid, nil).json(t)
	if g["name"] != "steady" || g["strategy"] != "sequential" || g["ringTimeout"] != float64(25) || len(g["members"].([]any)) != 2 {
		t.Fatalf("re-created group = %v", g)
	}
	if pos, ok := btNodes(t, c, canvas)["ringGroup:"+nid]; !ok || pos != [2]float64{77, 88} {
		t.Fatalf("canvas nodes = %v, want the position moved to the new id", btNodes(t, c, canvas))
	}
}

// TestBatchableHandlersNoExternalEffects fails if the handler of a batchable
// operation (or a server method it calls) reaches object storage, a speech
// provider, a page test, the queue state or SMTP: those effects do not roll
// back with the transaction, so the operation would have to be refused
// (spec flow-canvas, failure modes).
func TestBatchableHandlersNoExternalEffects(t *testing.T) {
	spec, err := batchSpec()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{} // "METHOD pattern" -> handler method name
	for _, r := range (&server{}).routes() {
		full := runtime.FuncForPC(reflect.ValueOf(r.H).Pointer()).Name() // ...api.(*server).createRingGroup-fm
		short := strings.TrimSuffix(full, "-fm")
		if i := strings.Index(short, "(*server)."); i >= 0 {
			short, _, _ = strings.Cut(short[i+len("(*server)."):], ".") // a closure: the method that makes it
		}
		names[r.Method+" "+r.Pattern] = short
	}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]*ast.FuncDecl{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && len(fd.Recv.List) == 1 {
				if star, ok := fd.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok && id.Name == "server" {
						methods[fd.Name.Name] = fd
					}
				}
			}
		}
	}
	forbidden := []string{"Objects", "TTS", "speech", "PageTests", "Queues", "SMTPDeliver", "SMTPEnv", "Cluster", "Diagnostics"}
	var walk func(name string, seen map[string]bool) []string
	walk = func(name string, seen map[string]bool) []string {
		fd, ok := methods[name]
		if !ok || seen[name] {
			return nil
		}
		seen[name] = true
		recv := "s"
		if names := fd.Recv.List[0].Names; len(names) == 1 {
			recv = names[0].Name
		}
		var hits []string
		ast.Inspect(fd, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
				if slices.Contains(forbidden, sel.Sel.Name) {
					hits = append(hits, name+" uses s."+sel.Sel.Name)
				}
				hits = append(hits, walk(sel.Sel.Name, seen)...)
			}
			return true
		})
		return hits
	}
	checked := 0
	for _, id := range configbatch.Sorted() {
		op := spec[id]
		h, ok := names[op.Method+" "+op.Path]
		if !ok {
			t.Errorf("%s: no route for %s %s", id, op.Method, op.Path)
			continue
		}
		if _, ok := methods[h]; !ok {
			t.Errorf("%s: handler %s not found in the package source", id, h)
			continue
		}
		checked++
		for _, hit := range walk(h, map[string]bool{}) {
			t.Errorf("%s is batchable but its handler has an effect that does not roll back: %s", id, hit)
		}
	}
	if checked != len(configbatch.Ops) {
		t.Errorf("checked %d of %d batchable operations", checked, len(configbatch.Ops))
	}
}

// TestFlowWarningEnum fails if the FlowWarning code enum in openapi.json
// differs from the warning codes the graph builder can raise: a batch and the
// graph answer with them, and a code missing from the enum fails response
// validation.
func TestFlowWarningEnum(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPI, &doc); err != nil {
		t.Fatal(err)
	}
	got := slices.Clone(doc.Components.Schemas["FlowWarning"].Properties["code"].Enum)
	want := []string{flow.WarnDangling, flow.WarnEmptyGroup, flow.WarnVoicemailDisable, flow.WarnUnreachable, flow.WarnLoop,
		flow.WarnShadowedRoute, flow.WarnNoGreeting}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("FlowWarning.code enum = %v, want %v", got, want)
	}
}
