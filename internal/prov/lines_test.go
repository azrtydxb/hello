package prov

import (
	"strconv"
	"testing"
)

// linesSample is a two-extension sample: line 1 is extension 1001, lines 2
// and 3 are extensions 1002 and 1003.
func linesSample(v Vendor, model string, n int) RenderData {
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.BLF = d.BLF[:1]
	d.Codecs = CodecInfo{Opus: true}
	extra := []Line{
		{Username: "1002-aabbcc", AuthName: "1002-aabbcc", Password: "pw-two", DisplayName: "Carol", Label: "1002", CallWaiting: true},
		{Username: "1003-aabbcc", AuthName: "1003-aabbcc", Password: "pw-three", DisplayName: "Dave", Label: "1003"},
	}
	d.Lines = extra[:n]
	return d
}

func linesFile(v Vendor, d RenderData) string {
	switch v {
	case Yealink, Fanvil:
		return d.Phone.MAC + ".cfg"
	case Poly:
		return d.Phone.MAC + "-hello.cfg"
	case Grandstream:
		return "cfg" + d.Phone.MAC + ".xml"
	}
	return d.Phone.MACUpper
}

// TestMultiLineProvisioning fails if lines 2 and 3 do not render their
// account keys in every vendor template that writes them, if a line is
// written for a model that cannot hold it (the WP822 has two lines, so a
// third is not written), if BLF keys land on the keys the lines use, or if
// account sharing P22521 and P22522 is rendered for a group that did not
// enable it, for a model without it, or with the wrong role.
func TestMultiLineProvisioning(t *testing.T) {
	t.Run("yealink", func(t *testing.T) {
		out := renderFor(t, Yealink, "805ec0aabbcc.cfg", linesSample(Yealink, "T54W", 2))
		wantLines(t, "yealink", out,
			"account.2.enable = 1", "account.2.user_name = 1002-aabbcc", "account.2.auth_name = 1002-aabbcc",
			"account.2.password = pw-two", "account.2.label = 1002 (Carol)", "account.2.sip_server.1.address = 192.168.10.101",
			"account.2.subscribe_mwi = 1", "account.2.codec.opus.priority = 1", "account.2.feature_key_sync.enable = 1",
			"account.3.enable = 1", "account.3.user_name = 1003-aabbcc", "account.3.password = pw-three",
			"voice_mail.number.2 = *97", "voice_mail.number.3 = *97",
			// Line keys 1 to 3 are the accounts, so BLF starts at key 4.
			"linekey.4.value = 1002",
		)
		wantNoLines(t, "yealink", out, "account.4.", "linekey.2.value", "linekey.3.value")
	})
	t.Run("poly", func(t *testing.T) {
		out := renderFor(t, Poly, "805ec0aabbcc-hello.cfg", linesSample(Poly, "VVX 450", 2))
		wantLines(t, "poly", out,
			`reg.2.address="1002-aabbcc"`, `reg.2.auth.password="pw-two"`, `reg.2.label="1002 (Carol)"`, `reg.2.server.1.address="192.168.10.101"`,
			`msg.mwi.2.subscribe="1002-aabbcc"`, `reg.3.address="1003-aabbcc"`, `msg.mwi.3.callBack="*97"`)
		wantNoLines(t, "poly", out, "reg.4.")
	})
	t.Run("snom", func(t *testing.T) {
		out := renderFor(t, Snom, "805EC0AABBCC", linesSample(Snom, "D785", 2))
		wantLines(t, "snom", out,
			`<user_active idx="2" perm="R">on</user_active>`, `<user_name idx="2" perm="R">1002-aabbcc</user_name>`,
			`<user_pass idx="2" perm="R">pw-two</user_pass>`, `<user_idle_text idx="2" perm="R">1002 (Carol)</user_idle_text>`,
			`<user_mailbox idx="2" perm="R">*97</user_mailbox>`, `<call_waiting idx="2" perm="R">on</call_waiting>`,
			`<user_name idx="3" perm="R">1003-aabbcc</user_name>`, `<call_waiting idx="3" perm="R">off</call_waiting>`,
			// Identities are keys 0 to 2, so BLF starts at key 3.
			`<fkey idx="3" `)
		wantNoLines(t, "snom", out, `idx="4" perm="R">on`, `<fkey idx="1" `, `<fkey idx="2" `)
	})
	t.Run("fanvil", func(t *testing.T) {
		out := renderFor(t, Fanvil, "805ec0aabbcc.cfg", linesSample(Fanvil, "X4U", 2))
		wantLines(t, "fanvil", out,
			"SIP2 Phone Number  :1002-aabbcc", "SIP2 Register Pswd :pw-two", "SIP2 Sip Name      :1002 (Carol)", "SIP2 Enable Reg    :1",
			"SIP2 Subscribe     :1", "SIP2 MWI Num       :*97", "SIP3 Phone Number  :1003-aabbcc", "P2 CallWaiting     :1", "P3 CallWaiting     :0")
		wantNoLines(t, "fanvil", out, "SIP4 ")
	})
	t.Run("a one-line phone renders no further line", func(t *testing.T) {
		for _, c := range []struct {
			v     Vendor
			model string
		}{{Yealink, "T54W"}, {Poly, "VVX 450"}, {Snom, "D785"}, {Fanvil, "X4U"}} {
			d := linesSample(c.v, c.model, 0)
			out := renderFor(t, c.v, linesFile(c.v, d), d)
			wantNoLines(t, string(c.v), out, "1002-aabbcc", "pw-two")
		}
	})
	t.Run("a model with one line key takes no second line", func(t *testing.T) {
		d := linesSample(Yealink, "T19P", 2)
		out := renderFor(t, Yealink, linesFile(Yealink, d), d)
		wantNoLines(t, "T19", out, "account.2.", "1002-aabbcc")
	})
	// Account blocks per Grandstream's own templates (wp8x6_config_1.0.3.39
	// accounts 2 and 3, wp810_822_825_config_1.0.11.83 and wp820_config_1.0.7.90
	// account 2, wp856_config_1.0.3.16 accounts 2 to 6): active, label, server,
	// user, auth ID, password, name, voicemail number and expiry, MWI, vocoders,
	// Broadsoft special feature with feature key sync, accept-from-proxy-only.
	t.Run("grandstream wp8x6", func(t *testing.T) {
		out := renderFor(t, Grandstream, linesFile(Grandstream, linesSample(Grandstream, "WP836", 2)), linesSample(Grandstream, "WP836", 2))
		wantLines(t, "wp836", out,
			"<P401>1</P401>", "<P417>1002 (Carol)</P417>", "<P402>192.168.10.101:30508</P402>", "<P404>1002-aabbcc</P404>", "<P405>1002-aabbcc</P405>",
			"<P406>pw-two</P406>", "<P407>Carol</P407>", "<P426>*97</P426>", "<P412>60</P412>", "<P415>1</P415>", "<P448>0</P448>",
			"<P451>123</P451>", "<P452>9</P452>", "<P424>102</P424>", "<P2425>1</P2425>", "<P2447>1</P2447>", "<P2497>1</P2497>", "<P26163>1</P26163>",
			"<P501>1</P501>", "<P517>1003 (Dave)</P517>", "<P504>1003-aabbcc</P504>", "<P506>pw-three</P506>", "<P524>102</P524>", "<P2525>1</P2525>",
			"<P2547>1</P2547>", "<P2597>1</P2597>", "<P26263>2</P26263>", "<P548>0</P548>")
		wantNoLines(t, "wp836", out, "<P601>")
	})
	t.Run("grandstream wp822 and wp820", func(t *testing.T) {
		for _, model := range []string{"WP822", "WP825", "WP820"} {
			d := linesSample(Grandstream, model, 2)
			out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			wantLines(t, model, out, "<P401>1</P401>", "<P404>1002-aabbcc</P404>", "<P406>pw-two</P406>", "<P2447>1</P2447>", "<P424>102</P424>")
			// A WP822 takes two lines: the third is not written.
			wantNoLines(t, model, out, "1003-aabbcc", "pw-three", "<P501>", "<P2497>")
		}
		// The WP820 template has no per-account call waiting key (P91 is phone-wide).
		wp820 := linesSample(Grandstream, "WP820", 2)
		wantNoLines(t, "WP820", renderFor(t, Grandstream, linesFile(Grandstream, wp820), wp820), "<P26163>")
	})
	t.Run("grandstream wp856 six lines", func(t *testing.T) {
		d := linesSample(Grandstream, "WP856", 2)
		for i := 4; i <= 6; i++ {
			n := strconv.Itoa(i)
			d.Lines = append(d.Lines, Line{Username: "100" + n + "-aabbcc", AuthName: "100" + n + "-aabbcc", Password: "pw-" + n, DisplayName: "Line" + n, Label: "100" + n})
		}
		d.Lines = append(d.Lines, Line{Username: "1007-aabbcc", AuthName: "1007-aabbcc", Password: "pw-7", DisplayName: "Line7", Label: "1007"})
		out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
		wantLines(t, "wp856", out,
			"<P401>1</P401>", "<P404>1002-aabbcc</P404>", "<P26163>2</P26163>", // account 2 has call waiting on; the WP856 reads the key the other way round
			"<P501>1</P501>", "<P504>1003-aabbcc</P504>", "<P26263>1</P26263>",
			"<P601>1</P601>", "<P604>1004-aabbcc</P604>", "<P606>pw-4</P606>", "<P2647>1</P2647>",
			"<P1701>1</P1701>", "<P1704>1005-aabbcc</P1704>", "<P1705>1005-aabbcc</P1705>", "<P1706>pw-5</P1706>", "<P1717>1005 (Line5)</P1717>",
			"<P1724>102</P1724>", "<P2725>1</P2725>", "<P2747>1</P2747>", "<P1748>0</P1748>",
			"<P1801>1</P1801>", "<P1804>1006-aabbcc</P1804>", "<P1806>pw-6</P1806>", "<P1807>Line6</P1807>", "<P2847>1</P2847>")
		wantNoLines(t, "wp856", out, "1007-aabbcc", "pw-7", "<P2701>", "<P1901>")
	})
	t.Run("grandstream account keys follow the transport", func(t *testing.T) {
		d := linesSample(Grandstream, "WP836", 2)
		d.Server.Transport = "tls"
		wantLines(t, "tls", renderFor(t, Grandstream, linesFile(Grandstream, d), d), "<P448>2</P448>", "<P2429>1</P2429>", "<P548>2</P548>", "<P2529>1</P2529>")
		d.Server.Transport = "tcp"
		out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
		wantLines(t, "tcp", out, "<P448>1</P448>", "<P548>1</P548>")
		wantNoLines(t, "tcp", out, "<P2429>")
	})
	t.Run("grandstream line without voicemail code keeps the default", func(t *testing.T) {
		d := linesSample(Grandstream, "WP836", 1)
		d.Line.VoicemailCode = ""
		wantLines(t, "default", renderFor(t, Grandstream, linesFile(Grandstream, d), d), "<P426>*97</P426>")
	})
}

// TestMaxLines pins the line counts: the Grandstream ones from the account
// blocks of the vendor's own templates (WP8x6 three, WP810/820/822/825 two,
// WP856 six), and the one-line default of models without a known count.
func TestMaxLines(t *testing.T) {
	for _, c := range []struct {
		v     Vendor
		model string
		want  int
	}{
		{Grandstream, "WP836", 3}, {Grandstream, "WP816", 3}, {Grandstream, "WP826", 3}, {Grandstream, "WP822", 2},
		{Grandstream, "WP825", 2}, {Grandstream, "WP810", 2}, {Grandstream, "WP820", 2}, {Grandstream, "WP856", 6},
		{Grandstream, "GRP2614", 1}, {Grandstream, "HT813", 1},
		{Yealink, "T19P", 1}, {Yealink, "T21P_E2", 2}, {Yealink, "T54W", 6}, {Yealink, "T99", 6},
		{Snom, "D120", 2}, {Snom, "D785", 6}, {Poly, "VVX 450", 6}, {Fanvil, "X4U", 6}, {Generic, "x", 1},
	} {
		if got := MaxLines(c.v, c.model); got != c.want {
			t.Errorf("MaxLines(%s, %s) = %d, want %d", c.v, c.model, got, c.want)
		}
	}
	for _, model := range []string{"WP836", "WP822", "WP856", "WP820"} {
		if !SupportsLineKeys(Grandstream, model) {
			t.Errorf("%s takes more than one line, but SupportsLineKeys is false", model)
		}
	}
	for _, model := range []string{"GRP2614", "HT813"} {
		if SupportsLineKeys(Grandstream, model) {
			t.Errorf("%s takes one line, but SupportsLineKeys is true", model)
		}
	}
}

// TestAccountSharing fails if P22521 and P22522 are written for a group that
// did not enable account sharing, for a model other than the WP8x6, or with
// the wrong role.
func TestAccountSharing(t *testing.T) {
	render := func(model string, sh *SharingInfo) string {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Grandstream, model
		d.Sharing = sh
		return renderFor(t, Grandstream, "cfg"+d.Phone.MAC+".xml", d)
	}
	// P22521 Enable Account Sharing (0 No, 1 Yes) and P22522 Role in Account
	// Sharing (0 Host Device, 1 Guest Device) are WP8x6 keys in
	// wp8x6_config_1.0.3.39; no other WP template has them. A WP8x6 outside a
	// sharing group is written P22521 = 0 so leaving the group turns it off.
	for name, out := range map[string]string{
		"off":   render("WP836", NewSharing(Grandstream, "WP836", false, SharingHost)),
		"unset": render("WP836", nil),
	} {
		wantLines(t, name, out, "<P22521>0</P22521>")
		wantNoLines(t, name, out, "P22522")
	}
	for _, model := range []string{"WP822", "WP856", "WP820"} {
		wantNoLines(t, model, render(model, NewSharing(Grandstream, model, true, SharingHost)), "P22521", "P22522")
	}
	host := render("WP836", NewSharing(Grandstream, "WP836", true, SharingHost))
	wantLines(t, "host", host, "<P22521>1</P22521>", "<P22522>0</P22522>")
	// A guest is enabled too: P22521 = 1 with the guest role, not 0.
	guest := render("WP826", NewSharing(Grandstream, "WP826", true, SharingGuest))
	wantLines(t, "guest", guest, "<P22521>1</P22521>", "<P22522>1</P22522>")
	wantNoLines(t, "guest", guest, "<P22521>0</P22521>")
	if NewSharing(Yealink, "T54W", true, SharingHost) != nil {
		t.Error("a Yealink has account sharing")
	}
	if !ValidSharingRole(SharingGuest) || ValidSharingRole("owner") {
		t.Error("sharing roles are host and guest")
	}
}

// TestAccountSharingPassword fails if P22523 (Group Password, default
// AccountSharing in wp8x6_config_1.0.3.39) is left at the shared default, is
// written without a group secret, is XML-unsafe, or is written to a model
// other than the WP8x6.
func TestAccountSharingPassword(t *testing.T) {
	render := func(model string, sh *SharingInfo) string {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Grandstream, model
		d.Sharing = sh
		return renderFor(t, Grandstream, "cfg"+d.Phone.MAC+".xml", d)
	}
	withPW := func(model, pw string) *SharingInfo {
		sh := NewSharing(Grandstream, model, true, SharingHost)
		if sh != nil {
			sh.Password = pw
		}
		return sh
	}
	out := render("WP836", withPW("WP836", "k3y<&>Zq9"))
	wantLines(t, "secret", out, "<P22523>k3y&lt;&amp;&gt;Zq9</P22523>")
	wantNoLines(t, "secret", out, "AccountSharing")
	// No secret, no key: the phone is never given the shared default.
	none := render("WP836", withPW("WP836", ""))
	wantNoLines(t, "empty", none, "P22523", "AccountSharing")
	for _, model := range []string{"WP822", "WP856", "WP820"} {
		wantNoLines(t, model, render(model, withPW(model, "x")), "P22523")
	}
	wantNoLines(t, "off", render("WP836", nil), "P22523")
}

// TestPlaceBLFLines fails if BLF keys land on the keys the lines use.
func TestPlaceBLFLines(t *testing.T) {
	keys := []BLFKey{{Number: "1"}, {Number: "2"}}
	if got := PlaceBLFLines(Yealink, "T54W", keys, "", 3); got[0].Slot != 4 || got[1].Slot != 5 {
		t.Errorf("yealink slots %d,%d, want 4,5", got[0].Slot, got[1].Slot)
	}
	if got := PlaceBLFLines(Yealink, "T54W", keys, "", 1); got[0].Slot != 2 {
		t.Errorf("one-line yealink slot %d, want 2", got[0].Slot)
	}
	if got := PlaceBLFLines(Snom, "D785", keys, "", 2); got[0].Slot != 2 {
		t.Errorf("snom slot %d, want 2", got[0].Slot)
	}
	if got := PlaceBLFLines(Yealink, "T99X", keys, "", 3); got[0].Slot != 4 {
		t.Errorf("unknown yealink slot %d, want 4", got[0].Slot)
	}
	if got := PlaceBLFLines(Fanvil, "X4U", keys, "", 3); got[0].Slot != 1 {
		t.Errorf("fanvil slot %d, want 1: its line keys are not DSS keys", got[0].Slot)
	}
	if vm := PlaceVMKeyLines(Yealink, "T54W", 2, "*97", "1001", "d", 3); vm == nil || vm.Slot != 6 {
		t.Errorf("voicemail key %+v, want slot 6", vm)
	}
}
