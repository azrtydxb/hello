package prov

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func renderDevice(t *testing.T, v Vendor, d RenderData) string {
	t.Helper()
	name := "cfg" + d.Phone.MAC + ".xml"
	if v == Yealink {
		name = d.Phone.MAC + ".cfg"
	}
	got, err := Render(context.Background(), builtinFor(t, v), name, d)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// TestSettingOverrides fails if the order template, group, phone is broken,
// a deny-listed key is accepted, a key of another vendor is rendered, or a
// secret value is rendered for another phone.
func TestSettingOverrides(t *testing.T) {
	d := gsFamilyData("WP836")
	d.Overrides = ResolveOverrides(Grandstream,
		[]Override{{Key: "P22208", Value: "0"}, {Key: "P71", Value: "group-hotline"}, {Key: "P1558", Value: "34"}, {Key: "features.text_message.enable", Value: "0"}},
		[]Override{{Key: "P71", Value: "phone-hotline"}, {Key: "P9999", Value: "s3cret&<x>", Secret: true}})
	got := renderDevice(t, Grandstream, d)

	for _, want := range []string{
		"<P71>phone-hotline</P71>",            // phone beats group
		"<P22208>0</P22208>",                  // group beats template (the template writes 1)
		"<P1558>34</P1558>",                   // group beats a template value that is not constant
		"<P9999>s3cret&amp;&lt;x&gt;</P9999>", // a new key is added, XML escaped
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"group-hotline", "<P22208>1</P22208>", "<P1558>26</P1558>", "features.text_message"} {
		if strings.Contains(got, not) {
			t.Errorf("still carries %q", not)
		}
	}
	if n := strings.Count(got, "<P22208>"); n != 1 {
		t.Errorf("P22208 written %d times", n)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "</gs_provision>") || !strings.Contains(got, "</config>") {
		t.Errorf("file structure broken")
	}

	// Another phone without the phone-level secret never sees it.
	other := gsFamilyData("WP836")
	other.Overrides = ResolveOverrides(Grandstream, []Override{{Key: "P71", Value: "group-hotline"}}, nil)
	if o := renderDevice(t, Grandstream, other); strings.Contains(o, "s3cret") || strings.Contains(o, "P9999") {
		t.Error("a phone's secret override rendered for another phone")
	}

	// Without overrides the render is byte-identical to before.
	plain := gsFamilyData("WP836")
	if renderDevice(t, Grandstream, plain) == got {
		t.Error("overrides changed nothing")
	}
}

func TestSettingOverridesYealink(t *testing.T) {
	d := SampleData(true)
	d.Phone.Vendor, d.Phone.Model = Yealink, "T54W"
	d.Overrides = ResolveOverrides(Yealink,
		[]Override{{Key: "features.text_message.enable", Value: "0"}, {Key: "static.network.qos.signaltos", Value: "10"}},
		[]Override{{Key: "static.network.qos.signaltos", Value: "12"}, {Key: "phone_setting.backlight_time", Value: "30"}})
	got := renderDevice(t, Yealink, d)
	for _, want := range []string{"features.text_message.enable = 0\n", "static.network.qos.signaltos = 12\n", "phone_setting.backlight_time = 30\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "signaltos = 10") || strings.Contains(got, "text_message.enable = 1") {
		t.Error("a template value survived")
	}
	if strings.Count(got, "static.network.qos.signaltos =") != 1 {
		t.Error("key written twice")
	}
	if !strings.HasPrefix(got, "#!version:") {
		t.Error("the version line moved")
	}
}

func TestOverrideDenyList(t *testing.T) {
	denied := []string{"P2", "P3", "P34", "P35", "P36", "P47", "P212", "P237", "P8433", "P270", "P271",
		"account.1.password", "account.2.user_name", "account.1.sip_server.1.address",
		"static.auto_provision.server.url", "static.trusted_certificates.url", "static.security.user_password"}
	for _, k := range denied {
		if err := ValidateOverride("", k, "x"); err == nil {
			t.Errorf("%s was accepted", k)
		}
	}
	// The identity of accounts 2 to 6 (lines, item 21) is Hello's too:
	// Active, Server, User, Auth ID, Password, Name and Account Name.
	for _, k := range []string{"P401", "P402", "P404", "P405", "P406", "P407", "P417", "P501", "P502", "P504", "P505", "P506", "P507", "P517",
		"P601", "P617", "P1701", "P1704", "P1717", "P1801", "P1806", "P1817"} {
		if err := ValidateOverride("", k, "x"); err == nil {
			t.Errorf("%s (identity of accounts 2 to 6) was accepted", k)
		}
	}
	for _, k := range []string{"P71", "P8388", "P22208", "P403", "P426", "features.text_message.enable", "account.1.codec.g722.priority"} {
		if err := ValidateOverride("", k, "x"); err != nil {
			t.Errorf("%s refused: %v", k, err)
		}
	}
	for _, bad := range []string{"", "p71", "P", "Phone", "P1234567", "a b", "x=y", "../x", "SIP1 Phone Number"} {
		if err := ValidateOverride("", bad, "x"); err == nil {
			t.Errorf("key %q was accepted", bad)
		}
	}
	if err := ValidateOverride(Yealink, "P71", "x"); err == nil {
		t.Error("a Grandstream key on a Yealink phone was accepted")
	}
	for _, v := range []string{"a\nb", "a\r", "a\x00", strings.Repeat("x", MaxOverrideValue+1)} {
		if err := ValidateOverride("", "P71", v); err == nil {
			t.Errorf("value %q accepted", v)
		}
	}
}

// A deny-listed key can never be reached through the rendering either: even
// if one were stored, the account keys the template writes come out of the
// template, and ResolveOverrides keeps only the phone vendor's keys.
func TestResolveOverridesVendor(t *testing.T) {
	got := ResolveOverrides(Grandstream, []Override{{Key: "features.x.y", Value: "1"}, {Key: "P5", Value: "a"}}, nil)
	if len(got) != 1 || got[0].Key != "P5" {
		t.Errorf("got %+v", got)
	}
	if got := ResolveOverrides(Snom, []Override{{Key: "P5", Value: "a"}}, nil); len(got) != 0 {
		t.Errorf("snom got %+v", got)
	}
}

func TestApplyOverridesIgnoresOtherFiles(t *testing.T) {
	in := []byte("<polycomConfig/>")
	out, err := ApplyOverrides(Poly, in, []Override{{Key: "P5", Value: "1"}})
	if err != nil || !bytes.Equal(out, in) {
		t.Errorf("poly changed: %q %v", out, err)
	}
	in = []byte("<gs_provision><mac>x</mac></gs_provision>")
	if out, _ := ApplyOverrides(Grandstream, in, []Override{{Key: "P5", Value: "1"}}); !bytes.Equal(out, in) {
		t.Errorf("a file without <config> changed: %q", out)
	}
}

func TestOverrideAADBindsOwner(t *testing.T) {
	a := OverrideAAD(ScopePhone, 1, "P71")
	for _, b := range []string{OverrideAAD(ScopePhone, 2, "P71"), OverrideAAD(ScopeGroup, 1, "P71"), OverrideAAD(ScopePhone, 1, "P72")} {
		if a == b {
			t.Errorf("AAD %q not distinct", b)
		}
	}
}

// TestOverrideWPKeys fails if one of the WP keys that have no field of their
// own (speed dial, off-hook auto dial, wallpaper, Wi-Fi tuning, side key) is
// refused or not rendered as one element with its value. The keys, models and
// values are from Grandstream's own templates: wp8x6_config_1.0.3.39,
// wp810_822_825_config_1.0.11.83, wp820_config_1.0.7.90 and
// wp856_config_1.0.3.16 (see decisions.md, "Grandstream keys reachable through
// overrides").
func TestOverrideWPKeys(t *testing.T) {
	for _, c := range []struct {
		model string
		set   []Override
	}{
		// Speed dial of the WP810/822/825: account index (0, 1) and number of
		// key 0, then keys 2 to 9 in steps of five.
		{"WP822", []Override{{Key: "P23001", Value: "0"}, {Key: "P23003", Value: "*97"}, {Key: "P23011", Value: "1"}, {Key: "P23013", Value: "2000"}, {Key: "P23046", Value: "0"}, {Key: "P23048", Value: "2009"}}},
		// Off-hook auto dial and its delay (WP810/822/825 and WP820), and the
		// wallpaper source (0 default, 1 download, 2 uploaded) and path.
		{"WP822", []Override{{Key: "P71", Value: "2001"}, {Key: "P8388", Value: "4"}, {Key: "P2916", Value: "1"}, {Key: "P2917", Value: "pics.example/wall.jpg"}}},
		{"WP820", []Override{{Key: "P71", Value: "2001"}, {Key: "P8388", Value: "10"}, {Key: "P2916", Value: "1"}, {Key: "P2917", Value: "pics.example/wall.jpg"}}},
		// VoWLAN target delay (0 low, 1 medium, 2 high) in both; Wi-Fi power
		// saving has different values per family: WP8x6 0 generic, 1 disabled,
		// 2 U-APSD; WP810/822/825 0 optimum, 1 relaxed fast-PS, 2 disabled
		// during calling, 3 disabled. The WP8x6 also has the Wi-Fi band (0 both,
		// 1 2.4G, 2 5G) and the side key (0 PTT/paging, 2 alarm, 4 none).
		{"WP836", []Override{{Key: "P82281", Value: "1"}, {Key: "P82269", Value: "2"}, {Key: "P25438", Value: "2"}, {Key: "P82317", Value: "2"}}},
		{"WP822", []Override{{Key: "P82281", Value: "2"}, {Key: "P82269", Value: "3"}}},
		// WP820 multi-function button: 3 = speed dial, with its account and number.
		{"WP820", []Override{{Key: "P22258", Value: "3"}, {Key: "P23051", Value: "1"}, {Key: "P23053", Value: "2002"}}},
		// WP856 PTT, left and right button: function (0 PTT, 1 mute, 3 speed
		// dial, 4 disable, 5 scan, 6 open the app), account (-1 dynamic) and value.
		{"WP856", []Override{{Key: "P82317", Value: "3"}, {Key: "P82318", Value: "-1"}, {Key: "P82319", Value: "2003"}, {Key: "P82321", Value: "5"}, {Key: "P82325", Value: "4"}}},
	} {
		d := gsFamilyData(c.model)
		for _, o := range c.set {
			if err := ValidateOverride(Grandstream, o.Key, o.Value); err != nil {
				t.Fatalf("%s %s refused: %v", c.model, o.Key, err)
			}
		}
		d.Overrides = ResolveOverrides(Grandstream, c.set, nil)
		got := renderDevice(t, Grandstream, d)
		for _, o := range c.set {
			want := "<" + o.Key + ">" + o.Value + "</" + o.Key + ">"
			if n := strings.Count(got, "<"+o.Key+">"); n != 1 || !strings.Contains(got, want) {
				t.Errorf("%s: want one %s, found %d", c.model, want, n)
			}
		}
	}
}
