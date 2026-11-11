package prov

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func wprof(name, ssid, sec string, prio int) WifiProfile {
	p := WifiProfile{Name: name, SSID: ssid, Security: sec, Priority: prio, CACert: CAServerSystem}
	switch {
	case WifiUsesPSK(sec):
		p.PSK = "secret-" + name
	case WifiEnterprise(sec):
		p.EAPMethod, p.Identity, p.Password = EAPPEAP, "id-"+name, "pw-"+name
	}
	return p
}

func keyMap(ks []WifiKey) map[string]string {
	m := map[string]string{}
	for _, k := range ks {
		m[k.P] = k.V
	}
	return m
}

func renderWifi(t *testing.T, model string, w *WifiInfo) string {
	t.Helper()
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = Grandstream, model
	d.Wifi = w
	out, err := Render(context.Background(), builtinFor(t, Grandstream), ExpandPattern("cfg{mac}.xml", d.Phone), d)
	if err != nil {
		t.Fatalf("%s: %v", model, err)
	}
	return string(out)
}

// TestWifiProvisioning fails if a profile renders without a verified profile
// in the phone's set, a PSK or 802.1X password reaches a preview or a config
// that must not carry it, more profiles are written than the model has slots
// for, a free slot keeps an old network, or a radio setting does not render
// its P-value (phone-feature-parity S-23 and S-24).
func TestWifiProvisioning(t *testing.T) {
	t.Run("nothing until a profile is verified", func(t *testing.T) {
		w := &WifiInfo{Profiles: []WifiProfile{wprof("office", "Office", WifiWPA2PSK, 5)}}
		for _, model := range []string{"WP836", "WP822", "WP856"} {
			out := renderWifi(t, model, w)
			if strings.Contains(out, "<P83000>") || strings.Contains(out, "<P7812>") || strings.Contains(out, "secret-office") {
				t.Errorf("%s: an unverified profile was written:\n%s", model, "(config withheld)")
			}
		}
		w.Verified = true
		if out := renderWifi(t, "WP836", w); !strings.Contains(out, "<P83000>Office</P83000>") {
			t.Error("a verified profile is not written")
		}
	})

	t.Run("WP8x6 keys", func(t *testing.T) {
		ent := wprof("corp", "Corp", WifiWPA2Enterprise, 9)
		ent.AnonymousIdentity, ent.PreferAccount = "anon", 2
		w := WifiInfo{Verified: true, Profiles: []WifiProfile{wprof("office", "Office", WifiWPA3SAE, 5), ent, wprof("guest", "Guest", WifiOpen, 1), wprof("owe", "Owe", WifiOWE, 0)}}
		m := keyMap(WifiKeys(FamilyWP8x6, w))
		for k, v := range map[string]string{
			"83000": "Corp", "83002": "5", "83004": "2", "83016": "1", "83013": "0", "83012": "anon", "83014": "id-corp", "83015": "pw-corp", "83049": "9", "83085": "2",
			"83100": "Office", "83102": "11", "83103": "secret-office", "83149": "5",
			"83200": "Guest", "83202": "0", "83249": "1",
			"83300": "Owe", "83302": "14",
			"83400": "",
		} {
			if m[k] != v {
				t.Errorf("P%s = %q, want %q", k, m[k], v)
			}
		}
		if _, ok := m["83403"]; ok {
			t.Error("a free slot wrote a password")
		}
	})

	t.Run("WP8x6 enterprise skips network 5", func(t *testing.T) {
		var ps []WifiProfile
		for i := 0; i < 5; i++ {
			ps = append(ps, wprof(fmt.Sprintf("p%d", i), fmt.Sprintf("S%d", i), WifiWPA2Enterprise, 10-i))
		}
		m := keyMap(WifiKeys(FamilyWP8x6, WifiInfo{Verified: true, Profiles: ps}))
		if m["83400"] != "" || m["83402"] != "" || m["83404"] != "" {
			t.Errorf("slot 5 was written for an enterprise profile: %v", m)
		}
		if m["83500"] != "S4" || m["83504"] != "2" {
			t.Errorf("the fifth profile did not move to slot 6: %v", m)
		}
	})

	t.Run("slot limits", func(t *testing.T) {
		var ps []WifiProfile
		for i := 0; i < 30; i++ {
			ps = append(ps, wprof(fmt.Sprintf("p%02d", i), fmt.Sprintf("S%02d", i), WifiWPA2PSK, 0))
		}
		for family, max := range map[string]int{FamilyWP8x6: 10, FamilyWP8xx: 17, FamilyWP856: 1} {
			keys := WifiKeys(family, WifiInfo{Verified: true, Profiles: ps})
			ssids := 0
			for _, k := range keys {
				if strings.HasPrefix(k.V, "S") && len(k.V) == 3 {
					ssids++
				}
			}
			if ssids != max {
				t.Errorf("%s: %d networks written, want %d", family, ssids, max)
			}
		}
		m := keyMap(WifiKeys(FamilyWP8x6, WifiInfo{Verified: true, Profiles: ps}))
		for k := range m {
			if k > "83999" && len(k) == 5 && k[:2] == "83" {
				t.Errorf("WP8x6 wrote P%s past network 10", k)
			}
		}
	})

	t.Run("WP8xx keys", func(t *testing.T) {
		hidden := wprof("hid", "Hid", WifiWPA3SAE, 1)
		hidden.Hidden = true
		ent := wprof("corp", "Corp", WifiWPA2Enterprise, 9)
		r := true
		w := WifiInfo{Verified: true, Radio: &WifiRadio{Dot11r: &r}, Profiles: []WifiProfile{wprof("office", "Office", WifiWPA2PSK, 5), hidden, ent}}
		m := keyMap(WifiKeys(FamilyWP8xx, w))
		for k, v := range map[string]string{
			"83000": "Office", "83001": "1", "83002": "4", "83003": "secret-office", "83048": "1", "83050": "0",
			"83100": "Hid", "83102": "11", "83150": "1", "83148": "1",
			"83200": "", "83201": "0",
		} {
			if m[k] != v {
				t.Errorf("P%s = %q, want %q", k, m[k], v)
			}
		}
		for _, s := range m {
			if s == "id-corp" || s == "pw-corp" || s == "Corp" {
				t.Error("an enterprise profile was written to a model with no 802.1X keys")
			}
		}
	})

	t.Run("WP856 keys", func(t *testing.T) {
		w := WifiInfo{Verified: true, Profiles: []WifiProfile{wprof("a", "A", WifiWPA2PSK, 1), wprof("b", "B", WifiWPA2PSK, 8), wprof("c", "C", WifiWPA3SAE, 10)}}
		m := keyMap(WifiKeys(FamilyWP856, w))
		if m["7812"] != "B" || m["7814"] != "2" || m["7830"] != "secret-b" {
			t.Errorf("WP856 network = %v, want the best profile it can take", m)
		}
	})

	t.Run("radio keys", func(t *testing.T) {
		i := func(n int) *int { return &n }
		s := func(v string) *string { return &v }
		b := func(v bool) *bool { return &v }
		all := &WifiRadio{Band: s(Band5), RoamMode: s(RoamBoost), Threshold: i(-62), PoorScan: i(12), GoodScan: i(300), Gain: i(9), Dot11r: b(true), Dot11v: b(true), Prompt: b(true)}
		for family, want := range map[string]map[string]string{
			FamilyWP8x6: {"25438": "2", "82332": "1", "22232": "-62", "22231": "12", "82334": "1"},
			FamilyWP8xx: {"22232": "-62", "22231": "12", "84992": "1"},
			FamilyWP856: {"25420": "0", "25421": "1", "82332": "1", "22232": "-62", "22443": "9", "22230": "300", "22231": "12", "25315": "1", "82333": "1"},
		} {
			m := keyMap(WifiKeys(family, WifiInfo{Radio: all}))
			for k, v := range want {
				if m[k] != v {
					t.Errorf("%s: P%s = %q, want %q", family, k, m[k], v)
				}
			}
			if len(m) != len(want) {
				t.Errorf("%s: wrote %v, want only %v", family, m, want)
			}
		}
		// Radio settings do not wait for a verified profile.
		if m := keyMap(WifiKeys(FamilyWP8x6, WifiInfo{Radio: &WifiRadio{Band: s(Band24)}})); m["25438"] != "1" {
			t.Errorf("band 2.4 = %v", m)
		}
		// Fast roaming is a WP856 mode only.
		if m := keyMap(WifiKeys(FamilyWP8x6, WifiInfo{Radio: &WifiRadio{RoamMode: s(RoamFast)}})); len(m) != 0 {
			t.Errorf("WP8x6 wrote a roaming mode it does not have: %v", m)
		}
	})

	t.Run("models without keys", func(t *testing.T) {
		w := WifiInfo{Verified: true, Profiles: []WifiProfile{wprof("a", "A", WifiWPA2PSK, 1)}}
		for _, c := range []struct {
			v     Vendor
			model string
		}{{Grandstream, "WP820"}, {Grandstream, "GRP2614"}, {Yealink, "T54W"}} {
			if f := WifiFamily(c.v, c.model); f != "" {
				t.Errorf("%s %s has Wi-Fi family %q", c.v, c.model, f)
			}
			d := renderSample(false)
			d.Phone.Vendor, d.Phone.Model, d.Wifi = c.v, c.model, &w
			out, err := Render(context.Background(), builtinFor(t, c.v), ExpandPattern(map[Vendor]string{Grandstream: "cfg{mac}.xml", Yealink: "{mac}.cfg"}[c.v], d.Phone), d)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "secret-a") {
				t.Errorf("%s %s: a PSK was written", c.v, c.model)
			}
		}
	})

	t.Run("validation", func(t *testing.T) {
		cases := map[string]struct {
			p       WifiProfile
			psk, pw bool
			path    string
		}{
			"ok":              {wprof("a", "A", WifiWPA2PSK, 1), true, false, ""},
			"short psk":       {func() WifiProfile { p := wprof("a", "A", WifiWPA2PSK, 1); p.PSK = "short"; return p }(), true, false, "psk"},
			"no psk":          {func() WifiProfile { p := wprof("a", "A", WifiWPA2PSK, 1); p.PSK = ""; return p }(), false, false, "psk"},
			"long ssid":       {wprof("a", strings.Repeat("x", 33), WifiOpen, 1), false, false, "ssid"},
			"priority":        {wprof("a", "A", WifiOpen, 11), false, false, "priority"},
			"unknown":         {wprof("a", "A", "wep", 1), false, false, "security"},
			"no eap password": {wprof("a", "A", WifiWPA2Enterprise, 1), false, false, "password"},
			"eap on psk":      {func() WifiProfile { p := wprof("a", "A", WifiWPA2PSK, 1); p.Identity = "x"; return p }(), true, false, "eapMethod"},
		}
		for name, c := range cases {
			errs := CheckWifiProfile(c.p, c.psk, c.pw)
			switch {
			case c.path == "" && len(errs) > 0:
				t.Errorf("%s: %v", name, errs)
			case c.path != "":
				found := false
				for _, e := range errs {
					found = found || e.Path == c.path
				}
				if !found {
					t.Errorf("%s: no error at %s: %v", name, c.path, errs)
				}
			}
		}
		n := 20
		if errs := CheckWifiRadio(WifiRadio{Threshold: &n}); len(errs) != 1 || errs[0].Path != "radio.threshold" {
			t.Errorf("threshold -> %v", errs)
		}
	})

	t.Run("merge", func(t *testing.T) {
		g := []WifiProfile{wprof("g1", "Same", WifiWPA2PSK, 1), wprof("g2", "Other", WifiOpen, 1)}
		p := []WifiProfile{wprof("p1", "Same", WifiWPA3SAE, 1)}
		got := MergeWifi(g, p)
		if len(got) != 2 || got[0].Name != "p1" || got[1].Name != "g2" {
			t.Errorf("merge = %+v", got)
		}
	})
}
