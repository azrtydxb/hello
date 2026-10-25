package store

// The store paths hello-sip calls: box lookup by extension number, password
// check, message insert and heard flag, feature-code updates. They need the
// real schema, so they skip unless HELLO_TEST_DATABASE_URL is set, and each
// test runs against its own migrated throwaway database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// scratchStore returns a Store over a migrated throwaway database.
func scratchStore(t *testing.T) *Store {
	t.Helper()
	db := scratchDB(t)
	if _, err := migrate.Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

// scratchDB returns a throwaway, unmigrated database.
func scratchDB(t *testing.T) *sql.DB {
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
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*scfg)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExtension(t *testing.T, s *Store, number string) Extension {
	t.Helper()
	e, err := s.CreateExtension(context.Background(), "test", number, "Test "+number, "", nil)
	if err != nil {
		t.Fatalf("create extension %s: %v", number, err)
	}
	return e
}

func TestVoicemailBoxByNumber(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	e := mustExtension(t, s, "101")
	email := "sales@hello.lab"
	if _, err := s.UpdateVoicemailBox(ctx, "test", e.ID, VoicemailBoxChange{Email: &email}, nil); err != nil {
		t.Fatalf("update voicemail box: %v", err)
	}
	b, ok, err := s.VoicemailBoxByNumber(ctx, "101")
	if err != nil || !ok {
		t.Fatalf("box by number: ok=%v err=%v", ok, err)
	}
	if b.ID == 0 || b.Email != email {
		t.Fatalf("box = %+v, want id and email %q", b, email)
	}
	if _, ok, err := s.VoicemailBoxByNumber(ctx, "199"); ok || err != nil {
		t.Fatalf("unknown number: ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestVerifyVoicemailPassword(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	e := mustExtension(t, s, "101")

	// A box without a password accepts none.
	if _, err := s.UpdateVoicemailBox(ctx, "test", e.ID, VoicemailBoxChange{}, nil); err != nil {
		t.Fatalf("create box: %v", err)
	}
	b, ok, err := s.VoicemailBoxByNumber(ctx, "101")
	if err != nil || !ok {
		t.Fatalf("box by number: ok=%v err=%v", ok, err)
	}
	if ok, err := s.VerifyVoicemailPassword(ctx, b.ID, "1234"); ok || err != nil {
		t.Fatalf("password on passwordless box: ok=%v err=%v, want false", ok, err)
	}

	secret := "1234"
	if _, err := s.UpdateVoicemailBox(ctx, "test", e.ID, VoicemailBoxChange{Password: &secret}, nil); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if ok, err := s.VerifyVoicemailPassword(ctx, b.ID, "1234"); !ok || err != nil {
		t.Fatalf("correct password: ok=%v err=%v, want true", ok, err)
	}
	if ok, err := s.VerifyVoicemailPassword(ctx, b.ID, "9999"); ok || err != nil {
		t.Fatalf("wrong password: ok=%v err=%v, want false", ok, err)
	}
	if ok, err := s.VerifyVoicemailPassword(ctx, b.ID+1000, "1234"); ok || err != nil {
		t.Fatalf("unknown box: ok=%v err=%v, want false", ok, err)
	}
}

func TestInsertVoicemailMessage(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	e := mustExtension(t, s, "101")
	if _, err := s.UpdateVoicemailBox(ctx, "test", e.ID, VoicemailBoxChange{}, nil); err != nil {
		t.Fatalf("create box: %v", err)
	}
	box, ok, err := s.VoicemailBoxByNumber(ctx, "101")
	if err != nil || !ok {
		t.Fatalf("box by number: ok=%v err=%v", ok, err)
	}

	first, err := s.InsertVoicemailMessage(ctx, box.ID, "box/1/1-a.wav", "102", "Jan", 8000)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	second, err := s.InsertVoicemailMessage(ctx, box.ID, "box/1/2-b.wav", "102", "", 9000)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	unread, total, err := s.VoicemailCounts(ctx, box.ID)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if unread != 2 || total != 2 {
		t.Fatalf("counts = %d/%d, want 2/2", unread, total)
	}
	if err := s.SetVoicemailMessageHeard(ctx, first, true); err != nil {
		t.Fatalf("mark heard: %v", err)
	}
	unread, total, err = s.VoicemailCounts(ctx, box.ID)
	if err != nil {
		t.Fatalf("counts after heard: %v", err)
	}
	if unread != 1 || total != 2 {
		t.Fatalf("counts after heard = %d/%d, want 1/2", unread, total)
	}

	// Newest first, and the unheard filter follows the heard flag.
	msgs, err := s.ListVoicemailMessages(ctx, box.ID, false)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].ID != second || msgs[0].Object != "box/1/2-b.wav" {
		t.Fatalf("list = %+v, want newest first with object key", msgs)
	}
	fresh, err := s.ListVoicemailMessages(ctx, box.ID, true)
	if err != nil {
		t.Fatalf("list unheard: %v", err)
	}
	if len(fresh) != 1 || fresh[0].ID != second {
		t.Fatalf("unheard list = %+v, want only %d", fresh, second)
	}
}

func TestApplyFeatureUpdate(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	e := mustExtension(t, s, "101")
	revBefore, err := s.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dnd := true
	target := "599"
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{
		Number: e.Number, DND: &dnd, ForwardAlways: &target,
	}); err != nil {
		t.Fatalf("apply feature update: %v", err)
	}
	got, err := s.GetExtension(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.DND || got.ForwardAlways != target {
		t.Fatalf("extension after update = dnd=%v always=%q, want dnd=true always=%q", got.DND, got.ForwardAlways, target)
	}
	revAfter, err := s.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revAfter != revBefore+1 {
		t.Fatalf("revision = %d, want %d", revAfter, revBefore+1)
	}

	// A nil field leaves its column alone.
	other := "600"
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: e.Number, ForwardBusy: &other}); err != nil {
		t.Fatalf("apply second update: %v", err)
	}
	got, err = s.GetExtension(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.DND || got.ForwardAlways != target || got.ForwardBusy != other {
		t.Fatalf("extension after second update = %+v", got)
	}

	// Call waiting defaults on; a feature-code change flips it and a nil
	// field leaves it alone (spec S-18).
	if !got.CallWaiting {
		t.Fatal("a new extension must have call waiting on")
	}
	off := false
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: e.Number, CallWaiting: &off}); err != nil {
		t.Fatalf("call waiting off: %v", err)
	}
	if got, err = s.GetExtension(ctx, e.ID); err != nil || got.CallWaiting || !got.DND {
		t.Fatalf("after call waiting off: %+v, %v (DND must stay)", got, err)
	}
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: e.Number, DND: &dnd}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetExtension(ctx, e.ID); err != nil || got.CallWaiting {
		t.Fatalf("an update without the field changed call waiting: %+v, %v", got, err)
	}
	on := true
	if _, err := s.UpdateExtension(ctx, "test", e.ID, ExtensionChange{CallWaiting: &on}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetExtension(ctx, e.ID); err != nil || !got.CallWaiting {
		t.Fatalf("after the API update: %+v, %v", got, err)
	}

	// An unknown extension number is a not-found, like every other store path.
	if err := s.ApplyFeatureUpdate(ctx, "hello-sip", FeatureUpdate{Number: "999"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown number: err=%v, want ErrNotFound", err)
	}
}

// TestVoicemailCallPlaneWrites covers what the *97 menus write from the phone:
// recorded greetings per slot, the revert to the spoken defaults, the PIN, a
// deleted message, and the caller-ID name kept with a message.
func TestVoicemailCallPlaneWrites(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	e := mustExtension(t, s, "101")
	if _, err := s.UpdateVoicemailBox(ctx, "test", e.ID, VoicemailBoxChange{}, nil); err != nil {
		t.Fatalf("create box: %v", err)
	}
	box, ok, err := s.VoicemailBoxByNumber(ctx, "101")
	if err != nil || !ok {
		t.Fatalf("box by number: ok=%v err=%v", ok, err)
	}
	if box.GreetingObject != "" || box.BusyObject != "" || box.NameObject != "" {
		t.Fatalf("fresh box = %+v", box)
	}

	// Each slot points at its own object; the replaced one is returned.
	for slot, obj := range map[string]string{"greeting": "box/1/g.wav", "busy": "box/1/b.wav", "unreachable": "box/1/u.wav", "name": "box/1/n.wav"} {
		prev, err := s.SetVoicemailGreeting(ctx, box.ID, slot, obj)
		if err != nil || prev != "" {
			t.Fatalf("set %s: prev=%q err=%v", slot, prev, err)
		}
	}
	prev, err := s.SetVoicemailGreeting(ctx, box.ID, "busy", "box/1/b2.wav")
	if err != nil || prev != "box/1/b.wav" {
		t.Fatalf("replace busy: prev=%q err=%v", prev, err)
	}
	box, _, _ = s.VoicemailBoxByNumber(ctx, "101")
	if box.GreetingObject != "box/1/g.wav" || box.BusyObject != "box/1/b2.wav" ||
		box.UnreachableObject != "box/1/u.wav" || box.NameObject != "box/1/n.wav" {
		t.Fatalf("box = %+v", box)
	}
	if _, err := s.SetVoicemailGreeting(ctx, box.ID, "nonsense", "x"); err == nil {
		t.Fatal("an unknown slot was accepted")
	}
	if _, err := s.SetVoicemailGreeting(ctx, 999999, "busy", "x"); err == nil {
		t.Fatal("an unknown box was accepted")
	}

	// Reverting drops greeting, busy and unreachable but keeps the name.
	dropped, err := s.ResetVoicemailGreetings(ctx, box.ID)
	if err != nil || len(dropped) != 3 {
		t.Fatalf("reset: dropped=%v err=%v", dropped, err)
	}
	box, _, _ = s.VoicemailBoxByNumber(ctx, "101")
	if box.GreetingObject != "" || box.BusyObject != "" || box.UnreachableObject != "" || box.NameObject != "box/1/n.wav" {
		t.Fatalf("after reset = %+v", box)
	}

	// The PIN.
	if err := s.SetVoicemailPIN(ctx, box.ID, "4321"); err != nil {
		t.Fatalf("set PIN: %v", err)
	}
	if ok, err := s.VerifyVoicemailPassword(ctx, box.ID, "4321"); err != nil || !ok {
		t.Fatalf("new PIN rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.VerifyVoicemailPassword(ctx, box.ID, "1234"); ok {
		t.Fatal("old PIN still accepted")
	}
	if err := s.SetVoicemailPIN(ctx, 999999, "4321"); err == nil {
		t.Fatal("PIN set on an unknown box")
	}

	// Messages keep the caller-ID name; delete is scoped to the box.
	id, err := s.InsertVoicemailMessage(ctx, box.ID, "box/1/m.wav", "0471234567", "Jan Peeters", 5000)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListVoicemailMessages(ctx, box.ID, false)
	if err != nil || len(msgs) != 1 || msgs[0].CallerName != "Jan Peeters" {
		t.Fatalf("messages = %+v err=%v", msgs, err)
	}
	if _, err := s.RemoveVoicemailMessage(ctx, box.ID+1, id); err == nil {
		t.Fatal("a message was deleted through another box")
	}
	obj, err := s.RemoveVoicemailMessage(ctx, box.ID, id)
	if err != nil || obj != "box/1/m.wav" {
		t.Fatalf("remove: obj=%q err=%v", obj, err)
	}
	if _, total, _ := s.VoicemailCounts(ctx, box.ID); total != 0 {
		t.Fatalf("total = %d after delete", total)
	}
}
