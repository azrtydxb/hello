package prov

import (
	"context"
	"strings"
	"testing"
)

func multicastSample(v Vendor, model string, mc, ptt bool) RenderData {
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.Phone.PTTCapable, d.Phone.WP = SupportsPTT(v, model), WPFamily(v, model)
	d.Paging = DefaultPagingOptions()
	if mc {
		d.Multicast = NewMulticastInfo([]MulticastChannel{
			{Label: "Office", Address: "239.255.0.2", Port: 5006, Codec: "PCMU", Priority: 2},
			{Label: "Warehouse", Address: "239.255.0.1", Port: 5004, Codec: "PCMU", Priority: 1},
			{Label: "Out", Address: "239.255.0.9", Port: 5010, Codec: "PCMU", Priority: 3},
		}, []string{"listen", "both", "send"})
		d.Multicast.PagingKey = SupportsPTT(v, model)
	}
	if ptt {
		d.PTT = NewPTTInfo(PTTSettings{Enabled: true, Address: "224.0.1.116:5001", Codec: "G722", DefaultChannel: 24,
			PriorityChannel: 2, EmergencyChannel: 25, AcceptBusy: true, DefaultGroup: 26, PriorityGroup: 49, EmergencyGroup: 50,
			Channels: []PTTChannel{
				{Channel: 1, Label: "All", Transmit: 1, Subscribe: 1},
				{Channel: 24, Label: "Priority", Transmit: 1, Subscribe: 0},
			}})
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
// paging keys (P22208, P1568 to P1588, P1566, P1567, P8381, P22532,
// P22556, P82317, P22177 to P22186 and the channel slots from P82001) are
// not written, if unused listening or channel slots are not cleared, or if
// a WP822 gets the WP8x6-only keys or lacks its PTT.
func TestGrandstreamPTTChannels(t *testing.T) {
	const file = "cfg805ec0aabbcc.xml"
	g := renderFor(t, Grandstream, file, multicastSample(Grandstream, "WP836", true, true))
	for _, w := range []string{
		"<P22208>1</P22208>", "<P1568>0</P1568>",
		"<P1569>239.255.0.1:5004</P1569>", "<P1570>Warehouse</P1570>",
		"<P1571>239.255.0.2:5006</P1571>", "<P1572>Office</P1572>",
		"<P1573></P1573>", "<P1588></P1588>",
		"<P1566>0</P1566>", "<P1567>1</P1567>", "<P8381>0</P8381>", "<P22532>0</P22532>", "<P26019>0</P26019>",
		"<P22556>1</P22556>", "<P82317>0</P82317>",
		"<P22177>1</P22177>", "<P22183>224.0.1.116:5001</P22183>", "<P22184>24</P22184>",
		"<P22185>2</P22185>", "<P22186>25</P22186>",
		"<P22179>1</P22179>", "<P22180>9</P22180>",
		"<P82001>1</P82001>", "<P82002>1</P82002>", "<P82003>1</P82003>", "<P82004>1</P82004>", "<P82005>All</P82005>",
		"<P82006>0</P82006>", "<P82010></P82010>",
		"<P82116>1</P82116>", "<P82118>0</P82118>", "<P82120>Priority</P82120>",
		"<P22194>0</P22194>",
	} {
		if !strings.Contains(g, w) {
			t.Errorf("WP836 rendering lacks %s", w)
		}
	}
	for _, k := range []string{"<P22556>", "<P82317>", "<P22183>", "<P26019>"} {
		if strings.Count(g, k) != 1 {
			t.Errorf("%s is not written exactly once", k)
		}
	}
	if strings.Contains(g, "<P82126>") || strings.Contains(g, "<P22187>") {
		t.Error("group paging off still writes group channel slots, or a WP836 gets the WP820 keys")
	}

	// WP8x6 with multicast paging but PTT off: the paging button is still on.
	m := renderFor(t, Grandstream, file, multicastSample(Grandstream, "WP826", true, false))
	if !strings.Contains(m, "<P22556>1</P22556>") || strings.Contains(m, "<P22177>") {
		t.Error("a WP826 with multicast only lacks the paging switch or carries PTT")
	}

	// WP822: multicast paging, P8381, and the PTT keys (unverified), but no
	// WP8x6-only switch, side key or timeout.
	w := renderFor(t, Grandstream, file, multicastSample(Grandstream, "WP822", true, true))
	for _, want := range []string{"<P22208>1</P22208>", "<P1569>239.255.0.1:5004</P1569>", "<P8381>0</P8381>", "<P26019>0</P26019>",
		"<P22177>1</P22177>", "<P82001>1</P82001>"} {
		if !strings.Contains(w, want) {
			t.Errorf("the WP822 lacks %s", want)
		}
	}
	for _, bad := range []string{"<P22556>", "<P82317>", "<P22532>", "<P26072>"} {
		if strings.Contains(w, bad) {
			t.Errorf("the WP822 carries the WP8x6-only key %s", bad)
		}
	}

	// A GXP: multicast and paging options, no PTT and no WP-only keys.
	x := renderFor(t, Grandstream, file, multicastSample(Grandstream, "GXP2170", true, false))
	if !strings.Contains(x, "<P1566>0</P1566>") || !strings.Contains(x, "<P26019>0</P26019>") {
		t.Error("a GXP2170 lacks the paging barge or intercom barge key")
	}
	for _, bad := range []string{"<P8381>", "<P22532>", "<P22556>", "<P22177>"} {
		if strings.Contains(x, bad) {
			t.Errorf("a GXP2170 carries the WP key %s", bad)
		}
	}
}

// TestGrandstreamWP856 fails if a WP856 (the audit's CALL-30) is not given
// PTT with its own keys: the side key P82317, P22177 and the channel slots,
// the priority and emergency channels, group paging P22194 to P22201 with
// the slots from P82126, and multicast paging; or if it gets the WP8x6-only
// P22556, P8381 or P22532.
func TestGrandstreamWP856(t *testing.T) {
	const file = "cfg805ec0aabbcc.xml"
	d := multicastSample(Grandstream, "WP856", true, true)
	d.PTT.GroupPaging = true
	d.PTT = NewPTTInfo(PTTSettings{Enabled: true, Address: "239.1.1.1:5001", Codec: "PCMU", DefaultChannel: 1,
		PriorityChannel: 24, EmergencyChannel: 25, GroupPaging: true, DefaultGroup: 27, PriorityGroup: 48, EmergencyGroup: 50,
		Channels: []PTTChannel{{Channel: 1, Label: "All", Transmit: 1, Subscribe: 1}, {Channel: 27, Label: "Floor", Transmit: 1, Subscribe: 1}}})
	d.Paging = &PagingOptions{AllowInDND: 1, Barge: 4, PriorityActive: 0, Timeout: 30, IntercomBarge: 1}
	g := renderFor(t, Grandstream, file, d)
	for _, w := range []string{
		"<P82317>0</P82317>", "<P22177>1</P22177>", "<P22183>239.1.1.1:5001</P22183>",
		"<P22184>1</P22184>", "<P22185>24</P22185>", "<P22186>25</P22186>", "<P22180>0</P22180>",
		"<P82001>1</P82001>", "<P82005>All</P82005>",
		"<P22194>1</P22194>", "<P22199>27</P22199>", "<P22200>48</P22200>", "<P22201>50</P22201>", "<P22196>0</P22196>", "<P22197>0</P22197>",
		"<P82126>0</P82126>", "<P82131>1</P82131>", "<P82132>1</P82132>", "<P82133>1</P82133>", "<P82134>1</P82134>", "<P82135>Floor</P82135>",
		"<P82250></P82250>",
		"<P22208>1</P22208>", "<P1566>4</P1566>", "<P1567>0</P1567>", "<P26019>1</P26019>", "<P26072>1</P26072>",
	} {
		if !strings.Contains(g, w) {
			t.Errorf("WP856 rendering lacks %s", w)
		}
	}
	for _, bad := range []string{"<P22556>", "<P8381>", "<P22532>", "<P82251>"} {
		if strings.Contains(g, bad) {
			t.Errorf("the WP856 carries %s", bad)
		}
	}
	if strings.Count(g, "<P22183>") != 1 {
		t.Error("the PTT/group paging address is written twice")
	}

	// A WP836 with the same options writes the WP8x6-only ones.
	d.Phone.Model, d.Phone.WP = "WP836", WPFamily(Grandstream, "WP836")
	x := renderFor(t, Grandstream, file, d)
	for _, w := range []string{"<P8381>1</P8381>", "<P22532>30</P22532>", "<P22556>1</P22556>"} {
		if !strings.Contains(x, w) {
			t.Errorf("WP836 rendering lacks %s", w)
		}
	}
}

// TestGrandstreamWP820 fails if a WP820 is not given its PTT and group
// paging channels as digit strings (P22187 to P22192, P22203 to P22207)
// with its own codec numbers, or gets the P82001 slots.
func TestGrandstreamWP820(t *testing.T) {
	const file = "cfg805ec0aabbcc.xml"
	d := multicastSample(Grandstream, "WP820", false, false)
	d.PTT = NewPTTInfo(PTTSettings{Enabled: true, Address: "224.0.1.116:5001", Codec: "G722", DefaultChannel: 2,
		PriorityChannel: 24, EmergencyChannel: 25, GroupPaging: true, DefaultGroup: 26, PriorityGroup: 49, EmergencyGroup: 50,
		Channels: []PTTChannel{{Channel: 2, Label: "Shop^floor", Transmit: 1, Subscribe: 0}, {Channel: 25, Label: "SOS", Transmit: 1, Subscribe: 1},
			{Channel: 26, Label: "All", Transmit: 1, Subscribe: 1}}})
	g := renderFor(t, Grandstream, file, d)
	for _, w := range []string{
		"<P22177>1</P22177>", "<P22180>1</P22180>", "<P22184>2</P22184>",
		"<P22187>0100000000000000000000001</P22187>", "<P22188>0100000000000000000000001</P22188>",
		"<P22189>0100000000000000000000001</P22189>", "<P22190>0000000000000000000000001</P22190>",
		"<P22192>^Shop floor^^^^^^^^^^^^^^^^^^^^^^^SOS</P22192>",
		"<P22194>1</P22194>", "<P22197>1</P22197>", "<P22203>1000000000000000000000000</P22203>", "<P22207>All^^^^^^^^^^^^^^^^^^^^^^^^</P22207>",
	} {
		if !strings.Contains(g, w) {
			t.Errorf("WP820 rendering lacks %s", w)
		}
	}
	if strings.Contains(g, "<P82001>") || strings.Contains(g, "<P82317>") {
		t.Error("the WP820 gets the WP8x6 slots or side key")
	}
}

// TestGrandstreamPTTDefault fails if a WP8x6 with Hello's default PTT (no
// group, no catalog channel) is not given the PTT/paging switch, the side
// key on PTT, PTT on Grandstream's default 224.0.1.116:5001 in PCMU with
// channel 1 available for transmit and subscribe and the priority and
// emergency channels 24 and 25, group paging off, and intercom barging
// off; or if a WP8x6 whose group switched PTT off is not given P22177=0
// without the paging switch.
func TestGrandstreamPTTDefault(t *testing.T) {
	const file = "cfg805ec0aabbcc.xml"
	d := multicastSample(Grandstream, "WP836", false, false)
	d.PTT = NewPTTInfo(PTTSettings{Enabled: true, Address: "224.0.1.116:5001", Codec: "PCMU", DefaultChannel: 1,
		PriorityChannel: 24, EmergencyChannel: 25, DefaultGroup: 26, PriorityGroup: 49, EmergencyGroup: 50,
		Channels: []PTTChannel{{Channel: 1, Label: "Channel 1", Transmit: 1, Subscribe: 1}}})
	g := renderFor(t, Grandstream, file, d)
	for _, w := range []string{
		"<P22556>1</P22556>", "<P82317>0</P82317>",
		"<P22177>1</P22177>", "<P22183>224.0.1.116:5001</P22183>", "<P22184>1</P22184>",
		"<P22185>24</P22185>", "<P22186>25</P22186>",
		"<P22179>0</P22179>", "<P22180>0</P22180>",
		"<P82001>1</P82001>", "<P82002>1</P82002>", "<P82003>1</P82003>", "<P82004>1</P82004>", "<P82005>Channel 1</P82005>",
		"<P82006>0</P82006>", "<P22194>0</P22194>", "<P26019>0</P26019>",
	} {
		if !strings.Contains(g, w) {
			t.Errorf("default WP836 PTT lacks %s", w)
		}
	}

	d.PTT = &PTTInfo{}
	off := renderFor(t, Grandstream, file, d)
	if !strings.Contains(off, "<P22177>0</P22177>") || !strings.Contains(off, "<P22194>0</P22194>") {
		t.Error("PTT off is not written as P22177=0 and P22194=0")
	}
	for _, bad := range []string{"<P22556>", "<P22183>", "<P82001>"} {
		if strings.Contains(off, bad) {
			t.Errorf("PTT off still carries %s", bad)
		}
	}
}

// TestSupportsPTT fails if a Grandstream WP is not given the PTT keys, or
// another phone is, or a model gets the wrong WP family.
func TestSupportsPTT(t *testing.T) {
	for model, want := range map[string]string{"WP816": WP8x6, "WP826": WP8x6, "WP836": WP8x6, "wp 836": WP8x6,
		"WP856": WP856, "WP822": WP8xx, "WP810": WP8xx, "WP825": WP8xx, "WP820": WP820, "GXP1630": "", "GRP2614": ""} {
		if got := WPFamily(Grandstream, model); got != want {
			t.Errorf("WPFamily(%q) = %q, want %q", model, got, want)
		}
		if got := SupportsPTT(Grandstream, model); got != (want != "") {
			t.Errorf("SupportsPTT(%q) = %v", model, got)
		}
	}
	if SupportsPTT(Yealink, "WP836") {
		t.Error("a Yealink is given the Grandstream PTT keys")
	}
}

// TestYealinkIntercomBarge fails if the group's intercom barging option
// does not reach features.intercom.barge, or a phone without options does
// not get it off.
func TestYealinkIntercomBarge(t *testing.T) {
	d := multicastSample(Yealink, "T54W", false, false)
	d.Paging = &PagingOptions{IntercomBarge: 1}
	if y := renderFor(t, Yealink, "805ec0aabbcc.cfg", d); !strings.Contains(y, "features.intercom.barge = 1") {
		t.Error("intercom barging on is not written")
	}
	d.Paging = nil
	if y := renderFor(t, Yealink, "805ec0aabbcc.cfg", d); !strings.Contains(y, "features.intercom.barge = 0") {
		t.Error("intercom barging is not off by default")
	}
}
