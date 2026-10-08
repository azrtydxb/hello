package proposal_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/ai/proposal"
)

func (e *env) validate(actions ...proposal.Action) (*proposal.Draft, error) {
	d := &proposal.Draft{Source: proposal.SourceAssistant, Title: "t", Actions: actions}
	return d, e.val.Validate(context.Background(), e.ident(), d)
}

func act(op string, params map[string]string, body any) proposal.Action {
	a := proposal.Action{OperationID: op, PathParams: params}
	if body != nil {
		a.Body = raw(body)
	}
	return a
}

// TestProposalValidation (allowlist half): every allowlisted operation is in
// the document, write-scoped and secret-free, and nothing else gets through.
// Fails if an operation outside the allowlist, a body with an unknown or
// mistyped property, a credential property, a missing target, a path
// parameter that adds segments, a ninth action or a routing table that does
// not compile is accepted, or if the before/after are wrong.
func TestProposalValidation(t *testing.T) {
	if err := proposal.CheckDocument(loadSpec(t)); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	s := e.seed()
	ext := map[string]string{"id": s.ext1}

	t.Run("allowlist", func(t *testing.T) {
		for _, id := range []string{"createDevice", "createPhone", "deleteExtension", "deleteTrunk", "deleteDevice", "createToken",
			"updateUser", "createServiceAccount", "deleteVoicemailMessage", "deleteRecording", "rotateDeviceSecret", "noSuchOperation"} {
			if _, err := e.validate(act(id, map[string]string{"id": "1"}, map[string]any{})); err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Errorf("%s: err = %v, want not allowed", id, err)
			}
		}
	})

	t.Run("schema", func(t *testing.T) {
		for name, body := range map[string]any{
			"unknown property": map[string]any{"name": "x", "nope": 1},
			"wrong type":       map[string]any{"name": 5},
			"not an object":    []int{1},
		} {
			if _, err := e.validate(act("updateExtension", ext, body)); err == nil || !strings.Contains(err.Error(), "request schema") {
				t.Errorf("%s: err = %v, want a request schema error", name, err)
			}
		}
		if _, err := e.validate(act("updateExtension", ext, nil)); err == nil {
			t.Error("a missing body was accepted")
		}
	})

	t.Run("credentials", func(t *testing.T) {
		tr := map[string]string{"id": s.trunk}
		for name, a := range map[string]proposal.Action{
			"trunk password":   act("updateTrunk", tr, map[string]any{"password": "hunter2"}),
			"trunk nested":     act("updateTrunk", tr, map[string]any{"destinations": []map[string]any{{"host": "h", "sipPassword": "x"}}}),
			"extension secret": act("updateExtension", ext, map[string]any{"name": "x", "secret": "x"}),
		} {
			_, err := e.validate(a)
			if err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
		_, err := e.validate(act("updateTrunk", tr, map[string]any{"password": "hunter2"}))
		if err == nil || !strings.Contains(err.Error(), "credential") {
			t.Errorf("trunk password err = %v, want the credential refusal", err)
		}
	})

	t.Run("targets", func(t *testing.T) {
		for name, p := range map[string]map[string]string{
			"missing":   {"id": "99999"},
			"segments":  {"id": s.ext1 + "/../../users"},
			"query":     {"id": s.ext1 + "?x=1"},
			"unknown":   {"id": s.ext1, "other": "1"},
			"no params": nil,
		} {
			if _, err := e.validate(act("updateExtension", p, map[string]any{"name": "x"})); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	})

	t.Run("limits", func(t *testing.T) {
		a := act("updateExtension", ext, map[string]any{"name": "x"})
		if _, err := e.validate(); err == nil {
			t.Error("zero actions accepted")
		}
		nine := make([]proposal.Action, proposal.MaxActions+1)
		for i := range nine {
			nine[i] = a
		}
		if _, err := e.validate(nine...); err == nil {
			t.Error("nine actions accepted")
		}
		if _, err := e.validate(nine[:proposal.MaxActions]...); err != nil {
			t.Errorf("eight actions: %v", err)
		}
		d := &proposal.Draft{Source: "assistant", Actions: []proposal.Action{a}}
		if err := e.val.Validate(context.Background(), e.ident(), d); err == nil {
			t.Error("a proposal without a title was accepted")
		}
	})

	t.Run("before and after", func(t *testing.T) {
		d, err := e.validate(
			act("updateExtension", ext, map[string]any{"name": "Reception"}),
			act("createOutboundRoute", nil, map[string]any{"name": "Intl", "matchKind": "prefix", "match": "00", "trunks": []json.Number{json.Number(s.trunk)}}),
		)
		if err != nil {
			t.Fatal(err)
		}
		var before, after map[string]any
		if err := json.Unmarshal(d.Actions[0].Before, &before); err != nil || before["name"] != "Desk" {
			t.Errorf("patch before = %s", d.Actions[0].Before)
		}
		if err := json.Unmarshal(d.Actions[0].After, &after); err != nil || after["name"] != "Reception" || after["number"] != "101" {
			t.Errorf("patch after = %s, want the merge", d.Actions[0].After)
		}
		if len(d.Actions[1].Before) != 0 || string(d.Actions[1].After) != string(d.Actions[1].Body) {
			t.Errorf("create before = %s after = %s", d.Actions[1].Before, d.Actions[1].After)
		}
		ch, err := proposal.Diff(d.Actions[0].Before, d.Actions[0].After)
		if err != nil || len(ch) != 1 || ch[0].Path != "/name" || ch[0].After != "Reception" {
			t.Errorf("diff = %v %v", ch, err)
		}
	})

	t.Run("route dry run", func(t *testing.T) {
		_, err := e.validate(act("createOutboundRoute", nil, map[string]any{"name": "Bad", "matchKind": "regex", "match": "(", "trunks": []json.Number{json.Number(s.trunk)}}))
		if err == nil || !strings.Contains(err.Error(), "would not compile") {
			t.Errorf("an invalid regex route: err = %v, want the dry run to refuse it", err)
		}
		if _, err := e.validate(act("updateOutboundRoute", map[string]string{"id": s.route}, map[string]any{"matchKind": "regex", "match": "("})); err == nil {
			t.Error("an update that breaks the table was accepted")
		}
	})
}

// TestProposalDeleteValidation: deletes of ring groups and routes are
// accepted, marked destructive, computed with an empty after, and list what
// they take with them and what points at them; no other delete passes. Fails
// if a delete diff does not show its references.
func TestProposalDeleteValidation(t *testing.T) {
	e := newEnv(t)
	s := e.seed()
	// An inbound route that sends calls to the ring group by its name.
	if _, err := e.db.Exec(`INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
		VALUES (1, 'Main', 'any', 'extension', 'sales')`); err != nil {
		t.Fatal(err)
	}

	d, err := e.validate(act("deleteRingGroup", map[string]string{"id": s.group}, nil))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Actions[0]
	if !a.Destructive || len(a.After) != 0 || len(a.Before) == 0 {
		t.Fatalf("delete action = %+v", a)
	}
	kinds := map[string]bool{}
	for _, r := range a.References {
		kinds[r.Kind+":"+r.Detail] = true
	}
	if len(a.References) != 3 || !kinds["inbound_route:sends calls to the group (extension)"] || !kinds["extension:member of the group"] {
		t.Errorf("ring group references = %+v, want 2 members and the inbound route", a.References)
	}
	ch, err := proposal.Diff(a.Before, a.After)
	if err != nil || len(ch) == 0 {
		t.Fatalf("the delete diff shows nothing: %v %v", ch, err)
	}
	for _, c := range ch {
		if c.After != nil || c.Before == nil {
			t.Errorf("delete diff change %+v is not a removal", c)
		}
	}

	d, err = e.validate(act("deleteOutboundRoute", map[string]string{"id": s.route}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Actions[0].Destructive || len(d.Actions[0].References) != 1 || d.Actions[0].References[0].Kind != "trunk" {
		t.Errorf("outbound route delete = %+v, want its trunk listed", d.Actions[0])
	}
	var in map[string]any
	var inID string
	if err := e.db.QueryRow(`SELECT id::text FROM inbound_routes`).Scan(&inID); err != nil {
		t.Fatal(err)
	}
	if d, err = e.validate(act("deleteInboundRoute", map[string]string{"id": inID}, nil)); err != nil {
		t.Fatal(err)
	}
	_ = in
	if len(d.Actions[0].References) == 0 {
		t.Error("inbound route delete lists no references")
	}

	for _, op := range []string{"deleteTrunk", "deleteExtension", "deleteDevice", "deletePhone", "deleteVoicemailMessage"} {
		if _, err := e.validate(act(op, map[string]string{"id": "1"}, nil)); err == nil {
			t.Errorf("%s was accepted", op)
		}
	}
	if _, err := e.validate(act("deleteRingGroup", map[string]string{"id": "99999"}, nil)); err == nil {
		t.Error("a delete of a missing group was accepted")
	}
	if _, err := e.validate(act("deleteRingGroup", map[string]string{"id": s.group}, map[string]any{"x": 1})); err == nil {
		t.Error("a delete with a body was accepted")
	}
}
