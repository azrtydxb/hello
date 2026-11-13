package snapshot

import (
	"context"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/jackc/pgx/v5"
)

// TestDeviceByUsernameDerived fails if a web device is not found by the
// expiring name it registers with, if a desk device is found by a name that
// merely looks derived, or if the base name of a derived device is reachable
// as a different device.
func TestDeviceByUsernameDerived(t *testing.T) {
	web := Device{ID: 12, Username: "w12", Realm: domain, Extension: "100", Kind: auth.KindWeb, AuthMode: AuthDerived}
	desk := Device{ID: 1, Username: "desk", Realm: domain, Extension: "100", HA1MD5: "x", HA1SHA256: "y"}
	lookalike := Device{ID: 2, Username: "w99", Realm: domain, Extension: "101", HA1MD5: "x", HA1SHA256: "y"}
	s := New(1, domain, []Device{web, desk, lookalike})
	for name, c := range map[string]struct {
		user string
		want int64
	}{
		"expiring name": {"w12.1893456000", 12}, "base name": {"w12", 12}, "desk": {"desk", 1},
		"desk lookalike exact": {"w99", 2}, "desk lookalike with expiry": {"w99.1893456000", 0},
		"unknown device": {"w13.1893456000", 0}, "not derived form": {"w12.x", 0}, "empty": {"", 0},
	} {
		d, ok := s.DeviceByUsername(c.user)
		if (c.want != 0) != ok || d.ID != c.want {
			t.Errorf("%s: DeviceByUsername(%q) = %d, %v; want %d", name, c.user, d.ID, ok, c.want)
		}
	}
}

// TestWebDeviceLoad fails if the snapshot query cannot read a derived device
// (NULL HA1 columns), loses its kind, owner or auth mode, ignores the
// owner's ring_web preference, or lists a revoked device.
func TestWebDeviceLoad(t *testing.T) {
	cfg := scratch(t)
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users (username, password_hash, role, ring_web) VALUES ('quiet', 'h', 'user', FALSE), ('loud', 'h', 'user', TRUE)`)
	exec(`INSERT INTO extensions (number, name) VALUES ('100', 'A'), ('101', 'B')`)
	exec(`INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256) SELECT id, 'desk100', $1::text, 'aa', 'bb' FROM extensions WHERE number = '100'`, domain)
	for _, w := range []struct {
		name, user, ext string
		enabled         bool
	}{{"w1", "quiet", "100", true}, {"w2", "loud", "101", true}, {"w3", "loud", "101", false}} {
		exec(`INSERT INTO devices (extension_id, sip_username, realm, enabled, kind, user_id, label, auth_mode)
			SELECT e.id, $1::text, $2::text, $5::boolean, 'web', u.id, 'browser', 'derived' FROM extensions e, users u WHERE e.number = $3::text AND u.username = $4::text`,
			w.name, domain, w.ext, w.user, w.enabled)
	}
	s, err := (&Watcher{Domain: domain}).loadAll(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.DeviceByUsername("w1.1893456000")
	if !ok || d.Username != "w1" || d.Kind != auth.KindWeb || d.AuthMode != AuthDerived || d.UserID == 0 || d.HA1MD5 != "" || d.Extension != "100" {
		t.Fatalf("w1 = %+v ok=%v", d, ok)
	}
	if !d.RingOff {
		t.Fatal("ring_web=false did not set RingOff")
	}
	if loud, ok := s.DeviceByUsername("w2.1893456000"); !ok || loud.RingOff {
		t.Fatalf("w2 = %+v ok=%v, want it ringing", loud, ok)
	}
	if _, ok := s.DeviceByUsername("w3.1893456000"); ok {
		t.Fatal("a revoked (disabled) device is in the snapshot")
	}
	desk, ok := s.DeviceByUsername("desk100")
	if !ok || desk.Kind != "desk" || desk.AuthMode != "digest" || desk.RingOff || desk.HA1MD5 != "aa" || desk.UserID != 0 {
		t.Fatalf("desk = %+v ok=%v", desk, ok)
	}
	if n := len(s.DevicesForExtension("100")); n != 2 {
		t.Fatalf("extension 100 devices = %d, want the desk phone and the web client", n)
	}
}
