package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// provStore is a store on a scratch database with provisioning settings,
// a sealing box and extension 101; it skips without
// HELLO_TEST_DATABASE_URL.
func provStore(t *testing.T) (*Store, *sql.DB, int64) {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_store_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	box, err := secret.New(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	st := New(db).WithSecretBox(box).WithProv(ProvSettings{
		PublicURL: "https://prov.hello.test", SIPServer: "192.0.2.1:5060", Domain: "hello.test",
		Expiry: time.Hour, Resync: 24 * time.Hour, TokenGrace: time.Hour,
		Deployment: map[prov.Vendor]bool{prov.Snom: true},
	})
	ext, err := st.CreateExtension(ctx, "test", "101", "Sales", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return st, db, ext.ID
}

// TestClaimBootConcurrent fails if two concurrent boot claims for one
// armed MAC both get the hand-off, if the loser does not flag the phone
// boot_reclaimed, or if a claim for an unallowlisted phone disarms it.
func TestClaimBootConcurrent(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000001", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []*prov.ProvInfo
	)
	for range 8 {
		wg.Go(func() {
			_, h, err := st.ClaimBoot(ctx, "805ec0000001")
			if err != nil {
				t.Error(err)
				return
			}
			if h != nil {
				mu.Lock()
				wins = append(wins, h)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("%d claims got the hand-off, want 1", len(wins))
	}
	if want := "https://prov.hello.test/p/" + c.Token + "/"; wins[0].URL != want || wins[0].CAURL != "http://prov.hello.test/p/ca.crt" {
		t.Fatalf("hand-off = %+v, want URL %s", wins[0], want)
	}
	var armed, reclaimed bool
	if err := db.QueryRow(`SELECT boot_armed, boot_reclaimed FROM phones WHERE id = $1`, c.Phone.ID).Scan(&armed, &reclaimed); err != nil || armed || !reclaimed {
		t.Fatalf("armed %v reclaimed %v (%v)", armed, reclaimed, err)
	}
	if _, _, err := st.ClaimBoot(ctx, "000000000000"); !errors.Is(err, prov.ErrNotFound) {
		t.Fatalf("unknown MAC = %v", err)
	}

	// An armed phone that is not allowlisted is left armed.
	off, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000002", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: false, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	if rec, h, err := st.ClaimBoot(ctx, "805ec0000002"); err != nil || h != nil || rec.Allowlisted || !rec.BootArmed {
		t.Fatalf("unallowlisted claim = %+v, %v, %v", rec, h, err)
	}
	_ = off
}

// TestRedirectQueueReplace fails if a job replaced between Target and
// FinishJob (a rotation's new URL) is removed or its status overwritten by
// the finish of the job read before it, or if a retry of a stale job
// counts against the new one.
func TestRedirectQueueReplace(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	q := st.Redirect()
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "000413000001", Vendor: prov.Snom, Model: "D785",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Phone.RedirectStatus.State != redirect.StatePending {
		t.Fatalf("status = %+v, want pending (deployment credentials)", c.Phone.RedirectStatus)
	}
	jobs, err := q.DueJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].Op != redirect.OpRegister {
		t.Fatalf("due = %+v, %v", jobs, err)
	}
	old := jobs[0]
	tg, err := q.Target(ctx, prov.Snom, "000413000001")
	if err != nil || tg.URL != "https://prov.hello.test/p/"+c.Token+"/{mac}" {
		t.Fatalf("target = %+v, %v", tg, err)
	}
	// A rotation lands between Target and FinishJob.
	rot, err := st.RotatePhoneToken(ctx, "test", c.Phone.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.FinishJob(ctx, old, &redirect.Status{State: redirect.StateRegistered, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := q.RetryJob(ctx, old, time.Now().Add(time.Hour), &redirect.Status{State: redirect.StateFailed, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	jobs, err = q.DueJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].Seq == old.Seq || jobs[0].Attempts != 0 {
		t.Fatalf("after the stale finish, due = %+v (%v), want the replacement untouched", jobs, err)
	}
	p, _ := st.GetPhone(ctx, c.Phone.ID)
	if p.RedirectStatus.State != redirect.StatePending {
		t.Fatalf("stale finish overwrote the status: %+v", p.RedirectStatus)
	}
	if tg, _ := q.Target(ctx, prov.Snom, "000413000001"); tg.URL != "https://prov.hello.test/p/"+rot.Token+"/{mac}" {
		t.Fatalf("target after rotation = %s", tg.URL)
	}
	if err := q.FinishJob(ctx, jobs[0], &redirect.Status{State: redirect.StateRegistered}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM prov_redirect_jobs`).Scan(&n)
	if p, _ := st.GetPhone(ctx, c.Phone.ID); n != 0 || p.RedirectStatus.State != redirect.StateRegistered {
		t.Fatalf("jobs %d, status %+v after the current finish", n, p.RedirectStatus)
	}
	if reg, err := q.Registered(ctx, prov.Snom); err != nil || len(reg) != 1 {
		t.Fatalf("registered = %+v, %v", reg, err)
	}
	if _, err := q.Target(ctx, prov.Yealink, "000413000001"); !errors.Is(err, prov.ErrNotFound) {
		t.Fatalf("target of another vendor = %v", err)
	}
}

// TestRedirectQueueNilStatusAndDrift fails if a nil status overwrites the
// phone's status or the retry's last error, or the drift-check time does
// not round-trip through the settings row.
func TestRedirectQueueNilStatusAndDrift(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	q := st.Redirect()
	if last, err := q.LastDriftCheck(ctx); err != nil || !last.IsZero() {
		t.Fatalf("last drift check before any = %v, %v; want zero", last, err)
	}
	at := time.Date(2026, 10, 7, 3, 4, 5, 0, time.UTC)
	if err := q.SetLastDriftCheck(ctx, at); err != nil {
		t.Fatal(err)
	}
	if last, err := q.LastDriftCheck(ctx); err != nil || !last.Equal(at) {
		t.Fatalf("last drift check = %v, %v; want %v", last, err, at)
	}

	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "000413000002", Vendor: prov.Snom, Model: "D785",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := q.DueJobs(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("due = %+v, %v", jobs, err)
	}
	if err := q.RetryJob(ctx, jobs[0], time.Now().Add(-time.Second), &redirect.Status{State: redirect.StatePending, Reason: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := q.RetryJob(ctx, jobs[0], time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
	var (
		attempts int
		lastErr  string
	)
	if err := db.QueryRow(`SELECT attempts, COALESCE(last_error, '') FROM prov_redirect_jobs`).Scan(&attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || lastErr != "first" {
		t.Fatalf("after a nil retry: attempts %d, last error %q; want 2, \"first\"", attempts, lastErr)
	}
	if p, _ := st.GetPhone(ctx, c.Phone.ID); p.RedirectStatus.Reason != "first" {
		t.Fatalf("nil retry changed the status: %+v", p.RedirectStatus)
	}
	if err := q.FinishJob(ctx, jobs[0], nil); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM prov_redirect_jobs`).Scan(&n)
	if p, _ := st.GetPhone(ctx, c.Phone.ID); n != 0 || p.RedirectStatus.State != redirect.StatePending || p.RedirectStatus.Reason != "first" {
		t.Fatalf("jobs %d, status %+v after a nil finish; want 0 and the status unchanged", n, p.RedirectStatus)
	}
}

// TestRenderInputsFirmwareAndFetches fails if the more specific pin does
// not win, the firmware URL does not carry the current token, or fetch
// rows are not written and pruned.
func TestRenderInputsFirmwareAndFetches(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000003", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test", BLF: []string{"101"}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, name := range []string{"t5x.rom", "t54w.rom"} {
		f, err := st.CreateFirmware(ctx, "test", prov.Firmware{Vendor: prov.Yealink, ModelGlob: "T5*", Version: fmt.Sprint(i),
			Filename: name, ObjectKey: "yealink/" + fmt.Sprint(i) + "/" + name, SHA256: fmt.Sprintf("%064d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	if _, err := st.PutFirmwarePins(ctx, "test", []FirmwarePin{{prov.Yealink, "T5*", ids[0]}, {prov.Yealink, "T54?", ids[1]}}); err != nil {
		t.Fatal(err)
	}
	d, _, err := st.RenderInputs(ctx, c.Phone.ID)
	if err != nil && !errors.Is(err, prov.ErrNoTemplate) {
		t.Fatal(err)
	}
	if d.Firmware == nil || d.Firmware.URL != "https://prov.hello.test/p/"+c.Token+"/fw/t54w.rom" ||
		d.Line.Username != "101-000003" || d.Server.Port != 5060 || len(d.BLF) != 1 || d.BLF[0].URI != "sip:101@hello.test" {
		t.Fatalf("render data = %+v / %+v", d.Firmware, d)
	}
	if _, err := st.DeleteFirmware(ctx, "test", ids[1]); err == nil {
		t.Fatal("deleting a pinned firmware succeeded")
	}
	if f, err := st.FirmwareByName(ctx, prov.Yealink, "t54w.rom"); err != nil || f.ID != ids[1] {
		t.Fatalf("by name = %+v, %v", f, err)
	}
	old := time.Now().Add(-100 * 24 * time.Hour)
	if err := st.InsertFetches(ctx, []prov.FetchRecord{
		{At: old, PhoneID: c.Phone.ID, PathRedacted: "/p/****/a.cfg", Kind: prov.KindDevice, Result: prov.ResultServed, Status: 200},
		{At: time.Now(), PathRedacted: "/p/boot/y000000000000.boot", Kind: prov.KindBoot, Result: prov.ResultBootServed, Status: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PruneFetches(ctx, time.Now().Add(-90*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM prov_fetches`).Scan(&n)
	if n != 1 {
		t.Fatalf("fetches left = %d", n)
	}
}

// TestPhoneFetchStates fails if a phone that never fetched, one that
// fetched since the stale bound and one whose last fetch is older are not
// counted in their own state.
func TestPhoneFetchStates(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	now := time.Now()
	lastFetch := map[string]*time.Time{"805ec0000011": nil, "805ec0000012": new(now.Add(-time.Hour)), "805ec0000013": new(now.Add(-72 * time.Hour))}
	for mac, at := range lastFetch {
		c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: mac, Vendor: prov.Yealink, Model: "T54W",
			ExtensionID: ext, Enabled: true, Realm: "hello.test"})
		if err != nil {
			t.Fatal(err)
		}
		if at != nil {
			if _, err := db.ExecContext(ctx, `UPDATE phones SET last_fetch_at = $1 WHERE id = $2`, *at, c.Phone.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	never, fetched, stale, err := st.PhoneFetchStates(ctx, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if never != 1 || fetched != 1 || stale != 1 {
		t.Fatalf("states = never %d, fetched %d, stale %d; want 1 each", never, fetched, stale)
	}
}
