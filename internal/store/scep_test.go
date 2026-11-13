package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestSCEPStore walks the SCEP and 802.1X persistence against PostgreSQL.
// It fails if:
//   - the CA key or a challenge or profile password is stored in clear;
//   - a second CA replaces the first without replace;
//   - a phone of a group with SCEP off, or with HELLO_SCEP off, renders SCEP;
//   - two concurrent enrolments with the same challenge both get a
//     certificate, or a used challenge is accepted again;
//   - a retried transaction is signed twice;
//   - a renewal from a revoked certificate or another phone's is accepted;
//   - removing a phone leaves its certificates unrevoked;
//   - a profile a group uses can be deleted, or a wired profile renders on
//     a model without a wired port.
func TestSCEPStore(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	st.prov.SCEP = true

	if ca, err := st.GetSCEPCA(ctx); err != nil || ca != nil {
		t.Fatalf("a new store has a CA: %v %v", ca, err)
	}
	certPEM, key, err := prov.GenerateSCEPCA("", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := st.SetSCEPCA(ctx, "test", certPEM, key, "generated", false)
	if err != nil || ca.Source != "generated" || !strings.Contains(ca.Subject, "Hello device CA") {
		t.Fatalf("set CA = %+v, %v", ca, err)
	}
	var enc []byte
	if err := db.QueryRow(`SELECT key_enc FROM scep_ca WHERE id = $1`, ca.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	der, _ := prov.MarshalRSAKey(key)
	if bytes.Contains(enc, der[:64]) {
		t.Fatal("the CA key is stored in clear")
	}
	var used *InUseError
	if _, err := st.SetSCEPCA(ctx, "test", certPEM, key, "generated", false); !errors.As(err, &used) {
		t.Fatalf("a second CA without replace = %v", err)
	}
	active, err := st.SCEPActiveCA(ctx)
	if err != nil || active.ID != ca.ID || !active.Key.Equal(key) {
		t.Fatalf("active CA = %v", err)
	}

	// A group with SCEP on and a GRP2602 in it; a WP836 without a group.
	var gid int64
	if err := db.QueryRow(`INSERT INTO phone_groups (name) VALUES ('Lab') RETURNING id`).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	p1, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "c074ad000001", Vendor: prov.Grandstream, Model: "GRP2602",
		ExtensionID: ext, Enabled: true, Realm: "hello.test", GroupID: &gid})
	if err != nil {
		t.Fatal(err)
	}
	ext2, err := st.CreateExtension(ctx, "test", "102", "Two", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "c074ad000002", Vendor: prov.Grandstream, Model: "WP836",
		ExtensionID: ext2.ID, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := st.SCEPState(ctx, p1.Phone.ID); err != nil || s.Enabled || s.CA != nil {
		t.Fatalf("SCEP state while off = %+v, %v", s, err)
	}
	d, _, err := st.RenderInputs(ctx, p1.Phone.ID)
	if err != nil || d.SCEP != nil || d.Dot1X != nil {
		t.Fatalf("render while off = %+v %+v, %v", d.SCEP, d.Dot1X, err)
	}
	if _, err := st.SetGroupSecurity(ctx, "test", GroupSecurity{GroupID: gid, SCEPEnabled: true, CertValidityDays: 10}); err == nil {
		t.Fatal("a 10-day lifetime was accepted")
	}
	if _, err := st.SetGroupSecurity(ctx, "test", GroupSecurity{GroupID: gid + 99, SCEPEnabled: true, CertValidityDays: 90}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group = %v", err)
	}
	if _, err := st.SetGroupSecurity(ctx, "test", GroupSecurity{GroupID: gid, SCEPEnabled: true, CertValidityDays: 90}); err != nil {
		t.Fatal(err)
	}
	d, _, err = st.RenderInputs(ctx, p1.Phone.ID)
	if err != nil || d.SCEP == nil || !strings.HasSuffix(d.SCEP.URL, "/p/"+p1.Token+"/scep") || len(d.SCEP.Challenge) != 32 {
		t.Fatalf("render while on = %+v, %v", d.SCEP, err)
	}
	challenge := d.SCEP.Challenge
	if again, _, _ := st.RenderInputs(ctx, p1.Phone.ID); again.SCEP.Challenge != challenge {
		t.Fatal("the challenge changed between renders")
	}
	var chEnc []byte
	if err := db.QueryRow(`SELECT challenge_enc FROM scep_challenges WHERE phone_id = $1`, p1.Phone.ID).Scan(&chEnc); err != nil || bytes.Contains(chEnc, []byte(challenge)) {
		t.Fatalf("the challenge is stored in clear or missing: %v", err)
	}
	st.prov.SCEP = false
	if off, _, _ := st.RenderInputs(ctx, p1.Phone.ID); off.SCEP != nil {
		t.Fatal("HELLO_SCEP off still renders SCEP")
	}
	st.prov.SCEP = true
	if s, err := st.SCEPState(ctx, p1.Phone.ID); err != nil || !s.Enabled || s.CA == nil || s.ValidityDays != 90 {
		t.Fatalf("SCEP state while on = %+v, %v", s, err)
	}

	issue := func(phone CreatedPhone, challenge, renewal string, k *rsa.PrivateKey) (*x509.Certificate, error) {
		h, _ := prov.PublicKeySHA256(&k.PublicKey)
		return st.SCEPIssue(ctx, prov.SCEPIssue{PhoneID: phone.Phone.ID, MAC: phone.Phone.MAC, CAID: active.ID,
			Challenge: challenge, RenewalSerial: renewal, KeySHA256: h,
			Sign: func() (*x509.Certificate, error) {
				return prov.DeviceCertificate(active, &x509.CertificateRequest{PublicKey: &k.PublicKey}, phone.Phone.MAC, 90, "", time.Now())
			}})
	}
	keys := make([]*rsa.PrivateKey, 4)
	for i := range keys {
		if keys[i], err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := issue(p1, "wrong", "", keys[0]); !errors.Is(err, prov.ErrSCEPRefused) {
		t.Fatalf("wrong challenge = %v", err)
	}
	// Two enrolments race on one challenge: one certificate.
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []*x509.Certificate
	)
	for i := range 2 {
		wg.Go(func() {
			c, err := issue(p1, challenge, "", keys[i])
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins = append(wins, c)
			} else if !errors.Is(err, prov.ErrSCEPRefused) {
				t.Errorf("racing enrolment: %v", err)
			}
		})
	}
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("%d enrolments won one challenge", len(wins))
	}
	first := wins[0]
	winner := keys[0]
	if !first.PublicKey.(*rsa.PublicKey).Equal(&keys[0].PublicKey) {
		winner = keys[1]
	}
	if again, err := issue(p1, challenge, "", winner); err != nil || !again.Equal(first) {
		t.Fatalf("retried transaction = %v", err)
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM scep_certificates`); n != 1 {
		t.Fatalf("%d certificates after a retry", n)
	}
	if fresh, _, _ := st.RenderInputs(ctx, p1.Phone.ID); fresh.SCEP.Challenge == challenge {
		t.Fatal("the used challenge is still rendered")
	}
	renewed, err := issue(p1, "", prov.SerialHex(first.SerialNumber), keys[2])
	if err != nil || renewed.Equal(first) {
		t.Fatalf("renewal = %v", err)
	}
	if _, err := issue(p2, "", prov.SerialHex(renewed.SerialNumber), keys[3]); !errors.Is(err, prov.ErrSCEPRefused) {
		t.Fatalf("renewal with another phone's certificate = %v", err)
	}

	certs, err := st.ListSCEPCertificates(ctx, SCEPCertFilter{PhoneID: &p1.Phone.ID})
	if err != nil || len(certs) != 2 || certs[0].Kind != "renewal" || certs[0].Status != "active" || certs[0].Subject != "CN=c074ad000001,O=Kuvryn Hello" {
		t.Fatalf("certificates = %+v, %v", certs, err)
	}
	if _, err := st.RevokeSCEPCertificate(ctx, "test", certs[0].ID, "key_compromise"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RevokeSCEPCertificate(ctx, "test", certs[0].ID, "key_compromise"); !errors.As(err, &used) {
		t.Fatalf("revoking twice = %v", err)
	}
	if _, err := st.RevokeSCEPCertificate(ctx, "test", 99999, "unspecified"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking an unknown certificate = %v", err)
	}
	if _, err := issue(p1, "", prov.SerialHex(renewed.SerialNumber), keys[3]); !errors.Is(err, prov.ErrSCEPRefused) {
		t.Fatalf("renewal with a revoked certificate = %v", err)
	}
	revoked, err := st.SCEPRevoked(ctx, active.ID)
	if err != nil || len(revoked) != 1 || revoked[0].SerialNumber.Cmp(renewed.SerialNumber) != 0 {
		t.Fatalf("revoked = %v, %v", revoked, err)
	}

	// Past the retry window, a renewal that keeps its key is a new
	// certificate, not the old one sent back.
	if _, err := db.Exec(`UPDATE scep_certificates SET issued_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if same, err := issue(p1, "", prov.SerialHex(first.SerialNumber), winner); err != nil || same.Equal(first) {
		t.Fatalf("renewal keeping the key = %v", err)
	}

	// 802.1X profiles.
	pw := "radius-secret"
	prof, err := st.CreateDot1XProfile(ctx, "test", Dot1XInput{Name: "Office", Method: prov.Dot1XPEAP, Identity: "{mac}", Password: &pw})
	if err != nil || !prof.HasPassword {
		t.Fatalf("create profile = %+v, %v", prof, err)
	}
	var pwEnc []byte
	if err := db.QueryRow(`SELECT password_enc FROM dot1x_profiles WHERE id = $1`, prof.ID).Scan(&pwEnc); err != nil || bytes.Contains(pwEnc, []byte(pw)) {
		t.Fatalf("the profile password is stored in clear: %v", err)
	}
	if _, err := st.CreateDot1XProfile(ctx, "test", Dot1XInput{Name: "office", Method: prov.Dot1XEAPTLS, Identity: "{mac}"}); !errors.As(err, &used) {
		t.Fatalf("a duplicate name = %v", err)
	}
	var verr *ValidationError
	if _, err := st.CreateDot1XProfile(ctx, "test", Dot1XInput{Name: "NoPW", Method: prov.Dot1XPEAP, Identity: "{mac}"}); !errors.As(err, &verr) {
		t.Fatalf("PEAP without a password = %v", err)
	}
	kept, err := st.UpdateDot1XProfile(ctx, "test", prof.ID, Dot1XInput{Name: "Office", Method: prov.Dot1XPEAP, Identity: "ph-{mac}"})
	if err != nil || !kept.HasPassword || kept.Identity != "ph-{mac}" {
		t.Fatalf("update keeping the password = %+v, %v", kept, err)
	}
	if _, err := st.SetGroupSecurity(ctx, "test", GroupSecurity{GroupID: gid, SCEPEnabled: true, CertValidityDays: 90, WiredDot1XID: &prof.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDot1XProfile(ctx, "test", prof.ID); !errors.As(err, &used) {
		t.Fatalf("deleting a used profile = %v", err)
	}
	d, _, err = st.RenderInputs(ctx, p1.Phone.ID)
	if err != nil || d.Dot1X == nil || d.Dot1X.Wired == nil || d.Dot1X.Wired.Identity != "ph-c074ad000001" || d.Dot1X.Wired.Password != pw {
		t.Fatalf("render wired = %+v, %v", d.Dot1X, err)
	}
	d2, _, err := st.RenderInputs(ctx, p2.Phone.ID)
	if err != nil || d2.Dot1X == nil || d2.Dot1X.Wired != nil || len(d2.Dot1X.Profiles) != 1 {
		t.Fatalf("render on a WP without a group = %+v, %v", d2.Dot1X, err)
	}

	// Removing a phone revokes its live certificates.
	if err := st.DeletePhone(ctx, "test", p1.Phone.ID); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM scep_certificates WHERE revoked_at IS NULL`); n != 0 {
		t.Fatalf("%d certificates still live after the phone was removed", n)
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM scep_certificates WHERE revoke_reason = 'phone_removed' AND phone_id IS NULL`); n != 2 {
		t.Fatalf("%d certificates revoked for the removed phone", n)
	}

	// Replacing the CA keeps the old one's certificates.
	certPEM2, key2, err := prov.GenerateSCEPCA("Second", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := st.SetSCEPCA(ctx, "test", certPEM2, key2, "imported", true)
	if err != nil || ca2.ID == ca.ID || ca2.Source != "imported" {
		t.Fatalf("replace CA = %+v, %v", ca2, err)
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM scep_certificates`); n != 3 {
		t.Fatalf("%d certificates after replacing the CA", n)
	}
}

func scalarInt(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
