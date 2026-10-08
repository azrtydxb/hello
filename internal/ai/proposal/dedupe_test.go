package proposal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/azrtydxb/hello/internal/ai/proposal"
)

func (e *env) draft(source string, session *string, actions ...proposal.Action) proposal.Draft {
	e.t.Helper()
	d := proposal.Draft{Source: source, Title: "T", Rationale: "r1", SessionID: session, Actions: actions}
	if err := e.val.Validate(context.Background(), e.ident(), &d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

// TestProposalDedupe: the same actions from the same source refresh the open
// proposal instead of duplicating it, a newer proposal on the same targets
// from the same session supersedes the older, a dismissed fingerprint is
// suppressed for 7 days and not after, and the fingerprint ignores read
// state and key order. Fails if a duplicate row appears, an unrelated or
// create-only proposal is superseded, or a dismissed one comes back early.
func TestProposalDedupe(t *testing.T) {
	e := newEnv(t)
	s := e.seed()
	ctx := context.Background()
	var sess string
	owner := e.users["operator"]
	if err := e.db.QueryRow(`INSERT INTO ai_sessions (id, owner, title) VALUES (gen_random_uuid(), $1, 's') RETURNING id::text`, owner).Scan(&sess); err != nil {
		t.Fatal(err)
	}
	ext1 := map[string]string{"id": s.ext1}
	rename := func(n string) proposal.Action { return act("updateExtension", ext1, map[string]any{"name": n}) }
	count := func(where string, args ...any) (n int) {
		t.Helper()
		if err := e.db.QueryRow(`SELECT count(*) FROM ai_proposals WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	d := e.draft("assistant", &sess, rename("A"))
	id1, err := e.ps.Upsert(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	// Same actions: refreshed, not duplicated; the revision and text move.
	e.must("admin", 201, "POST", "/api/v1/extensions", map[string]any{"number": "103", "name": "x"})
	d2 := e.draft("assistant", &sess, rename("A"))
	d2.Rationale = "refreshed"
	id2, err := e.ps.Upsert(ctx, d2)
	if err != nil || id2 != id1 || count(`status = 'open'`) != 1 {
		t.Fatalf("refresh: id %s vs %s, err %v, open %d", id2, id1, err, count(`status = 'open'`))
	}
	p, err := e.ps.Get(ctx, id1)
	if err != nil || p.Rationale != "refreshed" || p.ConfigRevision != e.revision() {
		t.Fatalf("refreshed proposal = %+v %v", p, err)
	}

	// Another source with the same actions is its own proposal.
	if id3, err := e.ps.Upsert(ctx, e.draft("finding:x", nil, rename("A"))); err != nil || id3 == id1 {
		t.Fatalf("other source: %s %v", id3, err)
	}

	// Same target, different change, same session: the older is superseded.
	idNew, err := e.ps.Upsert(ctx, e.draft("assistant", &sess, rename("B")))
	if err != nil || idNew == id1 {
		t.Fatalf("newer: %s %v", idNew, err)
	}
	if st := e.status(id1); st != "superseded" {
		t.Fatalf("older status = %s, want superseded", st)
	}
	if st := e.status(idNew); st != "open" {
		t.Fatalf("newer status = %s", st)
	}
	// A different target in the same session leaves it open.
	idOther, err := e.ps.Upsert(ctx, e.draft("assistant", &sess, act("updateExtension", map[string]string{"id": s.ext2}, map[string]any{"name": "C"})))
	if err != nil || e.status(idNew) != "open" || e.status(idOther) != "open" {
		t.Fatalf("other target superseded something: %v %s %s", err, e.status(idNew), e.status(idOther))
	}

	// Dismissed: suppressed for 7 days.
	if err := e.ps.Dismiss(ctx, idNew, e.users["operator"], proposal.ReasonWrong, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ps.Upsert(ctx, e.draft("assistant", &sess, rename("B"))); !errors.Is(err, proposal.ErrSuppressed) {
		t.Fatalf("dismissed fingerprint: err = %v, want ErrSuppressed", err)
	}
	if _, err := e.db.Exec(`UPDATE ai_proposals SET dismissed_at = now() - interval '8 days' WHERE id = $1`, idNew); err != nil {
		t.Fatal(err)
	}
	if id4, err := e.ps.Upsert(ctx, e.draft("assistant", &sess, rename("B"))); err != nil || id4 == idNew {
		t.Fatalf("after 7 days: %s %v", id4, err)
	}
}

// TestFingerprint: key order, whitespace and read state do not change it;
// an operation, parameter or body change does.
func TestFingerprint(t *testing.T) {
	a := []proposal.Action{{OperationID: "updateExtension", PathParams: map[string]string{"id": "1"},
		Body: []byte(`{"name":"x","number":"1"}`), Before: []byte(`{"a":1}`)}}
	b := []proposal.Action{{OperationID: "updateExtension", PathParams: map[string]string{"id": "1"},
		Body: []byte("{ \"number\": \"1\",\n \"name\": \"x\" }"), Before: []byte(`{"a":2}`), After: []byte(`{}`)}}
	fa, fb := proposal.Fingerprint("assistant", a), proposal.Fingerprint("assistant", b)
	if fa != fb || len(fa) != len("assistant:")+16 {
		t.Fatalf("fingerprints %q %q", fa, fb)
	}
	for name, c := range map[string][]proposal.Action{
		"op":     {{OperationID: "updateDevice", PathParams: a[0].PathParams, Body: a[0].Body}},
		"param":  {{OperationID: "updateExtension", PathParams: map[string]string{"id": "2"}, Body: a[0].Body}},
		"body":   {{OperationID: "updateExtension", PathParams: a[0].PathParams, Body: []byte(`{"name":"y"}`)}},
		"second": append(a, a...),
	} {
		if proposal.Fingerprint("assistant", c) == fa {
			t.Errorf("%s change kept the fingerprint", name)
		}
	}
	if proposal.Fingerprint("finding:x", a) == fa {
		t.Error("the source is not part of the fingerprint")
	}
}
