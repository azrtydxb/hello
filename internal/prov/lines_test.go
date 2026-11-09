package prov

import "testing"

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
	t.Run("grandstream writes no unconfirmed account block", func(t *testing.T) {
		for _, model := range []string{"WP836", "WP822"} {
			d := linesSample(Grandstream, model, 2)
			out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			wantNoLines(t, model, out, "1002-aabbcc", "pw-two", "<P401>", "<P501>")
		}
	})
}

// TestMaxLines pins the line counts the spec gives (WP8x6 three accounts,
// WP822 two) and the one-line default of models without a confirmed count.
func TestMaxLines(t *testing.T) {
	for _, c := range []struct {
		v     Vendor
		model string
		want  int
	}{
		{Grandstream, "WP836", 3}, {Grandstream, "WP816", 3}, {Grandstream, "WP826", 3}, {Grandstream, "WP822", 2},
		{Grandstream, "WP825", 1}, {Grandstream, "WP856", 1}, {Grandstream, "GRP2614", 1},
		{Yealink, "T19P", 1}, {Yealink, "T21P_E2", 2}, {Yealink, "T54W", 6}, {Yealink, "T99", 6},
		{Snom, "D120", 2}, {Snom, "D785", 6}, {Poly, "VVX 450", 6}, {Fanvil, "X4U", 6}, {Generic, "x", 1},
	} {
		if got := MaxLines(c.v, c.model); got != c.want {
			t.Errorf("MaxLines(%s, %s) = %d, want %d", c.v, c.model, got, c.want)
		}
	}
	if SupportsLineKeys(Grandstream, "WP836") {
		t.Error("Grandstream line keys are not confirmed, but SupportsLineKeys says they are")
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
	wantNoLines(t, "off", render("WP836", NewSharing(Grandstream, "WP836", false, SharingHost)), "P22521", "P22522")
	wantNoLines(t, "WP822", render("WP822", NewSharing(Grandstream, "WP822", true, SharingHost)), "P22521", "P22522")
	wantNoLines(t, "unset", render("WP836", nil), "P22521", "P22522")
	wantLines(t, "host", render("WP836", NewSharing(Grandstream, "WP836", true, SharingHost)), "<P22521>1</P22521>", "<P22522>0</P22522>")
	wantLines(t, "guest", render("WP826", NewSharing(Grandstream, "WP826", true, SharingGuest)), "<P22521>0</P22521>", "<P22522>1</P22522>")
	if NewSharing(Yealink, "T54W", true, SharingHost) != nil {
		t.Error("a Yealink has account sharing")
	}
	if !ValidSharingRole(SharingGuest) || ValidSharingRole("owner") {
		t.Error("sharing roles are host and guest")
	}
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
