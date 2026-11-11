package store

import (
	"context"
	"testing"
)

// TestSRTPPolicyStorage fails if a device or trunk does not carry its SRTP
// policy through the database (migration 00077): devices start optional,
// trunks off, PATCH replaces only what is sent, and an unknown value is
// refused by the column's CHECK. Needs HELLO_TEST_DATABASE_URL.
func TestSRTPPolicyStorage(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	ext, err := s.CreateExtension(ctx, "test", "301", "SRTP", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDevice(ctx, "test", NewDevice{ExtensionID: ext.ID, SIPUsername: "srtp-desk", Enabled: true, Realm: "pbx.test", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if d.SRTP != "optional" {
		t.Fatalf("a new device's srtp = %q, want optional", d.SRTP)
	}
	req := "required"
	d, err = s.UpdateDevice(ctx, "test", d.ID, nil, nil, DeviceMedia{SRTP: &req})
	if err != nil || d.SRTP != "required" {
		t.Fatalf("UpdateDevice srtp = %q, %v", d.SRTP, err)
	}
	off := false
	if d, err = s.UpdateDevice(ctx, "test", d.ID, &off, nil, DeviceMedia{}); err != nil || d.SRTP != "required" {
		t.Fatalf("an update without srtp changed it to %q (%v)", d.SRTP, err)
	}
	bad := "sometimes"
	if _, err := s.UpdateDevice(ctx, "test", d.ID, nil, nil, DeviceMedia{SRTP: &bad}); err == nil {
		t.Fatal("an unknown srtp value was stored")
	}

	var id int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO trunks (name, mode) VALUES ('srtp-trunk', 'ip') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tr, err := s.GetTrunk(ctx, id)
	if err != nil || tr.SRTP != "off" {
		t.Fatalf("a new trunk's srtp = %q, %v; want off", tr.SRTP, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE trunks SET srtp = 'required' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if tr, err = s.GetTrunk(ctx, id); err != nil || tr.SRTP != "required" || tr.Input().SRTP != "required" {
		t.Fatalf("trunk srtp = %q, %v; want required", tr.SRTP, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE trunks SET srtp = 'maybe' WHERE id = $1`, id); err == nil {
		t.Fatal("an unknown trunk srtp value was stored")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE cdrs SET srtp = 'maybe'`); err != nil {
		t.Fatalf("an empty cdrs table cannot violate the check: %v", err)
	}
}

// TestDeviceKeyExchangeStorage fails if a device does not carry its key
// exchange and ICE setting through the database (migration 00078): new devices
// are sdes with ICE off, an update replaces only what is sent, an unknown
// exchange is refused by the column's CHECK, and a CDR carries its key
// exchange and ICE flag. Needs HELLO_TEST_DATABASE_URL.
func TestDeviceKeyExchangeStorage(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	ext, err := s.CreateExtension(ctx, "test", "302", "DTLS", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDevice(ctx, "test", NewDevice{ExtensionID: ext.ID, SIPUsername: "dtls-desk", Enabled: true, Realm: "pbx.test", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if d.SRTPKeyEx != "sdes" || d.MediaICE {
		t.Fatalf("a new device has key exchange %q, ice %v; want sdes and off", d.SRTPKeyEx, d.MediaICE)
	}
	kex, ice := "dtls", true
	if d, err = s.UpdateDevice(ctx, "test", d.ID, nil, nil, DeviceMedia{KeyEx: &kex, ICE: &ice}); err != nil || d.SRTPKeyEx != "dtls" || !d.MediaICE || d.SRTP != "optional" {
		t.Fatalf("UpdateDevice = %q, %v, srtp %q, %v", d.SRTPKeyEx, d.MediaICE, d.SRTP, err)
	}
	off := false
	if d, err = s.UpdateDevice(ctx, "test", d.ID, &off, nil, DeviceMedia{}); err != nil || d.SRTPKeyEx != "dtls" || !d.MediaICE {
		t.Fatalf("an update without the fields changed them: %q, %v, %v", d.SRTPKeyEx, d.MediaICE, err)
	}
	bad := "zrtp"
	if _, err := s.UpdateDevice(ctx, "test", d.ID, nil, nil, DeviceMedia{KeyEx: &bad}); err == nil {
		t.Fatal("an unknown key exchange was stored")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE cdrs SET srtp_keyex = 'zrtp'`); err != nil {
		t.Fatalf("an empty cdrs table cannot violate the check: %v", err)
	}
}
