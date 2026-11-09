package prov

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var ringTestTags = []RingTag{
	{"internal", 1}, {"external", 2}, {"ringgroup", 3}, {"page", 1},
	{"doorbell", 4}, {"alarm", 5}, {"emergency-callback", 1},
}

// renderRing renders v's built-in file that carries the ring keys, with the
// ring map the tags and tones give.
func renderRing(t *testing.T, v Vendor, model string, tags []RingTag, tones map[string]string) string {
	t.Helper()
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.Ring = NewRingInfo(v, model, tags, tones)
	name := map[Vendor]string{
		Yealink:     d.Phone.MAC + ".cfg",
		Poly:        d.Phone.MAC + "-hello.cfg",
		Grandstream: "cfg" + d.Phone.MAC + ".xml",
		Snom:        d.Phone.MACUpper,
		Fanvil:      d.Phone.MAC + ".cfg",
	}[v]
	b, err := Render(context.Background(), builtinFor(t, v), name, d)
	if err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	return string(b)
}

func wantLines(t *testing.T, label, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s: the rendered configuration lacks %q", label, w)
		}
	}
}

func wantNoLines(t *testing.T, label, out string, not ...string) {
	t.Helper()
	for _, w := range not {
		if strings.Contains(out, w) {
			t.Errorf("%s: the rendered configuration has %q", label, w)
		}
	}
}

// TestRingMapProvisioning renders every vendor's built-in template with the
// seven built-in tags and a custom one, and fails if a template omits a
// mapped tag's match rule or tone key, if a choice (as a group override
// would make it) is not applied, if the default tone of the line does not
// follow the internal tag, or if a ring map change leaves the render data
// (which the automatic resync hashes) unchanged.
func TestRingMapProvisioning(t *testing.T) {
	tags := append(slices.Clone(ringTestTags), RingTag{"vip", 4})

	t.Run("yealink", func(t *testing.T) {
		out := renderRing(t, Yealink, "T54W", tags, map[string]string{"doorbell": "bellcore2", "vip": "7"})
		wantLines(t, "yealink", out,
			"distinctive_ring_tones.alert_info.1.text = hello-emergency-callback\n",
			"distinctive_ring_tones.alert_info.1.ringer = 1\n",
			"distinctive_ring_tones.alert_info.5.text = hello-doorbell\n",
			"distinctive_ring_tones.alert_info.5.ringer = 10\n", // bellcore2 follows Ring8 (verify)
			"distinctive_ring_tones.alert_info.8.text = hello-vip\n", "distinctive_ring_tones.alert_info.8.ringer = 7\n",
			"hello-external\n", "account.1.ringtone.ring_type = Ring1.wav\n")
		// Longest text first, so hello-door cannot shadow hello-doorbell.
		wantNoLines(t, "yealink", out, "alert_info.9.")
	})
	t.Run("poly", func(t *testing.T) {
		out := renderRing(t, Poly, "VVX 450", tags, map[string]string{"alarm": "silent", "vip": "ringer9"})
		wantLines(t, "poly", out,
			`voIpProt.SIP.alertInfo.1.value="Auto Answer"`, // the auto-answer rule keeps number 1
			`voIpProt.SIP.alertInfo.2.value="hello-emergency-callback" voIpProt.SIP.alertInfo.2.class="custom1"`,
			`voIpProt.SIP.alertInfo.4.value="hello-internal" voIpProt.SIP.alertInfo.4.class="custom3"`,
			`se.rt.custom1.ringer="ringer2"`, // emergency-callback: slot 1
			`se.rt.custom3.ringer="ringer2"`, // internal
			`se.rt.default.ringer="ringer2"`,
			`ringer="silent"`, `ringer="ringer9"`)
	})
	t.Run("snom", func(t *testing.T) {
		out := renderRing(t, Snom, "D785", tags, map[string]string{"ringgroup": "Silent", "external": "Ringer7"})
		wantLines(t, "snom", out,
			`<alert_internal_ring_text perm="R">alert-internal</alert_internal_ring_text>`,
			`<alert_internal_ring_sound perm="R">Ringer1</alert_internal_ring_sound>`,
			`<alert_external_ring_text perm="R">alert-external</alert_external_ring_text>`,
			`<alert_external_ring_sound perm="R">Ringer7</alert_external_ring_sound>`,
			`<alert_group_ring_text perm="R">alert-group</alert_group_ring_text>`,
			`<alert_group_ring_sound perm="R">Silent</alert_group_ring_sound>`,
			`<ring_sound idx="1" perm="R">Ringer1</ring_sound>`)
	})
	t.Run("fanvil", func(t *testing.T) {
		out := renderRing(t, Fanvil, "X6U", tags, map[string]string{"internal": "9"})
		wantLines(t, "fanvil", out,
			"SIP1 Alert Info Text 1 :hello-emergency-callback\n",
			"SIP1 Alert Info Text 8 :hello-vip\n",
			"SIP1 Ring Type     :9\n")
	})
	t.Run("grandstream WP8x6", func(t *testing.T) {
		out := renderRing(t, Grandstream, "WP836", tags, map[string]string{"external": "2", "alarm": "3"})
		// Slot 1 is internal (system), 2 external, 3 ringgroup, 4 doorbell, 5 alarm.
		wantLines(t, "wp836", out, "<P95026>0,2,0,0,3</P95026>")
		wantNoLines(t, "wp836", out, "<P1488>", "<P104>")
	})
	t.Run("grandstream WP822", func(t *testing.T) {
		out := renderRing(t, Grandstream, "WP822", tags, map[string]string{"internal": "1", "page": "2"})
		wantLines(t, "wp822", out, "<P1488>hello-emergency-callback</P1488>", "<P1489>0</P1489>",
			"<P104>1</P104>", "<P1490>", "<P1502>") // eight rules: P1488 to P1503
		wantNoLines(t, "wp822", out, "<P95026>", "<P1504>")
	})
	t.Run("grandstream without a known key", func(t *testing.T) {
		for _, m := range []string{"WP856", "GRP2614"} {
			out := renderRing(t, Grandstream, m, tags, nil)
			wantNoLines(t, m, out, "<P95026>", "<P1488>", "<P104>")
		}
	})
}

// TestRingMapChangeChangesRenderData: the automatic resync hashes the render
// data, so a different ring map must show up in it.
func TestRingMapChangeChangesRenderData(t *testing.T) {
	for _, v := range RingVendors {
		model := map[Vendor]string{Yealink: "T54W", Poly: "VVX 450", Grandstream: "WP822", Snom: "D785", Fanvil: "X6U"}[v]
		a, _ := json.Marshal(NewRingInfo(v, model, ringTestTags, nil))
		tone := RingTones(v)[len(RingTones(v))-1].ID
		b, _ := json.Marshal(NewRingInfo(v, model, ringTestTags, map[string]string{"internal": tone, "external": tone}))
		if string(a) == string(b) {
			t.Errorf("%s: a ring map change does not change the render data", v)
		}
	}
}

func TestNewRingInfo(t *testing.T) {
	t.Run("defaults are valid tones", func(t *testing.T) {
		for _, v := range RingVendors {
			for slot := 1; slot <= 5; slot++ {
				if id := DefaultRingTone(v, slot); !ValidRingTone(v, id) {
					t.Errorf("default %s slot %d = %q is not in the vendor's list", v, slot, id)
				}
			}
			for _, tn := range RingTones(v) {
				if tn.Silent && tn.ID != "silent" && tn.ID != "Silent" {
					t.Errorf("%s: silent tone has id %q", v, tn.ID)
				}
			}
		}
	})
	t.Run("an unknown tone falls back to the default", func(t *testing.T) {
		r := NewRingInfo(Yealink, "T54W", ringTestTags, map[string]string{"internal": "Ring99"})
		for _, ru := range r.Rules {
			if ru.Tag == "internal" && ru.Tone != "1" {
				t.Fatalf("rule = %+v", ru)
			}
		}
	})
	t.Run("no key no rules", func(t *testing.T) {
		if r := NewRingInfo(Generic, "x", ringTestTags, nil); len(r.Rules) != 0 || r.Default != "" || r.Bellcore != "" {
			t.Fatalf("generic = %+v", r)
		}
		if r := NewRingInfo(Grandstream, "WP856", ringTestTags, nil); len(r.Rules) != 0 {
			t.Fatalf("WP856 = %+v", r)
		}
	})
	t.Run("a tag that starts another comes after it", func(t *testing.T) {
		r := NewRingInfo(Yealink, "T54W", []RingTag{{"door", 1}, {"doorbell", 4}}, nil)
		if r.Rules[0].Tag != "doorbell" || r.Rules[1].Tag != "door" || r.Rules[0].N != 1 || r.Rules[1].N != 2 {
			t.Fatalf("rules = %+v", r.Rules)
		}
	})
	t.Run("silent is flagged", func(t *testing.T) {
		r := NewRingInfo(Poly, "VVX 450", ringTestTags, map[string]string{"alarm": "silent"})
		for _, ru := range r.Rules {
			if (ru.Tag == "alarm") != ru.Silent {
				t.Errorf("rule %s silent = %v", ru.Tag, ru.Silent)
			}
		}
	})
	t.Run("slots are bounded", func(t *testing.T) {
		var many []RingTag
		for i := range 40 {
			many = append(many, RingTag{Name: "t" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Slot: 1})
		}
		for _, c := range []struct {
			v     Vendor
			model string
			n     int
		}{{Yealink, "T54W", 10}, {Poly, "VVX 450", 23}, {Fanvil, "X6U", 10}, {Grandstream, "WP822", 10}} {
			if got := len(NewRingInfo(c.v, c.model, many, nil).Rules); got != c.n {
				t.Errorf("%s: %d rules, want %d", c.v, got, c.n)
			}
		}
	})
	t.Run("a shared Bellcore slot takes the first tag's tone", func(t *testing.T) {
		r := NewRingInfo(Grandstream, "WP836", ringTestTags, map[string]string{"page": "3", "internal": "1"})
		if !strings.HasPrefix(r.Bellcore, "1,") {
			t.Fatalf("bellcore = %q, want internal's tone first", r.Bellcore)
		}
	})
}
