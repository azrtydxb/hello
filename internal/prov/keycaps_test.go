package prov

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

// renderFor renders a built-in vendor file for a model with n BLF keys and
// the given star codes. The sample carries test credentials only.
func renderKeys(t *testing.T, v Vendor, model, file string, n int, codes FeatureCodes) string {
	t.Helper()
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.Codes = codes
	d.BLF = nil
	for i := range n {
		num := fmt.Sprint(2000 + i)
		d.BLF = append(d.BLF, BLFKey{Number: num, Label: "User " + num, URI: "sip:" + num + "@hello.test"})
	}
	out, err := Render(context.Background(), builtinFor(t, v), file, d)
	if err != nil {
		t.Fatalf("%s %s: %v", v, file, err)
	}
	return string(out)
}

var testCodes = NewFeatureCodes([]FeatureCodeRow{
	{"*72", "forward_always", "set"}, {"*73", "forward_always", "clear"},
	{"*78", "dnd_on", ""}, {"*79", "dnd_off", ""},
	{"*90", "forward_busy", "set"}, {"*91", "forward_busy", "clear"},
	{"*92", "forward_no_answer", "set"}, {"*93", "forward_no_answer", "clear"},
	{"*97", "voicemail", ""}, {"*41", "pickup_directed", ""},
})

// TestKeyCapacity fails if the capability table gives a model a different
// BLF capacity than its vendor document: Snom from the Service Hub's
// "Function Key Index Ranges" (keys on the phone minus the account's line
// key, plus the first 18 keys of expansion module 1), Yealink from the
// linekey range minus the account's key (plus three 60-key EXP50 on the
// T53, T54, T57 and T58), Fanvil from the datasheet DSS pages.
func TestKeyCapacity(t *testing.T) {
	for _, tc := range []struct {
		v     Vendor
		model string
		want  int
		known bool
	}{
		{Snom, "D120", 1, true},
		{Snom, "D713", 3, true},
		{Snom, "D715", 4 + 18, true},
		{Snom, "snomD785", 23 + 18, true},
		{Snom, "D345", 47 + 18, true},
		{Snom, "D865", 39, true},
		{Snom, "D999", 0, false},
		{Yealink, "T31G", 1, true},
		{Yealink, "T19", 0, true},
		{Yealink, "SIP-T46U", 26, true},
		{Yealink, "T48S", 28, true},
		{Yealink, "T54W", 26 + 3*60, true},
		{Yealink, "T57W", 28 + 3*60, true},
		{Yealink, "T99", 0, false},
		{Fanvil, "X4U", 30, true},
		{Fanvil, "X5U-R", 30, true},
		{Fanvil, "X6U", 60, true},
		{Fanvil, "X7", 0, false},
		{Poly, "VVX 450", 0, false},
		{Grandstream, "GRP2614", 0, false},
	} {
		n, known := KeyCapacity(tc.v, tc.model)
		if known != tc.known || (known && n != tc.want) {
			t.Errorf("%s %s: capacity %d (known %v), want %d (known %v)", tc.v, tc.model, n, known, tc.want, tc.known)
		}
	}
}

// TestSnomBLFPastLineKey fails if Snom's first BLF lands on fkey idx 0, the
// phone's first line key (the account), or if keys past the model's last
// key are written, or the expansion module keys are not at the Service Hub
// index (D785: module 1 starts at 24; D345: at 174).
func TestSnomBLFPastLineKey(t *testing.T) {
	idxOf := func(out string) []string {
		var got []string
		for line := range strings.SplitSeq(out, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), `<fkey idx="`); ok {
				idx, _, _ := strings.Cut(rest, `"`)
				got = append(got, idx)
			}
		}
		return got
	}
	for _, tc := range []struct {
		model string
		n     int
		want  []string
	}{
		{"D785", 3, []string{"1", "2", "3"}},
		{"D120", 3, []string{"1"}},                       // two keys, the first is the account
		{"D785", 25, append(seq(1, 23), "24", "25")},     // phone keys, then module 1
		{"D785", 99, append(seq(1, 23), seq(24, 41)...)}, // capped at module 1's 18 keys
		{"D345", 49, append(seq(1, 47), "174", "175")},   // D3xx modules are not contiguous
		{"D713", 9, []string{"1", "2", "3"}},             // no expansion module
	} {
		out := renderKeys(t, Snom, tc.model, "805EC0AABBCC", tc.n, FeatureCodes{})
		got := idxOf(out)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s with %d BLF: fkey idx %v, want %v", tc.model, tc.n, got, tc.want)
		}
		if strings.Contains(out, `<fkey idx="0"`) {
			t.Errorf("%s: a BLF sits on fkey 0, the account's line key", tc.model)
		}
	}
}

func seq(from, to int) []string {
	var s []string
	for i := from; i <= to; i++ {
		s = append(s, fmt.Sprint(i))
	}
	return s
}

// TestYealinkBLFCap fails if Yealink BLF keys are written past the model's
// line keys (a T31 has two: linekey.1 is the account, so one BLF), if the
// overflow of an EXP50 phone does not go to expansion_module keys, if the
// directed-pickup code is not set on the BLF line keys (and only when the
// PBX has one), or if an unknown model stops getting every key.
func TestYealinkBLFCap(t *testing.T) {
	out := renderKeys(t, Yealink, "T31G", "805ec0aabbcc.cfg", 5, testCodes)
	if !strings.Contains(out, "linekey.2.value = 2000") || strings.Contains(out, "linekey.3.") || strings.Contains(out, "expansion_module") {
		t.Errorf("T31G with 5 BLF:\n%s", out)
	}
	if strings.Contains(out, "linekey.1.") {
		t.Error("a BLF took linekey.1, the account's key")
	}
	if !strings.Contains(out, "linekey.2.pickup_value = *41") {
		t.Error("the BLF line key has no pickup_value")
	}
	if out := renderKeys(t, Yealink, "T31G", "805ec0aabbcc.cfg", 1, FeatureCodes{}); strings.Contains(out, "pickup_value") {
		t.Error("pickup_value written without a pickup code in the PBX")
	}

	out = renderKeys(t, Yealink, "T54W", "805ec0aabbcc.cfg", 28, testCodes)
	for _, want := range []string{
		"linekey.27.value = 2025", "linekey.27.pickup_value = *41",
		"expansion_module.1.key.1.type = 16", "expansion_module.1.key.1.value = 2026",
		"expansion_module.1.key.2.value = 2027",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("T54W with 28 BLF lacks %q", want)
		}
	}
	if strings.Contains(out, "linekey.28.") || strings.Contains(out, "expansion_module.1.key.1.pickup") || strings.Contains(out, "expansion_module.1.key.1.extension") {
		t.Error("T54W: a key past linekey.27 on the phone, or a pickup value on an expansion key")
	}
	// A T46 has no expansion module in the table: 27 keys, 26 BLF.
	out = renderKeys(t, Yealink, "T46U", "805ec0aabbcc.cfg", 40, FeatureCodes{})
	if !strings.Contains(out, "linekey.27.value = 2025") || strings.Contains(out, "linekey.28.") || strings.Contains(out, "expansion_module") {
		t.Error("T46U is not capped at linekey.27")
	}
	// A model the table does not know keeps every key, from linekey.2.
	out = renderKeys(t, Yealink, "T99X", "805ec0aabbcc.cfg", 40, FeatureCodes{})
	if !strings.Contains(out, "linekey.41.value = 2039") || strings.Contains(out, "linekey.1.") {
		t.Error("an unknown Yealink model is capped or starts at linekey.1")
	}
}

// TestFanvilBLFPages fails if Fanvil BLF keys are not written under the
// DSSKEY module in "--Dsskey Config<page>--:" sections of the model's page
// size (X5U: 6 per page, 5 pages), or run past the last page.
func TestFanvilBLFPages(t *testing.T) {
	out := renderKeys(t, Fanvil, "X5U", "805ec0aabbcc.cfg", 8, FeatureCodes{})
	for _, want := range []string{
		"<DSSKEY CONFIG MODULE>", "--Dsskey Config1--:", "--Dsskey Config2--:",
		"Fkey6 Value       :2005@1/b", "Fkey1 Value       :2006@1/b", "Fkey2 Value       :2007@1/b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("X5U with 8 BLF lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<DSS CONFIG MODULE>") || strings.Contains(out, "Config3--") {
		t.Error("X5U: the old module name, or a third page")
	}
	out = renderKeys(t, Fanvil, "X5U", "805ec0aabbcc.cfg", 40, FeatureCodes{})
	if !strings.Contains(out, "--Dsskey Config5--:") || strings.Contains(out, "Config6--") || strings.Contains(out, ":2030@") {
		t.Error("X5U is not capped at 30 keys on 5 pages")
	}
	if out := renderKeys(t, Fanvil, "X5U", "805ec0aabbcc.cfg", 0, FeatureCodes{}); strings.Contains(out, "DSSKEY") {
		t.Error("an empty DSSKEY module is written without BLF keys")
	}
	// An unknown model keeps every key, on page 1.
	out = renderKeys(t, Fanvil, "X999", "805ec0aabbcc.cfg", 10, FeatureCodes{})
	if !strings.Contains(out, "Fkey10 Value       :2009@1/b") || strings.Contains(out, "Config2--") {
		t.Error("an unknown Fanvil model is capped or paged")
	}
}

// TestSnomWebPasswordAndCA fails if the Snom device file does not set the
// web UI user and password from the phone's admin password (http_user and
// http_pass for the D3xx/D7xx, webserver_admin_password for the D86x/D89x),
// or does not deliver the CA through <certificates> (device file and boot
// file) as the base64 text of the certificate, or writes the element without
// a CA.
func TestSnomWebPasswordAndCA(t *testing.T) {
	const caPEM = "-----BEGIN CERTIFICATE-----\nZm9v\nYmFy\n-----END CERTIFICATE-----"
	type settings struct {
		User  string `xml:"phone-settings>http_user"`
		Pass  string `xml:"phone-settings>http_pass"`
		Admin string `xml:"phone-settings>webserver_admin_password"`
		PIN   string `xml:"phone-settings>admin_mode_password"`
		Certs []struct {
			Type string `xml:"type,attr"`
			Body string `xml:",chardata"`
		} `xml:"certificates>certificate"`
	}
	parse := func(body []byte) settings {
		t.Helper()
		var s settings
		if err := xml.Unmarshal(body, &s); err != nil {
			t.Fatalf("snom: %v\n%s", err, body)
		}
		return s
	}
	for _, withCA := range []bool{true, false} {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Snom, "D785"
		if withCA {
			d.Prov.CACertPEM = caPEM
		}
		dev, err := Render(context.Background(), builtinFor(t, Snom), d.Phone.MACUpper, d)
		if err != nil {
			t.Fatal(err)
		}
		boot, err := renderBody(context.Background(), bootBodies[Snom].common, RenderData{Phone: d.Phone, Prov: d.Prov})
		if err != nil {
			t.Fatal(err)
		}
		s := parse(dev)
		if s.User != "admin" || s.Pass != d.Phone.AdminPassword || s.Admin != d.Phone.AdminPassword || s.PIN != d.Phone.AdminPassword {
			t.Errorf("snom web login = %q / %q / %q (PIN %q), want admin and the phone's admin password", s.User, s.Pass, s.Admin, s.PIN)
		}
		for name, body := range map[string][]byte{"device": dev, "boot": boot} {
			certs := parse(body).Certs
			if !withCA {
				if len(certs) != 0 || strings.Contains(string(body), "<certificates>") {
					t.Errorf("snom %s: a certificates element without a CA", name)
				}
				continue
			}
			if len(certs) != 1 || certs[0].Type != "base64" {
				t.Fatalf("snom %s: certificates = %+v", name, certs)
			}
			der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(certs[0].Body))
			if err != nil || string(der) != "foobar" {
				t.Errorf("snom %s: certificate body %q decodes to %q, %v", name, certs[0].Body, der, err)
			}
			// Inside <settings>, outside <phone-settings>.
			if i, j := strings.Index(string(body), "</phone-settings>"), strings.Index(string(body), "<certificates>"); j < i {
				t.Errorf("snom %s: <certificates> is inside <phone-settings>", name)
			}
		}
	}
}

// TestFanvilMWIIntercomRecord fails if the Fanvil file does not subscribe
// to MWI (SIP1 Subscribe with its period), does not enable intercom
// auto-answer with the tone, no mute and no barge (as Yealink's
// features.intercom.*), does not set the web admin password under
// --MMI Account--, or carries the old Web User List.
func TestFanvilMWIIntercomRecord(t *testing.T) {
	out := renderKeys(t, Fanvil, "X5U", "805ec0aabbcc.cfg", 0, FeatureCodes{})
	for _, want := range []string{
		"SIP1 Subscribe     :1", "SIP1 Sub Expire    :3600", "SIP1 MWI Num       :*97",
		"<CALL FEATURE MODULE>", "P1 Enable Intercom :1", "P1 Intercom Mute   :0", "P1 Intercom Tone   :1", "P1 Intercom Barge  :0",
		"<MMI CONFIG MODULE>", "--MMI Account--    :", "Account1 Name      :admin", "Account1 Password  :adm<&>\"'n",
		"SIP1 Record Start  :Record:on", "SIP1 Record Stop   :Record:off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fanvil lacks %q", want)
		}
	}
	for _, bad := range []string{"AdminPswd", "Web User List", "WEB CONFIG MODULE", "SIP1 Auto Answer", "SvcCode Mode", "Pickup Num"} {
		if strings.Contains(out, bad) {
			t.Errorf("fanvil carries %q", bad)
		}
	}
}

// TestSnomFanvilStarCodes fails if the Snom and Fanvil files do not carry
// the PBX's DND and forward star codes (Snom dnd_*_code and fwd_*_code,
// Fanvil SIP1 SvcCode Mode with the DND, CFU, CFB and CFN codes), the
// directed pickup (Fanvil SIP1 Pickup Num), or carry any of them when the
// PBX has none (Fanvil would then dial empty codes from its keys).
func TestSnomFanvilStarCodes(t *testing.T) {
	snom := renderKeys(t, Snom, "D785", "805EC0AABBCC", 0, testCodes)
	for _, want := range []string{
		`<dnd_on_code perm="R">*78</dnd_on_code>`, `<dnd_off_code perm="R">*79</dnd_off_code>`,
		`<fwd_all_on_code idx="1" perm="R">*72</fwd_all_on_code>`, `<fwd_all_off_code idx="1" perm="R">*73</fwd_all_off_code>`,
		`<fwd_busy_on_code idx="1" perm="R">*90</fwd_busy_on_code>`, `<fwd_busy_off_code idx="1" perm="R">*91</fwd_busy_off_code>`,
		`<fwd_time_on_code idx="1" perm="R">*92</fwd_time_on_code>`, `<fwd_time_off_code idx="1" perm="R">*93</fwd_time_off_code>`,
		`<recording_mechanism perm="R">SIP</recording_mechanism>`,
	} {
		if !strings.Contains(snom, want) {
			t.Errorf("snom lacks %q", want)
		}
	}
	fv := renderKeys(t, Fanvil, "X5U", "805ec0aabbcc.cfg", 0, testCodes)
	for _, want := range []string{
		"SIP1 SvcCode Mode  :1", "SIP1 DNDOn SvcCode :*78", "SIP1 DNDOff SvcCode :*79",
		"SIP1 CFUOn SvcCode :*72", "SIP1 CFUOff SvcCode :*73", "SIP1 CFBOn SvcCode :*90", "SIP1 CFBOff SvcCode :*91",
		"SIP1 CFNOn SvcCode :*92", "SIP1 CFNOff SvcCode :*93", "SIP1 Pickup Num    :*41",
	} {
		if !strings.Contains(fv, want) {
			t.Errorf("fanvil lacks %q", want)
		}
	}
	for _, v := range []struct {
		v     Vendor
		model string
		file  string
	}{{Snom, "D785", "805EC0AABBCC"}, {Fanvil, "X5U", "805ec0aabbcc.cfg"}} {
		out := renderKeys(t, v.v, v.model, v.file, 0, FeatureCodes{})
		for _, bad := range []string{"_on_code", "_off_code", "SvcCode", "Pickup Num"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s with no PBX codes carries %q", v.v, bad)
			}
		}
	}
}

// TestNewFeatureCodes fails if the star codes are not picked by action and
// mode, or depend on the order of the rows.
func TestNewFeatureCodes(t *testing.T) {
	rows := []FeatureCodeRow{
		{"*79", "dnd_off", ""}, {"*78", "dnd_on", ""}, {"*99", "dnd_on", ""},
		{"*72", "forward_always", "set"}, {"*73", "forward_always", "clear"},
		{"*90", "forward_busy", "set"}, {"*92", "forward_no_answer", "clear"},
		{"*41", "pickup_directed", ""}, {"*97", "voicemail", ""},
	}
	want := FeatureCodes{DNDOn: "*78", DNDOff: "*79", ForwardOn: "*72", ForwardOff: "*73", ForwardBusyOn: "*90", ForwardNoAnswerOff: "*92", PickupDirected: "*41"}
	if got := NewFeatureCodes(rows); got != want {
		t.Errorf("codes = %+v, want %+v", got, want)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if got := NewFeatureCodes(rows); got != want {
		t.Errorf("reversed rows: codes = %+v, want %+v", got, want)
	}
}

// TestPEMBase64 fails if a PEM block is not reduced to its base64 text.
func TestPEMBase64(t *testing.T) {
	for in, want := range map[string]string{
		"-----BEGIN CERTIFICATE-----\nAAAA\nBBBB\n-----END CERTIFICATE-----": "AAAABBBB",
		"-----BEGIN CERTIFICATE-----\r\nAAAA\r\n-----END CERTIFICATE-----\n": "AAAA",
		"":                                    "",
		"not a certificate":                   "",
		"-----BEGIN CERTIFICATE-----\nAAAA\n": "",
	} {
		if got := pemBase64(in); got != want {
			t.Errorf("pemBase64(%q) = %q, want %q", in, got, want)
		}
	}
}
