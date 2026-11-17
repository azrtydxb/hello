package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
	"github.com/azrtydxb/hello/internal/secret"
)

// recordingStore is a store whose pool and batch transaction both run on d,
// and a context that joins the batch, so methods that open a transaction run
// without a database. Every statement d receives is one momo built.
func recordingStore(d *fakeDriver) (*Store, context.Context) {
	h := dbConn{momo.New(d)}
	s := &Store{db: &dbPool{dbConn: h}, prov: ProvSettings{Deployment: map[prov.Vendor]bool{prov.Yealink: true}}}
	b := &Batch{s: s, tx: &dbTx{dbConn: h}}
	return s, context.WithValue(context.Background(), batchKey{}, b)
}

// TestProvRuntimeStatements builds the provisioning statements that are
// assembled at run time, which TestStatementsBuild cannot see (a caller's
// VALUES or SELECT for upsertJob, the redirect queue's job updates, the
// group push's phone list), and pins the statements other parts of Hello
// depend on: the push claim lock key, and the device secret lookup hello-sip
// runs on every challenge (one text parameter, the SIP username).
func TestProvRuntimeStatements(t *testing.T) {
	d := &fakeDriver{rows: [][]any{{int64(1)}}, affected: 1}
	s, ctx := recordingStore(d)
	tx := s.batchOf(ctx).tx

	sent := func() []sentStmt { out := d.sent; d.sent = nil; return out }
	want := func(t *testing.T, got []sentStmt, sql string, nargs int) {
		t.Helper()
		for _, g := range got {
			if strings.Join(strings.Fields(g.sql), " ") == sql {
				if len(g.args) != nargs {
					t.Errorf("%q sent with %d arguments, want %d", sql, len(g.args), nargs)
				}
				return
			}
		}
		var have []string
		for _, g := range got {
			have = append(have, strings.Join(strings.Fields(g.sql), " "))
		}
		t.Errorf("not sent: %q\nsent: %q", sql, have)
	}
	const upsertTail = "ON CONFLICT (vendor, mac) DO UPDATE SET seq = nextval('prov_redirect_jobs_seq'), op = EXCLUDED.op, attempts = 0, next_attempt_at = now(), last_error = '', first_queued_at = CASE WHEN prov_redirect_jobs.op = EXCLUDED.op THEN prov_redirect_jobs.first_queued_at ELSE now() END"

	t.Run("upsertJob", func(t *testing.T) {
		if _, err := s.queueRedirect(ctx, tx, prov.Yealink, "001565000001", redirect.OpRegister); err != nil {
			t.Fatal(err)
		}
		if err := s.queueVendor(ctx, tx, prov.Yealink); err != nil {
			t.Fatal(err)
		}
		got := sent()
		want(t, got, "INSERT INTO prov_redirect_jobs (vendor, mac, op) VALUES ($1, $2, $3) "+upsertTail, 3)
		want(t, got, "INSERT INTO prov_redirect_jobs (vendor, mac, op) SELECT vendor, mac, 'register' FROM phones WHERE vendor = $1 "+upsertTail, 1)
		want(t, got, "UPDATE phones SET redirect_status = $1 WHERE vendor = $2", 2)
	})

	t.Run("redirect jobs", func(t *testing.T) {
		q := s.Redirect().(redirectQueue)
		j := redirect.Job{Seq: 7, Vendor: prov.Yealink, MAC: "001565000001"}
		st := &redirect.Status{State: redirect.StatePending}
		if err := q.FinishJob(ctx, j, st); err != nil {
			t.Fatal(err)
		}
		if err := q.RetryJob(ctx, j, time.Now(), &redirect.Status{State: redirect.StateFailed, Reason: "x"}); err != nil {
			t.Fatal(err)
		}
		got := sent()
		want(t, got, "DELETE FROM prov_redirect_jobs WHERE vendor = $1 AND mac = $2 AND seq = $3", 3)
		want(t, got, "UPDATE prov_redirect_jobs SET attempts = attempts + 1, next_attempt_at = $1, last_error = COALESCE($2, last_error) WHERE vendor = $3 AND mac = $4 AND seq = $5", 5)
		want(t, got, "UPDATE phones SET redirect_status = $1 WHERE mac = $2 AND vendor = $3", 3)
	})

	t.Run("group push", func(t *testing.T) {
		// The fake answers every query with one row of 1, so the push of
		// that phone stops at the eligibility read; the list is what counts.
		_, _, _ = s.QueuePushGroup(ctx, "admin", 5, "check-sync")
		want(t, sent(), "SELECT id FROM phones WHERE group_id = $1 ORDER BY id", 1)
	})

	// The fake's one-column row fits none of the reads below: answer them
	// with no rows.
	d.rows = nil

	t.Run("push list", func(t *testing.T) {
		if _, err := s.ListPhonePushes(ctx, 3, 10); err != nil {
			t.Fatal(err)
		}
		want(t, sent(), "SELECT "+strings.Join(strings.Fields(pushCols), " ")+" FROM phone_pushes WHERE phone_id = $1 ORDER BY id DESC LIMIT $2", 2)
	})

	t.Run("push claim lock", func(t *testing.T) {
		_, _ = s.ClaimPushes(ctx, "node-a", 30)
		got := sent()
		if len(got) == 0 || got[0].sql != `SELECT pg_advisory_xact_lock($1)` || fmt.Sprint(got[0].args) != fmt.Sprint([]any{int64(pushLockKey)}) {
			t.Fatalf("first statement %v, want the push lock with key %#x", got, pushLockKey)
		}
	})

	t.Run("device secret", func(t *testing.T) {
		// No row comes back, so the box never opens anything.
		s.box = &secret.Box{}
		if _, err := s.DeviceSecret(ctx, "dev-1"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("DeviceSecret of no device = %v, want sql.ErrNoRows", err)
		}
		got := sent()
		if len(got) != 1 || got[0].sql != `SELECT id, secret_enc FROM devices WHERE sip_username = $1 AND enabled` ||
			len(got[0].args) != 1 || got[0].args[0] != "dev-1" {
			t.Fatalf("DeviceSecret sent %d statements; want its one lookup by SIP username", len(got))
		}
	})
}
