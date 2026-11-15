package prov

import (
	"strconv"
	"strings"
	"testing"
)

// TestWPSideKey fails if a WP's side key is not written with the key and
// value of its model's Grandstream template (P82317 on the WP8x6 and WP856,
// P22258 on the WP820, none on the WP810/822/825), if alarm reaches a model
// without an alarm value or a WP8x6 without an alarm destination, or if a
// phone that is not a WP gets a side key.
func TestWPSideKey(t *testing.T) {
	cases := []struct {
		model, function string
		alarm           bool
		want            string // "" means no side key is written
	}{
		{"WP836", "", false, "<P82317>0</P82317>"},
		{"WP836", SideKeyPTT, false, "<P82317>0</P82317>"},
		{"WP826", SideKeyAlarm, true, "<P82317>2</P82317>"},
		{"WP816", SideKeyAlarm, false, "<P82317>0</P82317>"}, // nothing to dial: PTT/paging
		{"WP836", SideKeyNone, false, "<P82317>4</P82317>"},
		{"WP856", SideKeyNone, false, "<P82317>4</P82317>"},
		{"WP856", SideKeyAlarm, true, "<P82317>0</P82317>"}, // no alarm value on the WP856
		{"WP820", SideKeyNone, false, "<P22258>4</P22258>"},
		{"WP820", SideKeyPTT, false, "<P22258>0</P22258>"},
		{"WP822", SideKeyNone, false, ""},
		{"GRP2614", SideKeyNone, false, ""},
	}
	for _, c := range cases {
		t.Run(c.model+"/"+c.function, func(t *testing.T) {
			d := gsFamilyData(c.model)
			d.SideKey = SideKey{Function: c.function}
			if c.alarm {
				d.AlarmNumber = AlarmNumber
			}
			got := renderDevice(t, Grandstream, d)
			for _, key := range []string{"<P82317>", "<P22258>"} {
				n := strings.Count(got, key)
				switch {
				case c.want != "" && strings.HasPrefix(c.want, key) && n != 1:
					t.Errorf("%s written %d times", key, n)
				case (c.want == "" || !strings.HasPrefix(c.want, key)) && n != 0:
					t.Errorf("%s written for a model without it", key)
				}
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("want %s", c.want)
			}
		})
	}
	if got := SideKeyChoices(Grandstream, "WP836"); len(got) != 3 {
		t.Errorf("WP836 choices = %v", got)
	}
	if got := SideKeyChoices(Grandstream, "WP856"); len(got) != 2 || got[1] != SideKeyNone {
		t.Errorf("WP856 choices = %v", got)
	}
	if got := SideKeyChoices(Grandstream, "WP822"); got != nil {
		t.Errorf("WP822 choices = %v", got)
	}
}

// TestWPSpeedDials fails if a WP's BLF entries are not written as speed
// dials on the slots of its model's template (WP8x6 number keys, WP8xx and
// WP820 speed dial keys, WP856 multi-purpose keys), in order (keys 2 to 9,
// then 0), with every unused slot cleared, if a WP still gets BLF keys, or
// if a phone that is not a WP gets speed dials.
func TestWPSpeedDials(t *testing.T) {
	entries := func(n int) []BLFKey {
		var out []BLFKey
		for i := range n {
			num := strconv.Itoa(201 + i)
			out = append(out, BLFKey{Number: num, Label: "Ext & " + num, URI: "sip:" + num + "@hello.example"})
		}
		return out
	}
	cases := []struct {
		model string
		want  []string
		not   []string
	}{
		{"WP836", []string{
			// key 2 first: mode, account, label, value
			"<P8690>0</P8690>", "<P8691>0</P8691>", "<P8692>Ext &amp; 201</P8692>", "<P8693>201</P8693>",
			"<P8694>0</P8694>", "<P8697>202</P8697>",
			// key 9 holds the 8th entry, key 0 is unused and cleared
			"<P8718>0</P8718>", "<P8721>208</P8721>",
			"<P8682>-1</P8682>", "<P8685></P8685>",
		}, []string{"<P323>", "<P23003>"}},
		{"WP822", []string{
			"<P23011>0</P23011>", "<P23013>201</P23013>", "<P23018>202</P23018>",
			"<P23048>208</P23048>", "<P23001>0</P23001>", "<P23003></P23003>",
		}, []string{"<P323>", "<P8690>", "<P23000>", "<P23012>"}},
		{"WP820", []string{"<P23013>201</P23013>", "<P23048>208</P23048>", "<P23003></P23003>"}, []string{"<P323>"}},
		{"WP856", []string{
			"<P23000>0</P23000>", "<P23001>0</P23001>", "<P23002>Ext &amp; 201</P23002>", "<P23003>201</P23003>",
			"<P23038>208</P23038>", "<P23043></P23043>", "<P23148></P23148>",
		}, []string{"<P323>", "<P23153>"}},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			d := gsFamilyData(c.model)
			d.BLF = entries(8)
			got := renderDevice(t, Grandstream, d)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s", w)
				}
			}
			for _, n := range c.not {
				if strings.Contains(got, n) {
					t.Errorf("writes %s", n)
				}
			}
			if strings.Contains(got, "blf-") {
				t.Error("a WP got a BLF list URI")
			}
		})
	}

	// Entries past the last slot are dropped.
	d := gsFamilyData("WP822")
	d.BLF = entries(12)
	if got := renderDevice(t, Grandstream, d); strings.Contains(got, ">210<") || !strings.Contains(got, "<P23003>209</P23003>") {
		t.Error("the WP822's ninth entry is not on key 0, or a tenth was written")
	}
	// A desk phone keeps its BLF keys and gets no speed dials.
	d = gsFamilyData("GRP2614")
	d.BLF = entries(2)
	got := renderDevice(t, Grandstream, d)
	if !strings.Contains(got, "<P325>201</P325>") || strings.Contains(got, "<P23013>") || strings.Contains(got, "<P8690>") {
		t.Error("a GRP lost its BLF keys or got speed dials")
	}

	for model, want := range map[string]int{"WP836": 9, "WP822": 9, "WP820": 9, "WP856": 30, "GRP2614": 0} {
		if got := SpeedDialSlots(Grandstream, model); got != want {
			t.Errorf("SpeedDialSlots(%s) = %d, want %d", model, got, want)
		}
	}
	if SpeedDialSlots(Yealink, "T54W") != 0 {
		t.Error("a Yealink has speed-dial slots")
	}
}
