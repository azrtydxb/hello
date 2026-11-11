package store

import (
	"context"
	"testing"
)

// TestSRTPPolicyStorage fails if a device or trunk does not carry its SRTP
// policy through the database (migration 00072): devices start optional,
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
	d, err = s.UpdateDevice(ctx, "test", d.ID, nil, nil, &req)
	if err != nil || d.SRTP != "required" {
		t.Fatalf("UpdateDevice srtp = %q, %v", d.SRTP, err)
	}
	off := false
	if d, err = s.UpdateDevice(ctx, "test", d.ID, &off, nil, nil); err != nil || d.SRTP != "required" {
		t.Fatalf("an update without srtp changed it to %q (%v)", d.SRTP, err)
	}
	bad := "sometimes"
	if _, err := s.UpdateDevice(ctx, "test", d.ID, nil, nil, &bad); err == nil {
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
