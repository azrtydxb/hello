package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestWifiRenderInputs fails if a PSK or 802.1X password is stored unsealed,
// the render data lacks a phone's group profiles and its own (a phone's
// profile with the same SSID replacing its group's), the group's roaming
// settings are missing, a model without Wi-Fi keys gets any, a profile in use
// can be deleted, or a Wi-Fi change leaves the phone's sync hash as it was.
func TestWifiRenderInputs(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	str := func(s string) *string { return &s }

	office, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "Office", SSID: "corp", Security: prov.WifiWPA2PSK,
		CACert: prov.CAServerSystem, Priority: 5, Verified: true, PSK: str("psk-office-123")})
	if err != nil {
		t.Fatal(err)
	}
	if !office.PSKSet || office.PasswordSet {
		t.Fatalf("office = %+v", office)
	}
	corp, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "Corp", SSID: "corp", Security: prov.WifiWPA2Enterprise,
		EAPMethod: prov.EAPPEAP, Identity: "phones", CACert: prov.CAServerSystem, Password: str("radius-pw-123")})
	if err != nil {
		t.Fatal(err)
	}
	var psk, pw []byte
	if err := db.QueryRowContext(ctx, `SELECT p.psk_enc, (SELECT password_enc FROM wifi_profiles WHERE id = $2) FROM wifi_profiles p WHERE p.id = $1`, office.ID, corp.ID).Scan(&psk, &pw); err != nil {
		t.Fatal(err)
	}
	if len(psk) == 0 || bytes.Contains(psk, []byte("psk-office-123")) || len(pw) == 0 || bytes.Contains(pw, []byte("radius-pw-123")) {
		t.Fatal("a Wi-Fi secret is stored unsealed")
	}
	if _, err := st.CreateWifiProfile(ctx, "test", WifiProfileInput{Name: "OFFICE", SSID: "x", Security: prov.WifiOpen, CACert: prov.CAServerSystem}); err == nil {
		t.Fatal("a duplicate name was accepted")
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
	if len(w.Profiles) != 1 || w.Profiles[0].Name != "Corp" || w.Profiles[0].Password != "radius-pw-123" {
		t.Fatalf("merged = %+v", w.Profiles)
	}

	// A profile in use cannot be deleted; an unused one can.
	var used *InUseError
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
