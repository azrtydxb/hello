package prov

import (
	"context"
	"strings"
	"testing"
)

func TestNewCodecInfo(t *testing.T) {
	for _, c := range []struct {
		v     Vendor
		model string
		opus  bool
	}{
		{Yealink, "T54W", true},
		{Yealink, "SIP-T46S", true},
		{Yealink, "T42U", true},
		{Yealink, "T31P", false},
		{Yealink, "T23G", false},
		{Grandstream, "GRP2614", true},
		{Grandstream, "WP822", true},
		{Grandstream, "GXP2170", true},
		{Grandstream, "GXP1405", false},
		{Grandstream, "GXP2100", false},
		{Snom, "D785", true},
		{Snom, "snomD717", true},
		{Snom, "D315", false},
		{Fanvil, "X6U", true},
		{Fanvil, "X210", true},
		{Fanvil, "X3S", false},
		{Poly, "VVX 450", true},
		{Poly, "Poly VVX 250", true},
		{Poly, "SoundPoint IP 331", false},
		{Generic, "T54W", false},
		{Yealink, "", false},
	} {
		if got := NewCodecInfo(c.v, c.model).Opus; got != c.opus {
			t.Errorf("NewCodecInfo(%s, %q).Opus = %v, want %v", c.v, c.model, got, c.opus)
		}
	}
}

// renderCodecs renders the built-in file of v that carries the codec keys.
func renderCodecs(t *testing.T, v Vendor, model string) string {
	t.Helper()
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.Codecs = NewCodecInfo(v, model)
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

// inOrder reports whether the needles appear in text in the given order.
func inOrder(text string, needles ...string) bool {
	at := 0
	for _, n := range needles {
		i := strings.Index(text[at:], n)
		if i < 0 {
			return false
		}
		at += i + len(n)
	}
	return true
}

// TestBuiltinCodecOrder fails if a vendor template stops putting G.722 first
// (then Opus where the model has it, then PCMU and PCMA) with the vendor's
// own keys.
func TestBuiltinCodecOrder(t *testing.T) {
	t.Run("yealink opus model", func(t *testing.T) {
		out := renderCodecs(t, Yealink, "T54W")
		for _, want := range []string{
			"account.1.codec.g722.enable = 1\naccount.1.codec.g722.priority = 1\n",
			"account.1.codec.opus.enable = 1\naccount.1.codec.opus.priority = 2\n",
			"account.1.codec.pcmu.enable = 1\naccount.1.codec.pcmu.priority = 3\n",
			"account.1.codec.pcma.enable = 1\naccount.1.codec.pcma.priority = 4\n",
			"account.1.codec.g729.enable = 0\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})
	t.Run("yealink without opus", func(t *testing.T) {
		out := renderCodecs(t, Yealink, "T31P")
		if !strings.Contains(out, "account.1.codec.opus.enable = 0\n") ||
			!strings.Contains(out, "account.1.codec.pcmu.priority = 2\n") ||
			!strings.Contains(out, "account.1.codec.pcma.priority = 3\n") ||
			strings.Contains(out, "account.1.codec.opus.priority") {
			t.Errorf("codec keys:\n%s", out)
		}
	})
	t.Run("grandstream", func(t *testing.T) {
		out := renderCodecs(t, Grandstream, "GRP2614")
		// 9 G.722, 123 Opus, 0 PCMU, 8 PCMA, then the last codec repeated.
		if !inOrder(out, "<P57>9</P57>", "<P58>123</P58>", "<P59>0</P59>", "<P60>8</P60>", "<P61>8</P61>", "<P62>8</P62>") {
			t.Errorf("Grandstream choices:\n%s", out)
		}
		old := renderCodecs(t, Grandstream, "GXP1405")
		if !inOrder(old, "<P57>9</P57>", "<P58>0</P58>", "<P59>8</P59>") || strings.Contains(old, "<P58>123") {
			t.Errorf("Grandstream choices without Opus:\n%s", old)
		}
	})
	t.Run("snom", func(t *testing.T) {
		if out := renderCodecs(t, Snom, "D785"); !strings.Contains(out, `>g722,opus,pcmu,pcma,telephone-event</codec_priority_list>`) {
			t.Errorf("Snom codec list:\n%s", out)
		}
		if out := renderCodecs(t, Snom, "D315"); !strings.Contains(out, `>g722,pcmu,pcma,telephone-event</codec_priority_list>`) {
			t.Errorf("Snom codec list without Opus:\n%s", out)
		}
	})
	t.Run("fanvil", func(t *testing.T) {
		if out := renderCodecs(t, Fanvil, "X6U"); !strings.Contains(out, "SIP1 Voice Codec Map :G722,opus,G711U,G711A\n") {
			t.Errorf("Fanvil codec map:\n%s", out)
		}
		if out := renderCodecs(t, Fanvil, "X3S"); !strings.Contains(out, "SIP1 Voice Codec Map :G722,G711U,G711A\n") {
			t.Errorf("Fanvil codec map without Opus:\n%s", out)
		}
	})
	t.Run("poly", func(t *testing.T) {
		out := renderCodecs(t, Poly, "VVX 450")
		if !inOrder(out, `voice.codecPref.G722="1"`, `voice.codecPref.Opus="2"`, `voice.codecPref.G711_Mu="3"`, `voice.codecPref.G711_A="4"`) {
			t.Errorf("Poly codec preferences:\n%s", out)
		}
		old := renderCodecs(t, Poly, "SoundPoint IP 331")
		if !inOrder(old, `voice.codecPref.G722="1"`, `voice.codecPref.G711_Mu="2"`, `voice.codecPref.G711_A="3"`) || strings.Contains(old, "codecPref.Opus") {
			t.Errorf("Poly codec preferences without Opus:\n%s", old)
		}
	})
}
