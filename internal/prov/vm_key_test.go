package prov

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// renderVM renders a vendor file for an extension 1001 (voicemail code *97)
// with n BLF keys, editing the data first when edit is set.
func renderVM(t *testing.T, v Vendor, model, file string, n int, edit func(*RenderData)) string {
	t.Helper()
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.BLF = nil
	for i := range n {
		num := fmt.Sprint(2000 + i)
		d.BLF = append(d.BLF, BLFKey{Number: num, Label: "User " + num, URI: "sip:" + num + "@hello.test"})
	}
	if edit != nil {
		edit(&d)
	}
	out, err := Render(context.Background(), builtinFor(t, v), file, d)
	if err != nil {
		t.Fatalf("%s %s: %v", v, file, err)
	}
	return string(out)
}

// TestTransferVoicemailKey fails if a phone with a key to spare does not get
// a key that transfers a call to *97<own extension> (the phone's own mailbox)
// on the first key after its BLF keys, with the vendor's key syntax; if the
// key displaces a BLF key, or is written for a phone with no key left, an
// unknown model, no voicemail code, or an extension without voicemail.
func TestTransferVoicemailKey(t *testing.T) {
	const yea, snom, fan = "805ec0aabbcc.cfg", "805EC0AABBCC", "805ec0aabbcc.cfg"
	has := func(what, out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s lacks %q:\n%s", what, w, out)
			}
		}
	}
	lacks := func(what, out string, not ...string) {
		t.Helper()
		for _, w := range not {
			if strings.Contains(out, w) {
				t.Errorf("%s carries %q", what, w)
			}
		}
	}

	// Yealink: line keys 2 and 3 hold the BLF keys, the transfer key is key 4.
	out := renderVM(t, Yealink, "T54W", yea, 2, nil)
	has("T54W", out,
		"linekey.2.value = 2000", "linekey.3.value = 2001",
		"linekey.4.type = 3", "linekey.4.value = *971001", "linekey.4.label = To voicemail")
	lacks("T54W", out, "linekey.5.")
	// No BLF: the first key after the account's.
	has("T54W, no BLF", renderVM(t, Yealink, "T54W", yea, 0, nil), "linekey.2.type = 3", "linekey.2.value = *971001")
	// BLF keys filling the phone leave the key to the expansion module; a
	// phone that has nothing left gets none.
	has("T54W, body full", renderVM(t, Yealink, "T54W", yea, 26, nil),
		"linekey.27.value = 2025", "expansion_module.1.key.1.type = 3", "expansion_module.1.key.1.value = *971001")
	lacks("T46U, full", renderVM(t, Yealink, "T46U", yea, 26, nil), "= *971001")
	// T31G has one key besides the account's: a BLF keeps it.
	has("T31G, no BLF", renderVM(t, Yealink, "T31G", yea, 0, nil), "linekey.2.value = *971001")
	lacks("T31G, one BLF", renderVM(t, Yealink, "T31G", yea, 1, nil), "*971001")
	lacks("T19 (no spare key)", renderVM(t, Yealink, "T19", yea, 0, nil), "*971001")
	lacks("unknown Yealink model", renderVM(t, Yealink, "T99", yea, 0, nil), "*971001")

	// Snom: fkey 0 is the account, 1 and 2 are BLF, the transfer key is 3.
	out = renderVM(t, Snom, "D785", snom, 2, nil)
	has("D785", out, `<fkey idx="3" context="active" label="To voicemail" perm="R">transfer sip:*971001@hello.test</fkey>`)
	has("D785, full body", renderVM(t, Snom, "D785", snom, 23, nil), `<fkey idx="24" context="active" label="To voicemail"`)
	lacks("D120 (one key, a BLF)", renderVM(t, Snom, "D120", snom, 1, nil), "*971001")
	lacks("unknown Snom model", renderVM(t, Snom, "D999", snom, 0, nil), "*971001")

	// Fanvil: 6 keys a page; the seventh key opens page 2.
	out = renderVM(t, Fanvil, "X5U", fan, 6, nil)
	has("X5U", out, "--Dsskey Config2--:", "Fkey1 Type        :1", "Fkey1 Value       :*971001@1/f", "Fkey1 Title       :To voicemail")
	out = renderVM(t, Fanvil, "X5U", fan, 2, nil)
	has("X5U, page 1", out, "--Dsskey Config1--:", "Fkey3 Value       :*971001@1/f")
	if strings.Count(out, "Config1--:") != 1 {
		t.Errorf("X5U writes the page header more than once:\n%s", out)
	}
	has("X5U, no BLF", renderVM(t, Fanvil, "X5U", fan, 0, nil), "<DSSKEY CONFIG MODULE>", "--Dsskey Config1--:", "Fkey1 Value       :*971001@1/f")
	lacks("X5U, 30 BLF", renderVM(t, Fanvil, "X5U", fan, 30, nil), "*971001")
	lacks("unknown Fanvil model", renderVM(t, Fanvil, "X999", fan, 0, nil), "*971001")

	// Poly: a softkey running an enhanced feature key macro.
	out = renderVM(t, Poly, "VVX 450", "805ec0aabbcc-hello.cfg", 2, nil)
	has("Poly", out, `efk.efklist.1.action.string="$FTransfer$$Cpause2$*971001$Tinvite$"`,
		`softkey.1.action="!hellovm"`, `feature.enhancedFeatureKeys.enabled="1"`)

	// Nothing without the code, and nothing for an extension without voicemail.
	noCode := func(d *RenderData) { d.Line.VoicemailCode = "" }
	noBox := func(d *RenderData) { d.Line.NoVoicemail = true }
	for _, c := range []struct {
		v           Vendor
		model, file string
	}{
		{Yealink, "T54W", yea}, {Snom, "D785", snom}, {Fanvil, "X5U", fan}, {Poly, "VVX 450", "805ec0aabbcc-hello.cfg"},
	} {
		lacks(fmt.Sprint(c.v, " without a voicemail code"), renderVM(t, c.v, c.model, c.file, 0, noCode), "971001", "hellovm", "To voicemail")
		lacks(fmt.Sprint(c.v, " without voicemail"), renderVM(t, c.v, c.model, c.file, 0, noBox), "971001", "hellovm", "To voicemail")
	}

	// Grandstream: no WP has keys, and the other models' key block is not extended.
	for _, m := range []string{"WP822", "WP836", "GRP2614"} {
		lacks("Grandstream "+m, renderVM(t, Grandstream, m, "cfg805ec0aabbcc.xml", 0, nil), "971001", "To voicemail")
	}
}

// TestPlaceVMKey fails if the key lands on a BLF key's slot, is placed when
// the model has no slot left, or is built from anything but the voicemail
// code, the extension and the domain.
func TestPlaceVMKey(t *testing.T) {
	for _, tc := range []struct {
		v     Vendor
		model string
		nBLF  int
		slot  int
		page  int
		mod   int
		none  bool
	}{
		{Yealink, "T54W", 0, 2, 0, 0, false},
		{Yealink, "T54W", 5, 7, 0, 0, false},
		{Yealink, "T54W", 26, 1, 0, 1, false}, // expansion module 1, key 1
		{Yealink, "T54W", 206, 0, 0, 0, true}, // body and three modules full
		{Snom, "D713", 3, 0, 0, 0, true},      // 4 keys, fkey 0 the account: 3 BLF fill it
		{Snom, "D713", 2, 3, 0, 0, false},
		{Fanvil, "X6U", 12, 1, 2, 0, false},
		{Fanvil, "X6U", 60, 0, 0, 0, true},
		{Yealink, "T19", 0, 0, 0, 0, true},
	} {
		k := PlaceVMKey(tc.v, tc.model, tc.nBLF, "*97", "1001", "pbx.test")
		if tc.none {
			if k != nil {
				t.Errorf("%s %s with %d BLF: got a key %+v", tc.v, tc.model, tc.nBLF, *k)
			}
			continue
		}
		if k == nil {
			t.Errorf("%s %s with %d BLF: no key", tc.v, tc.model, tc.nBLF)
			continue
		}
		if k.Slot != tc.slot || k.Page != tc.page || k.Module != tc.mod {
			t.Errorf("%s %s with %d BLF: slot %d page %d module %d, want %d %d %d", tc.v, tc.model, tc.nBLF, k.Slot, k.Page, k.Module, tc.slot, tc.page, tc.mod)
		}
		if k.Number != "*971001" || k.URI != "sip:*971001@pbx.test" {
			t.Errorf("%s %s: number %q uri %q", tc.v, tc.model, k.Number, k.URI)
		}
	}
	// The slot never collides with a placed BLF key.
	for _, v := range []struct {
		v     Vendor
		model string
	}{{Yealink, "T54W"}, {Snom, "D785"}, {Fanvil, "X5U"}} {
		for n := range 40 {
			var keys []BLFKey
			for i := range n {
				keys = append(keys, BLFKey{Number: fmt.Sprint(i)})
			}
			placed := PlaceBLF(v.v, v.model, keys, "")
			k := PlaceVMKey(v.v, v.model, len(placed), "*97", "1001", "d")
			if k == nil {
				continue
			}
			for _, b := range placed {
				if b.Slot == k.Slot && b.Module == k.Module && b.Page == k.Page {
					t.Errorf("%s %s with %d BLF: transfer key on BLF slot %d", v.v, v.model, n, b.Slot)
				}
			}
		}
	}
	if PlaceVMKey(Yealink, "T54W", 0, "", "1001", "d") != nil || PlaceVMKey(Yealink, "T54W", 0, "*97", "", "d") != nil {
		t.Error("a key without a code or an extension")
	}
	for _, v := range []Vendor{Grandstream} {
		if SupportsVMKey(v, "WP836") || SupportsVMKey(v, "GXP2130") {
			t.Errorf("%s supports the key", v)
		}
	}
}
