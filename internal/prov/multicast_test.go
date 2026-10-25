package prov

import (
	"context"
	"strings"
	"testing"
)

func multicastSample(v Vendor, model string, mc, ptt bool) RenderData {
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	if mc {
		d.Multicast = NewMulticastInfo([]MulticastChannel{
			{Label: "Office", Address: "239.255.0.2", Port: 5006, Codec: "PCMU", Priority: 2},
			{Label: "Warehouse", Address: "239.255.0.1", Port: 5004, Codec: "PCMU", Priority: 1},
			{Label: "Out", Address: "239.255.0.9", Port: 5010, Codec: "PCMU", Priority: 3},
		}, []string{"listen", "both", "send"})
		d.Multicast.PagingKey = SupportsPTT(v, model)
	}
	if ptt {
		d.PTT = NewPTTInfo("224.0.1.116:5001", "G722", 24, true, []PTTChannel{
			{Channel: 1, Label: "All", Transmit: 1, Subscribe: 1},
			{Channel: 24, Label: "Priority", Transmit: 1, Subscribe: 0},
		})
	}
	return d
}

func renderFor(t *testing.T, v Vendor, file string, d RenderData) string {
	t.Helper()
	b, err := Render(context.Background(), builtinFor(t, v), file, d)
	if err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	return string(b)
}

// TestMulticastProvisioning fails if a group's channels do not render each
// vendor's listen and send keys in priority order with the group's codec,
// if a send-only channel is listened on or a listen-only channel is sent
// to, or if a phone without channels gets any multicast key.
func TestMulticastProvisioning(t *testing.T) {
	y := renderFor(t, Yealink, "805ec0aabbcc.cfg", multicastSample(Yealink, "T54W", true, false))
	for _, w := range []string{
		"multicast.codec = PCMU", "multicast.receive_priority.enable = 1",
		"multicast.listen_address.1.ip_address = 239.255.0.1:5004", "multicast.listen_address.1.label = Warehouse",
		"multicast.listen_address.2.ip_address = 239.255.0.2:5006",
		"multicast.paging_address.1.ip_address = 239.255.0.1:5004", "multicast.paging_address.2.ip_address = 239.255.0.9:5010",
	} {
		if !strings.Contains(y, w) {
			t.Errorf("yealink lacks %q", w)
		}
	}
	for _, bad := range []string{"multicast.listen_address.3.", "239.255.0.9:5010\nmulticast.listen", "multicast.paging_address.3."} {
		if strings.Contains(y, bad) {
			t.Errorf("yealink carries %q", bad)
		}
	}
	if strings.Contains(y, "multicast.listen_address.2.ip_address = 239.255.0.9") {
		t.Error("yealink listens on a send-only channel")
	}

	p := renderFor(t, Poly, "805ec0aabbcc-hello.cfg", multicastSample(Poly, "VVX 450", true, false))
	for _, w := range []string{`ptt.pageMode.enable="1"`, `ptt.pageMode.address="239.255.0.1"`, `ptt.pageMode.port="5004"`, `ptt.pageMode.group.2.label="Office"`} {
		if !strings.Contains(p, w) {
			t.Errorf("poly lacks %q", w)
		}
	}

	for _, v := range []struct {
		v    Vendor
		file string
	}{{Yealink, "805ec0aabbcc.cfg"}, {Poly, "805ec0aabbcc-hello.cfg"}, {Grandstream, "cfg805ec0aabbcc.xml"}} {
		out := renderFor(t, v.v, v.file, multicastSample(v.v, "WP836", false, false))
		for _, bad := range []string{"multicast.", "ptt.", "P22208", "P22177", "P22556", "P1569"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: a phone without channels carries %q", v.v, bad)
			}
		}
	}
}

// TestGrandstreamPTTChannels fails if a WP8x6 group's PTT and multicast
// paging keys (P22208, P1568 to P1588, P22556, P82317, P22177 to P22180 and
// the channel slots from P82001) are not written, if unused listening or
// channel slots are not cleared, or if a WP822 gets the WP8x6-only keys.
func TestGrandstreamPTTChannels(t *testing.T) {
	const file = "cfg805ec0aabbcc.xml"
	g := renderFor(t, Grandstream, file, multicastSample(Grandstream, "WP836", true, true))
	for _, w := range []string{
		"<P22208>1</P22208>", "<P1568>0</P1568>",
		"<P1569>239.255.0.1:5004</P1569>", "<P1570>Warehouse</P1570>",
		"<P1571>239.255.0.2:5006</P1571>", "<P1572>Office</P1572>",
		"<P1573></P1573>", "<P1588></P1588>",
		"<P22556>1</P22556>", "<P82317>0</P82317>",
		"<P22177>1</P22177>", "<P22183>224.0.1.116:5001</P22183>", "<P22184>24</P22184>",
		"<P22179>1</P22179>", "<P22180>9</P22180>",
		"<P82001>1</P82001>", "<P82002>1</P82002>", "<P82003>1</P82003>", "<P82004>1</P82004>", "<P82005>All</P82005>",
		"<P82006>0</P82006>", "<P82010></P82010>",
		"<P82116>1</P82116>", "<P82118>0</P82118>", "<P82120>Priority</P82120>",
	} {
		if !strings.Contains(g, w) {
			t.Errorf("WP836 rendering lacks %s", w)
		}
	}
	if strings.Count(g, "<P22556>") != 1 || strings.Count(g, "<P82317>") != 1 {
		t.Error("the PTT/paging switch or side key is written twice")
	}

	// WP8x6 with multicast paging but PTT off: the paging button is still on.
	m := renderFor(t, Grandstream, file, multicastSample(Grandstream, "WP826", true, false))
	if !strings.Contains(m, "<P22556>1</P22556>") || strings.Contains(m, "<P22177>") {
		t.Error("a WP826 with multicast only lacks the paging switch or carries PTT")
	}

	// WP822: multicast paging only.
	w := renderFor(t, Grandstream, file, multicastSample(Grandstream, "WP822", true, false))
	if !strings.Contains(w, "<P22208>1</P22208>") || !strings.Contains(w, "<P1569>239.255.0.1:5004</P1569>") {
		t.Error("the WP822 lacks multicast paging")
	}
	for _, bad := range []string{"<P22556>", "<P82317>", "<P22177>", "<P82001>"} {
		if strings.Contains(w, bad) {
			t.Errorf("the WP822 carries the WP8x6-only key %s", bad)
		}
	}
}

// TestSupportsPTT fails if a model outside the WP816, WP826 and WP836 is
// given the PTT keys.
func TestSupportsPTT(t *testing.T) {
	for model, want := range map[string]bool{"WP816": true, "WP826": true, "WP836": true, "wp 836": true, "WP822": false, "WP810": false, "GXP1630": false} {
		if got := SupportsPTT(Grandstream, model); got != want {
			t.Errorf("SupportsPTT(%q) = %v, want %v", model, got, want)
		}
	}
	if SupportsPTT(Yealink, "WP836") {
		t.Error("a Yealink is given the Grandstream PTT keys")
	}
}
