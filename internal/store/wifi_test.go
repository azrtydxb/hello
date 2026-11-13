package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestWifiRenderInputs fails if a PSK is stored unsealed, an enterprise profile
// does not resolve to its 802.1X profile and password,
// the render data lacks a phone's group profiles and its own (a phone's
// profile with the same SSID replacing its group's), the group's roaming
// settings are missing, a model without Wi-Fi keys gets any, a profile in use
// can be deleted, or a Wi-Fi change leaves the phone's sync hash as it was.
func TestWifiRenderInputs(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	str := func(s string) *string { return &s }

	office, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "Office", SSID: "corp", Security: prov.WifiWPA2PSK,
		Priority: 5, Verified: true, PSK: str("psk-office-123")})
	if err != nil {
		t.Fatal(err)
	}
	if !office.PSKSet || office.Dot1XID != nil {
		t.Fatalf("office = %+v", office)
	}
	dx, err := st.CreateDot1XProfile(ctx, "test", Dot1XInput{Name: "Radius", Method: prov.Dot1XPEAP, Identity: "phones", Password: str("radius-pw-123")})
	if err != nil {
		t.Fatal(err)
	}
	md5, err := st.CreateDot1XProfile(ctx, "test", Dot1XInput{Name: "Wired only", Method: prov.Dot1XEAPMD5, Identity: "wired", Password: str("md5-pw-123")})
	if err != nil {
		t.Fatal(err)
	}
	corp, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "Corp", SSID: "corp", Security: prov.WifiWPA2Enterprise, Dot1XID: dx.ID})
	if err != nil {
		t.Fatal(err)
	}
	if corp.Dot1XID == nil || *corp.Dot1XID != dx.ID {
		t.Fatalf("corp = %+v", corp)
	}
	var verr *ValidationError
	for name, id := range map[string]int64{"unknown": 9999, "EAP-MD5": md5.ID} {
		if _, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "Bad " + name, SSID: "x", Security: prov.WifiWPA2Enterprise, Dot1XID: id}); !errors.As(err, &verr) {
			t.Errorf("%s 802.1X profile: err = %v, want a validation error", name, err)
		}
	}
	var psk []byte
	if err := db.QueryRowContext(ctx, `SELECT psk_enc FROM wifi_profiles WHERE id = $1`, office.ID).Scan(&psk); err != nil {
		t.Fatal(err)
	}
	if len(psk) == 0 || bytes.Contains(psk, []byte("psk-office-123")) {
		t.Fatal("a Wi-Fi secret is stored unsealed")
	}
	if _, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "OFFICE", SSID: "x", Security: prov.WifiOpen}); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	// The 802.1X profile cannot go, or turn into a wired-only method, while a network uses it.
	var used *InUseError
	if err := st.DeleteDot1XProfile(ctx, "test", dx.ID); !errors.As(err, &used) {
		t.Fatalf("deleting an 802.1X profile a network uses = %v, want a conflict", err)
	}
	if _, err := st.UpdateDot1XProfile(ctx, "test", dx.ID, Dot1XInput{Name: "Radius", Method: prov.Dot1XEAPMD5, Identity: "phones"}); !errors.As(err, &used) {
		t.Fatalf("turning it into EAP-MD5 = %v, want a conflict", err)
	}
	if got, err := st.GetDot1XProfile(ctx, dx.ID); err != nil || len(got.UsedByWifi) != 1 || got.UsedByWifi[0] != corp.ID {
		t.Fatalf("usedByWifiProfiles = %+v, %v", got.UsedByWifi, err)
	}

	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Handsets"})
	if err != nil {
		t.Fatal(err)
	}
	gid := g.ID
	mk := func(mac, vendor, model string) int64 {
		t.Helper()
		c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: mac, Vendor: prov.Vendor(vendor), Model: model,
			ExtensionID: ext, Enabled: true, Realm: "hello.test", GroupID: &gid})
		if err != nil {
			t.Fatal(err)
		}
		return c.Phone.ID
	}
	wp := mk("805ec0000071", "grandstream", "WP836")
	desk := mk("805ec0000072", "yealink", "T54W")
	wifi := func(id int64) *prov.WifiInfo {
		t.Helper()
		d, _, err := st.RenderInputs(ctx, id)
		if err != nil && !errors.Is(err, prov.ErrNoTemplate) {
			t.Fatal(err)
		}
		return d.Wifi
	}
	if wifi(wp) != nil {
		t.Fatal("a phone with nothing assigned has Wi-Fi render data")
	}
	h0, err := st.PhoneSyncHash(ctx, wp)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.PutWifiGroup(ctx, "test", gid, []int64{office.ID}, prov.WifiRadio{Threshold: ptr(-60)}); err != nil {
		t.Fatal(err)
	}
	w := wifi(wp)
	if w == nil || len(w.Profiles) != 1 || w.Profiles[0].PSK != "psk-office-123" || !w.Verified || w.Radio == nil || w.Radio.Threshold == nil {
		t.Fatalf("group wifi = %+v", w)
	}
	if wifi(desk) != nil {
		t.Fatal("a desk phone has Wi-Fi render data")
	}
	h1, _ := st.PhoneSyncHash(ctx, wp)
	if h1 == h0 {
		t.Fatal("a group Wi-Fi change left the sync hash as it was")
	}

	// The phone's own profile with the same SSID replaces the group's.
	if sc, err := st.PutWifiPhone(ctx, "test", wp, []int64{corp.ID}); err != nil || len(sc.ProfileIDs) != 1 || len(sc.InheritedIDs) != 1 || !sc.Pushing {
		t.Fatalf("phone scope = %+v, %v", sc, err)
	}
	w = wifi(wp)
	if len(w.Profiles) != 1 || w.Profiles[0].Name != "Corp" || w.Profiles[0].Dot1XID != dx.ID {
		t.Fatalf("merged = %+v", w.Profiles)
	}
	if d, _, err := st.RenderInputs(ctx, wp); err != nil || d.Dot1X == nil || len(d.Dot1X.Profiles) == 0 || d.Dot1X.Profiles[0].Password != "radius-pw-123" {
		t.Fatalf("the 802.1X profile did not reach the render data: %v", err)
	}

	// A profile in use cannot be deleted; an unused one can.
	if err := st.DeleteWifiProfile(ctx, "test", office.ID); !errors.As(err, &used) {
		t.Fatalf("deleting a profile in use = %v, want a conflict", err)
	}
	for name, ids := range map[string][]int64{"unknown": {9999}, "twice": {office.ID, office.ID}} {
		var v *ValidationError
		if _, err := st.PutWifiGroup(ctx, "test", gid, ids, prov.WifiRadio{}); !errors.As(err, &v) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	if _, err := st.PutWifiGroup(ctx, "test", 999999, nil, prov.WifiRadio{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown group: err = %v", err)
	}
	if _, err := st.PutWifiPhone(ctx, "test", 999999, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown phone: err = %v", err)
	}

	// Clearing removes the radio row; deleting the group removes its assignments.
	if sc, err := st.PutWifiGroup(ctx, "test", gid, nil, prov.WifiRadio{}); err != nil || sc.Radio == nil || *sc.Radio != (prov.WifiRadio{}) {
		t.Fatalf("cleared group = %+v, %v", sc, err)
	}
	if _, err := st.PutWifiPhone(ctx, "test", wp, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteWifiProfile(ctx, "test", office.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteWifiProfile(ctx, "test", office.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice = %v", err)
	}
}

// TestAdoptWifiEAP fails if an enterprise Wi-Fi profile from before the shared
// 802.1X model (its own identity and sealed password, migration 00074) is not
// moved into an 802.1X profile that opens to the same password, if the move
// repeats, or if a profile with an identity an 802.1X profile cannot hold is
// not left alone and reported.
func TestAdoptWifiEAP(t *testing.T) {
	st, db, _ := provStore(t)
	ctx := context.Background()
	legacy := func(name, identity, password string) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(ctx, `INSERT INTO wifi_profiles (name, ssid, security, eap_method, identity, anonymous_identity, ca_cert)
			VALUES ($1, $1, 'wpa2-enterprise', 'peap', $2, 'anon', 'tofu') RETURNING id`, name, identity).Scan(&id); err != nil {
			t.Fatal(err)
		}
		enc, err := st.box.Seal(password, prov.WifiLegacyPasswordAAD(id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE wifi_profiles SET password_enc = $2 WHERE id = $1`, id, enc); err != nil {
			t.Fatal(err)
		}
		return id
	}
	good := legacy("Corp", "phones", "old-radius-pw")
	bad := legacy("Spacey", "has a space", "pw")
	n, skipped, err := st.AdoptWifiEAP(ctx)
	if err != nil || n != 1 || len(skipped) != 1 || skipped[0] != "Spacey" {
		t.Fatalf("adopt = %d, %v, %v", n, skipped, err)
	}
	p, err := st.GetWifiProfile(ctx, good)
	if err != nil || p.Dot1XID == nil {
		t.Fatalf("adopted profile = %+v, %v", p, err)
	}
	dx, err := st.GetDot1XProfile(ctx, *p.Dot1XID)
	if err != nil || dx.Method != prov.Dot1XPEAP || dx.Identity != "phones" || dx.AnonymousIdentity != "anon" || !dx.HasPassword || dx.CACertPEM != "" {
		t.Fatalf("802.1X profile = %+v, %v", dx, err)
	}
	var enc []byte
	if err := db.QueryRowContext(ctx, `SELECT password_enc FROM dot1x_profiles WHERE id = $1`, dx.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if pw, err := st.box.Open(enc, prov.Dot1XPasswordAAD(dx.ID)); err != nil || pw != "old-radius-pw" {
		t.Fatalf("the password did not survive the move: %v", err)
	}
	if b, err := st.GetWifiProfile(ctx, bad); err != nil || b.Dot1XID != nil {
		t.Fatalf("the unfit profile was adopted: %+v, %v", b, err)
	}
	if n, _, err := st.AdoptWifiEAP(ctx); err != nil || n != 0 {
		t.Fatalf("a second run adopted %d, %v", n, err)
	}
}
