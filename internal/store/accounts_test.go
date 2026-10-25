package store

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/migrations"
	"github.com/pressly/goose/v3"
)

func newAccount(t *testing.T, s *Store, name string, role auth.Role, exts ...int64) User {
	t.Helper()
	u, err := s.CreateAccount(context.Background(), "test", NewAccount{
		Username: name, Email: name + "@example.com", Role: role, PasswordHash: "$argon2id$x",
		ExtensionIDs: exts, Voicemail: VoicemailEmailPrefs{Notify: true, Attach: true, After: "none"}})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return u
}

func addMessage(t *testing.T, s *Store, extID int64, caller string) (box, msg int64) {
	t.Helper()
	ctx := context.Background()
	b, err := s.UpdateVoicemailBox(ctx, "test", extID, VoicemailBoxChange{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `INSERT INTO voicemail_messages (box_id, minio_object, caller, duration_ms)
		VALUES ($1, $2, $3, 4000) RETURNING id`, b.ID, "box/"+strconv.FormatInt(b.ID, 10)+"/"+caller+".wav", caller).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	return b.ID, msg
}

// TestUserAccounts fails if a user cannot be created with several
// extensions, an extension can belong to two users, the primary extension
// column drifts from the first linked one, the last enabled admin can be
// demoted, disabled or deleted, a disabled user's session or token still
// resolves, or a password reset leaves old sessions alive.
func TestUserAccounts(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	e1, e2, e3 := mustExtension(t, s, "101"), mustExtension(t, s, "102"), mustExtension(t, s, "103")

	admin := newAccount(t, s, "root", auth.RoleAdmin)
	alice := newAccount(t, s, "alice", auth.RoleUser, e1.ID, e2.ID)
	if alice.ExtensionID == nil || *alice.ExtensionID != e1.ID || len(alice.ExtensionIDs) != 2 || alice.ExtensionIDs[1] != e2.ID {
		t.Fatalf("alice = %+v; want primary %d and extensions [%d %d]", alice, e1.ID, e1.ID, e2.ID)
	}
	if _, err := s.CreateAccount(ctx, "test", NewAccount{Username: "alice", Role: auth.RoleUser, PasswordHash: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate login = %v, want ErrConflict", err)
	}
	var inUse *InUseError
	if _, err := s.CreateAccount(ctx, "test", NewAccount{Username: "bob", Role: auth.RoleUser, PasswordHash: "x", ExtensionIDs: []int64{e2.ID}}); !errors.As(err, &inUse) ||
		!strings.Contains(err.Error(), `"alice"`) {
		t.Fatalf("taken extension = %v, want a conflict naming alice", err)
	}
	var verr *ValidationError
	if _, err := s.CreateAccount(ctx, "test", NewAccount{Username: "bob", Role: auth.RoleUser, PasswordHash: "x", ExtensionIDs: []int64{1 << 40}}); !errors.As(err, &verr) {
		t.Fatalf("unknown extension = %v, want a validation error", err)
	}
	if us, _ := s.ListUsers(ctx); len(us) != 2 {
		t.Fatalf("a failed create left a user behind: %d users", len(us))
	}

	// Replace the set: the primary follows the first.
	ids := []int64{e3.ID, e1.ID}
	u, err := s.UpdateAccount(ctx, "test", alice.ID, AccountChange{ExtensionIDs: &ids})
	if err != nil || u.ExtensionID == nil || *u.ExtensionID != e3.ID || len(u.ExtensionIDs) != 2 {
		t.Fatalf("replace = %+v, %v", u, err)
	}
	none := []int64{}
	if u, err = s.UpdateAccount(ctx, "test", alice.ID, AccountChange{ExtensionIDs: &none}); err != nil || u.ExtensionID != nil || len(u.ExtensionIDs) != 0 {
		t.Fatalf("unlink = %+v, %v", u, err)
	}
	if u, err = s.SetUserExtension(ctx, "test", alice.ID, &e2.ID); err != nil || *u.ExtensionID != e2.ID {
		t.Fatalf("legacy link = %+v, %v", u, err)
	}

	// Self-service reads go through the link.
	if es, err := s.UserExtensions(ctx, alice.ID); err != nil || len(es) != 1 || es[0].ID != e2.ID {
		t.Fatalf("alice's extensions = %v, %v", es, err)
	}
	if _, err := s.UserExtension(ctx, alice.ID, e1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a foreign extension = %v, want ErrNotFound", err)
	}
	if _, err := s.UserExtension(ctx, alice.ID, e2.ID); err != nil {
		t.Fatal(err)
	}

	// Sessions and tokens stop with the account.
	sess, tok := auth.HashToken("sess-alice"), auth.HashToken("tok-alice")
	if err := s.CreateSession(ctx, alice.ID, sess, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateToken(ctx, "test", alice.ID, NewToken{Name: "t", Scopes: auth.Scopes{auth.ScopeRead}}, tok); err != nil {
		t.Fatal(err)
	}
	if a, err := s.SessionActor(ctx, sess); err != nil || a.Role != auth.RoleUser || a.MustChangePassword {
		t.Fatalf("session actor = %+v, %v", a, err)
	}
	if _, err := s.UpdateAccount(ctx, "test", alice.ID, AccountChange{Enabled: ptrTo(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionActor(ctx, sess); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("disabled user's session = %v", err)
	}
	if _, err := s.TokenActor(ctx, tok); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("disabled user's token = %v", err)
	}
	if _, err := s.UserActor(ctx, alice.ID); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("disabled user's agent identity = %v", err)
	}
	if l, err := s.UserForLogin(ctx, "alice"); err != nil || l.Enabled {
		t.Fatalf("login row = %+v, %v; want a disabled user", l, err)
	}
	if _, err := s.UpdateAccount(ctx, "test", alice.ID, AccountChange{Enabled: ptrTo(true)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, alice.ID, sess, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenActor(ctx, tok); err != nil {
		t.Fatalf("re-enabled user's token = %v", err)
	}
	// A reset ends sessions and forces a change.
	u, err = s.UpdateAccount(ctx, "test", alice.ID, AccountChange{PasswordHash: ptrTo("$argon2id$new"), MustChange: true})
	if err != nil || !u.MustChangePassword {
		t.Fatalf("reset = %+v, %v", u, err)
	}
	if _, err := s.SessionActor(ctx, sess); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("session survived a password reset: %v", err)
	}
	if a, err := s.TokenActor(ctx, tok); err != nil || !a.MustChangePassword {
		t.Fatalf("token actor = %+v, %v; want the forced-change flag", a, err)
	}
	if err := s.CreateSession(ctx, alice.ID, sess, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	other := auth.HashToken("sess-other")
	if err := s.CreateSession(ctx, alice.ID, other, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeOwnPassword(ctx, alice.ID, "$argon2id$mine", sess); err != nil {
		t.Fatal(err)
	}
	if a, err := s.SessionActor(ctx, sess); err != nil || a.MustChangePassword {
		t.Fatalf("own session after a change = %+v, %v", a, err)
	}
	if _, err := s.SessionActor(ctx, other); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("another session survived the change: %v", err)
	}
	if h, _ := s.UserPasswordHash(ctx, alice.ID); h != "$argon2id$mine" {
		t.Fatalf("hash = %q", h)
	}
	if err := s.UpgradePasswordHash(ctx, alice.ID, "$wrong", "$argon2id$upgraded"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.UserPasswordHash(ctx, alice.ID); h != "$argon2id$mine" {
		t.Fatal("an upgrade replaced a hash it did not match")
	}

	// The last enabled admin stays.
	if _, err := s.UpdateAccount(ctx, "test", admin.ID, AccountChange{Enabled: ptrTo(false)}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disable the last admin = %v", err)
	}
	if _, err := s.UpdateAccount(ctx, "test", admin.ID, AccountChange{Role: ptrTo(auth.RoleViewer)}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote the last admin = %v", err)
	}
	if err := s.DeleteUser(ctx, "test", admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete the last admin = %v", err)
	}
	second := newAccount(t, s, "second", auth.RoleAdmin)
	if _, err := s.UpdateAccount(ctx, "test", second.ID, AccountChange{Enabled: ptrTo(false)}); err != nil {
		t.Fatalf("disable a second admin: %v", err)
	}
	// A disabled admin does not count: the last enabled one still stays.
	if err := s.DeleteUser(ctx, "test", admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete the only enabled admin = %v", err)
	}
	if err := s.DeleteUser(ctx, "test", alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "test", alice.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
	if _, err := s.SessionActor(ctx, sess); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal("a deleted user's session still resolves")
	}
	// The extension is free again.
	if _, err := s.CreateAccount(ctx, "test", NewAccount{Username: "bob", Role: auth.RoleUser, PasswordHash: "x", ExtensionIDs: []int64{e2.ID}}); err != nil {
		t.Fatalf("relink a freed extension: %v", err)
	}
	// Audit rows name the action and the user, never a hash.
	rows, err := s.db.QueryContext(ctx, `SELECT actor || ' ' || action || ' ' || resource || ' ' || resource_id FROM audit_events WHERE resource = 'user'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		if strings.Contains(a, "argon2") || strings.Contains(a, "$") {
			t.Errorf("audit row carries a hash: %q", a)
		}
		n++
	}
	if n < 8 {
		t.Errorf("only %d user audit rows", n)
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestSelfServiceSQL fails if a user can read another extension's voicemail
// message or call history through the ownership queries, including by
// guessing ids.
func TestSelfServiceSQL(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	e1, e2 := mustExtension(t, s, "101"), mustExtension(t, s, "102")
	alice := newAccount(t, s, "alice", auth.RoleUser, e1.ID)
	bob := newAccount(t, s, "bob", auth.RoleUser, e2.ID)
	nobody := newAccount(t, s, "nobody", auth.RoleUser)
	_, mA := addMessage(t, s, e1.ID, "+111")
	_, mB := addMessage(t, s, e2.ID, "+222")

	if m, err := s.UserVoicemailMessage(ctx, alice.ID, mA); err != nil || m.Caller != "+111" {
		t.Fatalf("own message = %+v, %v", m, err)
	}
	for _, c := range []struct{ user, msg int64 }{{alice.ID, mB}, {bob.ID, mA}, {nobody.ID, mA}, {nobody.ID, mB}, {alice.ID, 1 << 40}, {1 << 40, mA}} {
		if _, err := s.UserVoicemailMessage(ctx, c.user, c.msg); !errors.Is(err, ErrNotFound) {
			t.Errorf("user %d message %d = %v, want ErrNotFound", c.user, c.msg, err)
		}
	}
	// Guess every id around the real ones.
	for id := int64(1); id < mB+20; id++ {
		if m, err := s.UserVoicemailMessage(ctx, alice.ID, id); err == nil && id != mA {
			t.Fatalf("alice reached message %d (%+v)", id, m)
		}
	}
	// An unlinked user loses access at once.
	if _, err := s.UpdateAccount(ctx, "test", alice.ID, AccountChange{ExtensionIDs: &[]int64{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserVoicemailMessage(ctx, alice.ID, mA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after unlinking = %v", err)
	}

	// Call history.
	for i, c := range []struct{ src, dst string }{{"101", "+3299"}, {"102", "+3298"}, {"+3297", "101"}, {"103", "104"}} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, end_time,
			duration_ms, billable_ms, sip_node, final_status, termination_side)
			VALUES ($1, $2, $3, $4, now(), now(), 1000, 1000, 'n', 200, 'caller')`, "c"+strconv.Itoa(i), "s"+strconv.Itoa(i), c.src, c.dst); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateAccount(ctx, "test", alice.ID, AccountChange{ExtensionIDs: &[]int64{e1.ID}}); err != nil {
		t.Fatal(err)
	}
	cs, next, err := s.UserCDRs(ctx, alice.ID, 0, 10)
	if err != nil || len(cs) != 2 || next != "" {
		t.Fatalf("alice's calls = %d (%v) next %q, %v; want her two", len(cs), cs, next, err)
	}
	for _, c := range cs {
		if c.Source != "101" && c.Destination != "101" {
			t.Errorf("alice sees call %+v", c)
		}
	}
	if cs, _, _ := s.UserCDRs(ctx, nobody.ID, 0, 10); len(cs) != 0 {
		t.Errorf("a user without extensions sees %d calls", len(cs))
	}
	page, next, err := s.UserCDRs(ctx, alice.ID, 0, 1)
	if err != nil || len(page) != 1 || next == "" {
		t.Fatalf("page = %v next %q %v", page, next, err)
	}
	before, _ := strconv.ParseInt(next, 10, 64)
	if page, _, _ = s.UserCDRs(ctx, alice.ID, before, 1); len(page) != 1 || page[0].ID >= before {
		t.Fatalf("second page = %v", page)
	}
}

// TestUserAccountsMigration fails if a user linked before the migration
// loses the link or becomes anything but an enabled local user.
func TestUserAccountsMigration(t *testing.T) {
	ctx := context.Background()
	db := scratchDB(t)
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	var ext, uid int64
	if err := db.QueryRowContext(ctx, `INSERT INTO extensions (number, name) VALUES ('101', 'A') RETURNING id`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO users (username, password_hash, extension_id) VALUES ('old', '$2a$x', $1) RETURNING id`, ext).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := New(db).GetUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != auth.RoleAdmin || !u.Enabled || u.AuthMethod != "local" || u.MustChangePassword ||
		len(u.ExtensionIDs) != 1 || u.ExtensionIDs[0] != ext || u.ExtensionID == nil || !u.VoicemailEmail.Notify || !u.VoicemailEmail.Attach || u.VoicemailEmail.After != "none" {
		t.Fatalf("migrated user = %+v", u)
	}
	// Role user is now allowed, and nothing else new.
	if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('u', 'x', 'user')`); err != nil {
		t.Fatalf("role user rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('v', 'x', 'root')`); err == nil {
		t.Fatal("an unknown role was accepted")
	}
}

// TestSMTPSettingsStore fails if the SMTP password is stored readably,
// returned by the settings read, lost by a save that omits it, or not
// cleared when asked.
func TestSMTPSettingsStore(t *testing.T) {
	ctx := context.Background()
	box, err := secret.New("MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	if err != nil {
		t.Fatal(err)
	}
	s := scratchStore(t).WithSecretBox(box)
	v, err := s.GetSMTPSettings(ctx)
	if err != nil || v.Host != "" || v.PasswordSet || v.Port != 587 || v.Security != "starttls" {
		t.Fatalf("defaults = %+v, %v", v, err)
	}
	const pw = "hunter2-smtp"
	in := SMTPInput{Host: "smtp.example.com", Port: 465, Security: "tls", Username: "pbx", From: "pbx@example.com", Password: ptrTo(pw)}
	if v, err = s.PutSMTPSettings(ctx, "user:admin", in); err != nil || !v.PasswordSet || v.Host != "smtp.example.com" || v.Port != 465 {
		t.Fatalf("saved = %+v, %v", v, err)
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_sealed FROM smtp_settings`).Scan(&raw); err != nil || bytes.Contains(raw, []byte(pw)) || len(raw) == 0 {
		t.Fatalf("stored password is readable or missing: %q %v", raw, err)
	}
	send, err := s.SMTPSendSettings(ctx)
	if err != nil || send.Password != pw || send.Username != "pbx" {
		t.Fatalf("send settings = %+v, %v", send, err)
	}
	in.Password, in.Port = nil, 587
	if _, err = s.PutSMTPSettings(ctx, "user:admin", in); err != nil {
		t.Fatal(err)
	}
	if send, _ = s.SMTPSendSettings(ctx); send.Password != pw || send.Port != 587 {
		t.Fatalf("a save without a password lost it: %+v", send)
	}
	in.Password = ptrTo("")
	if v, err = s.PutSMTPSettings(ctx, "user:admin", in); err != nil || v.PasswordSet {
		t.Fatalf("clear = %+v, %v", v, err)
	}
	var audit string
	if err := s.db.QueryRowContext(ctx, `SELECT string_agg(actor || action || resource, ',') FROM audit_events WHERE resource = 'smtp_settings'`).Scan(&audit); err != nil || strings.Contains(audit, pw) || audit == "" {
		t.Fatalf("audit = %q, %v", audit, err)
	}
}

// emailFixture is two extensions' worth of users, a box and messages.
func emailFixture(t *testing.T) (s *Store, ext Extension, alice, bob User, msg int64) {
	t.Helper()
	s = scratchStore(t)
	ext = mustExtension(t, s, "101")
	alice = newAccount(t, s, "alice", auth.RoleUser, ext.ID)
	_, msg = addMessage(t, s, ext.ID, "+3211")
	return s, ext, alice, User{}, msg
}

// TestEmailQueue fails if a new voicemail does not become one notification
// per recipient with that recipient's options, a disabled, email-less or
// opted-out user is notified, the box's own address is dropped or doubled,
// two nodes can claim the same notification, a failed send is not retried
// later, or the after-send actions are wrong.
func TestEmailQueue(t *testing.T) {
	ctx := context.Background()

	t.Run("fan out", func(t *testing.T) {
		s, ext, alice, _, msg := emailFixture(t)
		// bob: opted out; carol: no email; dave: disabled; erin: shares alice's address with the box.
		e2 := mustExtension(t, s, "102")
		bob := newAccount(t, s, "bob", auth.RoleUser, e2.ID)
		if _, err := s.UpdateAccount(ctx, "t", bob.ID, AccountChange{Voicemail: &VoicemailEmailPrefs{Notify: false, Attach: true, After: "none"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateAccount(ctx, "t", alice.ID, AccountChange{Voicemail: &VoicemailEmailPrefs{Notify: true, Attach: false, After: "mark_read"}}); err != nil {
			t.Fatal(err)
		}
		box := "alice@example.com" // the box address equals the user's: one mail, the user's options
		if _, err := s.UpdateVoicemailBox(ctx, "t", ext.ID, VoicemailBoxChange{Email: &box}, nil); err != nil {
			t.Fatal(err)
		}
		_, mBob := addMessage(t, s, e2.ID, "+3222")
		if n, err := s.FanOutEmails(ctx, 10); err != nil || n != 2 {
			t.Fatalf("fan out = %d, %v", n, err)
		}
		if n, _ := s.FanOutEmails(ctx, 10); n != 0 {
			t.Fatalf("a second fan out found %d messages", n)
		}
		var to string
		var attach bool
		var after string
		if err := s.db.QueryRowContext(ctx, `SELECT to_email, attach, after_action FROM voicemail_notifications WHERE message_id = $1`, msg).Scan(&to, &attach, &after); err != nil {
			t.Fatal(err)
		}
		if to != "alice@example.com" || attach || after != "mark_read" {
			t.Fatalf("notification = %q attach %v after %q; want alice's own options", to, attach, after)
		}
		var st string
		_ = s.db.QueryRowContext(ctx, `SELECT email_status FROM voicemail_messages WHERE id = $1`, msg).Scan(&st)
		if st != "queued" {
			t.Errorf("status = %q", st)
		}
		_ = s.db.QueryRowContext(ctx, `SELECT email_status FROM voicemail_messages WHERE id = $1`, mBob).Scan(&st)
		if st != "skipped" {
			t.Errorf("opted-out recipient: status = %q, want skipped", st)
		}
		var n int
		_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM voicemail_notifications`).Scan(&n)
		if n != 1 {
			t.Fatalf("%d notifications, want 1", n)
		}
	})

	t.Run("the box address is kept next to a user", func(t *testing.T) {
		s, ext, _, _, msg := emailFixture(t)
		box := "frontdesk@example.com"
		if _, err := s.UpdateVoicemailBox(ctx, "t", ext.ID, VoicemailBoxChange{Email: &box}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FanOutEmails(ctx, 10); err != nil {
			t.Fatal(err)
		}
		rows, _ := s.db.QueryContext(ctx, `SELECT to_email FROM voicemail_notifications WHERE message_id = $1 ORDER BY to_email`, msg)
		defer func() { _ = rows.Close() }()
		var got []string
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			got = append(got, a)
		}
		if strings.Join(got, ",") != "alice@example.com,frontdesk@example.com" {
			t.Fatalf("recipients = %v", got)
		}
	})

	t.Run("disabled and email-less users are not notified", func(t *testing.T) {
		s, _, alice, _, _ := emailFixture(t)
		if _, err := s.UpdateAccount(ctx, "t", alice.ID, AccountChange{Enabled: ptrTo(false)}); err != nil {
			// the only admin check does not apply to a user
			t.Fatal(err)
		}
		if _, err := s.FanOutEmails(ctx, 10); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM voicemail_notifications`).Scan(&n)
		if n != 0 {
			t.Fatalf("disabled user notified (%d)", n)
		}
	})

	t.Run("claims are exclusive across nodes", func(t *testing.T) {
		s, ext, _, _, _ := emailFixture(t)
		for range 30 {
			addMessage(t, s, ext.ID, "+3299")
		}
		for {
			n, err := s.FanOutEmails(ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
		}
		var (
			mu   sync.Mutex
			seen = map[int64]int{}
			wg   sync.WaitGroup
		)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					js, err := s.ClaimEmails(ctx, 4, time.Minute)
					if err != nil {
						t.Error(err)
						return
					}
					if len(js) == 0 {
						return
					}
					mu.Lock()
					for _, j := range js {
						seen[j.ID]++
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(seen) != 31 {
			t.Fatalf("claimed %d notifications, want 31", len(seen))
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("notification %d claimed %d times", id, n)
			}
		}
	})

	t.Run("retry, give up, stale reclaim", func(t *testing.T) {
		s, _, _, _, msg := emailFixture(t)
		if _, err := s.FanOutEmails(ctx, 10); err != nil {
			t.Fatal(err)
		}
		js, err := s.ClaimEmails(ctx, 10, time.Minute)
		if err != nil || len(js) != 1 || js[0].Attempts != 1 || js[0].To != "alice@example.com" || js[0].MessageID != msg || !js[0].Attach || js[0].Caller != "+3211" {
			t.Fatalf("claim = %+v, %v", js, err)
		}
		id := js[0].ID
		if again, _ := s.ClaimEmails(ctx, 10, time.Minute); len(again) != 0 {
			t.Fatalf("a claimed notification was claimed again: %+v", again)
		}
		// A dead sender: the claim goes stale and another node takes over.
		time.Sleep(1100 * time.Millisecond)
		if again, _ := s.ClaimEmails(ctx, 10, time.Second); len(again) != 1 || again[0].Attempts != 2 {
			t.Fatalf("stale claim not reclaimed: %+v", again)
		}
		if _, err := s.FinishEmail(ctx, id, EmailResult{Err: "451 try later", RetryAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if again, _ := s.ClaimEmails(ctx, 10, time.Minute); len(again) != 0 {
			t.Fatalf("a notification was claimed before its retry time: %+v", again)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE voicemail_notifications SET next_attempt_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if again, _ := s.ClaimEmails(ctx, 10, time.Minute); len(again) != 1 || again[0].Attempts != 3 {
			t.Fatalf("retry not claimed: %+v", again)
		}
		if _, err := s.FinishEmail(ctx, id, EmailResult{Err: "550 no", Final: true}); err != nil {
			t.Fatal(err)
		}
		var st, note string
		_ = s.db.QueryRowContext(ctx, `SELECT email_status FROM voicemail_messages WHERE id = $1`, msg).Scan(&st)
		_ = s.db.QueryRowContext(ctx, `SELECT status FROM voicemail_notifications WHERE id = $1`, id).Scan(&note)
		if st != "failed" || note != "failed" {
			t.Fatalf("message %q notification %q, want failed", st, note)
		}
		if again, _ := s.ClaimEmails(ctx, 10, time.Nanosecond); len(again) != 0 {
			t.Fatalf("a failed notification was claimed: %+v", again)
		}
	})

	t.Run("after-send actions", func(t *testing.T) {
		for _, c := range []struct {
			after      string
			wantHeard  bool
			wantDelete bool
		}{{"none", false, false}, {"mark_read", true, false}, {"delete", false, true}} {
			s, _, alice, _, msg := emailFixture(t)
			if _, err := s.UpdateAccount(ctx, "t", alice.ID, AccountChange{Voicemail: &VoicemailEmailPrefs{Notify: true, Attach: true, After: c.after}}); err != nil {
				t.Fatal(err)
			}
			_, _ = s.FanOutEmails(ctx, 10)
			js, _ := s.ClaimEmails(ctx, 10, time.Minute)
			if len(js) != 1 || js[0].After != c.after {
				t.Fatalf("%s: claim = %+v", c.after, js)
			}
			remove, err := s.FinishEmail(ctx, js[0].ID, EmailResult{})
			if err != nil {
				t.Fatal(err)
			}
			var heard bool
			var st string
			err = s.db.QueryRowContext(ctx, `SELECT heard, email_status FROM voicemail_messages WHERE id = $1`, msg).Scan(&heard, &st)
			switch {
			case c.wantDelete:
				if err == nil || remove == "" {
					t.Errorf("delete: the message remains (err %v) or no object to remove (%q)", err, remove)
				}
			case err != nil || heard != c.wantHeard || st != "sent" || remove != "":
				t.Errorf("%s: heard %v status %q remove %q err %v", c.after, heard, st, remove, err)
			}
		}
	})

	t.Run("a message waits for its other recipients; delete wins", func(t *testing.T) {
		s, ext, alice, _, msg := emailFixture(t)
		box := "frontdesk@example.com"
		_, _ = s.UpdateVoicemailBox(ctx, "t", ext.ID, VoicemailBoxChange{Email: &box}, nil)
		_, _ = s.UpdateAccount(ctx, "t", alice.ID, AccountChange{Voicemail: &VoicemailEmailPrefs{Notify: true, Attach: true, After: "delete"}})
		_, _ = s.FanOutEmails(ctx, 10)
		js, _ := s.ClaimEmails(ctx, 10, time.Minute)
		if len(js) != 2 {
			t.Fatalf("claim = %+v", js)
		}
		first, second := js[0], js[1]
		if remove, err := s.FinishEmail(ctx, first.ID, EmailResult{}); err != nil || remove != "" {
			t.Fatalf("first finish removed %q (%v) while another recipient was still pending", remove, err)
		}
		if _, err := s.GetVoicemailMessage(ctx, msg); err != nil {
			t.Fatalf("message gone before every recipient was mailed: %v", err)
		}
		if remove, err := s.FinishEmail(ctx, second.ID, EmailResult{}); err != nil || remove == "" {
			t.Fatalf("last finish: remove %q, %v; want the delete to apply", remove, err)
		}
		if _, err := s.GetVoicemailMessage(ctx, msg); !errors.Is(err, ErrNotFound) {
			t.Fatalf("message = %v, want deleted", err)
		}
	})
}
