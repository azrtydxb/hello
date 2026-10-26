package phonebook

import (
	"strings"
	"testing"
)

func ch(label, addr string, port int, codec, kind string, prio int) ChannelInput {
	return ChannelInput{Label: label, Address: addr, Port: port, Codec: codec, Kind: kind, Priority: prio}
}

// TestValidateChannel fails if an address outside the IPv4 multicast range
// or inside 224.0.0.0/24, a port outside 1024 to 65535, a codec the kind
// does not take, an unknown kind or a priority outside 1 to 25 is accepted,
// or if administratively scoped addresses, the top of the range and
// Grandstream's PTT default address are refused.
func TestValidateChannel(t *testing.T) {
	for name, in := range map[string]ChannelInput{
		"scoped":          ch("Dock", "239.255.0.1", 5004, CodecPCMU, KindPaging, 1),
		"g722 paging":     ch("Two", "239.192.1.1", 6000, CodecG722, KindPaging, 25),
		"pcma paging":     ch("Three", "239.192.1.2", 6000, CodecPCMA, KindPaging, 2),
		"top of range":    ch("Edge", "239.255.255.255", 65535, CodecPCMU, KindPaging, 1),
		"ptt default":     ch("PTT", "224.0.1.116", 5001, CodecPCMU, KindPTT, 1),
		"ptt g722":        ch("PTT HD", "239.1.1.1", 5001, CodecG722, KindPTT, 1),
		"bottom of ports": ch("Low", "239.1.1.1", 1024, CodecPCMU, KindPaging, 1),
	} {
		if f := ValidateChannel(in); len(f) != 0 {
			t.Errorf("%s refused: %v", name, f)
		}
	}
	for name, tc := range map[string]struct {
		in   ChannelInput
		path string
	}{
		"unicast":     {ch("a", "10.0.0.1", 5004, CodecPCMU, KindPaging, 1), "address"},
		"reserved":    {ch("a", "224.0.0.251", 5004, CodecPCMU, KindPaging, 1), "address"},
		"ipv6":        {ch("a", "ff02::1", 5004, CodecPCMU, KindPaging, 1), "address"},
		"text":        {ch("a", "lunch", 5004, CodecPCMU, KindPaging, 1), "address"},
		"low port":    {ch("a", "239.1.1.1", 1023, CodecPCMU, KindPaging, 1), "port"},
		"high port":   {ch("a", "239.1.1.1", 65536, CodecPCMU, KindPaging, 1), "port"},
		"codec":       {ch("a", "239.1.1.1", 5004, "OPUS", KindPaging, 1), "codec"},
		"ptt pcma":    {ch("a", "239.1.1.1", 5004, CodecPCMA, KindPTT, 1), "codec"},
		"kind":        {ch("a", "239.1.1.1", 5004, CodecPCMU, "talk", 1), "kind"},
		"priority 0":  {ch("a", "239.1.1.1", 5004, CodecPCMU, KindPaging, 0), "priority"},
		"priority 26": {ch("a", "239.1.1.1", 5004, CodecPCMU, KindPaging, 26), "priority"},
		"label":       {ch(" a", "239.1.1.1", 5004, CodecPCMU, KindPaging, 1), "label"},
		"long label":  {ch(strings.Repeat("a", 41), "239.1.1.1", 5004, CodecPCMU, KindPaging, 1), "label"},
	} {
		f := ValidateChannel(tc.in)
		if len(f) == 0 || f[0].Path != tc.path {
			t.Errorf("%s: %v, want a failure at %s", name, f, tc.path)
		}
	}
}

// TestValidateMulticast fails if a group's channel list with a missing or
// repeated channel id, an unknown role or an eleventh channel is accepted,
// or if an empty list or a good one is refused.
func TestValidateMulticast(t *testing.T) {
	for name, tc := range map[string][]MulticastInput{
		"empty": nil,
		"two":   {{ChannelID: 1, Role: RoleBoth}, {ChannelID: 2, Role: RoleListen}},
		"send":  {{ChannelID: 3, Role: RoleSend}},
	} {
		if f := ValidateMulticast(tc); len(f) != 0 {
			t.Errorf("%s refused: %v", name, f)
		}
	}
	for name, tc := range map[string]struct {
		in   []MulticastInput
		path string
	}{
		"no id":     {[]MulticastInput{{Role: RoleBoth}}, "channels[0].channelId"},
		"negative":  {[]MulticastInput{{ChannelID: -1, Role: RoleBoth}}, "channels[0].channelId"},
		"duplicate": {[]MulticastInput{{ChannelID: 1, Role: RoleBoth}, {ChannelID: 1, Role: RoleListen}}, "channels[1].channelId"},
		"role":      {[]MulticastInput{{ChannelID: 1, Role: "talk"}}, "channels[0].role"},
		"eleven": {func() []MulticastInput {
			var l []MulticastInput
			for i := range 11 {
				l = append(l, MulticastInput{ChannelID: int64(i + 1), Role: RoleBoth})
			}
			return l
		}(), "channels"},
	} {
		f := ValidateMulticast(tc.in)
		if len(f) == 0 || f[0].Path != tc.path {
			t.Errorf("%s: %v, want a failure at %s", name, f, tc.path)
		}
	}
}

// TestValidatePTT fails if a channel number outside 1 to 50 or used twice,
// a missing label, a default channel the phone does not take or that cannot
// transmit, a priority or emergency channel outside 1 to 25, or, with group
// paging on, a default group outside 26, 27, 49 and 50 or one that is not a
// transmitting group channel, or a priority or emergency group outside 26 to
// 50 is accepted; or if a good setting, PTT off, or PTT or group paging on
// with no channels of its kind (the store defines the default one) is
// refused.
func TestValidatePTT(t *testing.T) {
	good := DefaultPTT()
	good.GroupPaging = true
	good.Channels = []PTTChannel{{Channel: 1, Label: "All", Transmit: true, Subscribe: true}, {Channel: 24, Label: "Priority", Transmit: true},
		{Channel: 26, Label: "Floor", Transmit: true, Subscribe: true}}
	if f := ValidatePTT(good); len(f) != 0 {
		t.Fatalf("good PTT refused: %v", f)
	}
	if f := ValidatePTT(PTT{DefaultChannel: 7}); len(f) != 0 {
		t.Errorf("PTT off refused: %v", f)
	}
	on := DefaultPTT()
	on.DefaultChannel, on.GroupPaging = 2, true
	if f := ValidatePTT(on); len(f) != 0 {
		t.Errorf("PTT and group paging on with no channels refused: %v", f)
	}
	for name, mutate := range map[string]func(p *PTT){
		"channel 0":          func(p *PTT) { p.Channels[0].Channel = 0 },
		"channel 51":         func(p *PTT) { p.Channels[1].Channel = 51 },
		"twice":              func(p *PTT) { p.Channels[1].Channel = 1 },
		"default":            func(p *PTT) { p.DefaultChannel = 5 },
		"no transmit":        func(p *PTT) { p.Channels[0].Transmit = false },
		"missing":            func(p *PTT) { p.DefaultChannel = 2 },
		"label":              func(p *PTT) { p.Channels[0].Label = "" },
		"priority 26":        func(p *PTT) { p.PriorityChannel = 26 },
		"emergency 0":        func(p *PTT) { p.EmergencyChannel = 0 },
		"default group 28":   func(p *PTT) { p.DefaultGroup = 28 },
		"group missing":      func(p *PTT) { p.DefaultGroup = 27 },
		"group no transmit":  func(p *PTT) { p.Channels[2].Transmit = false },
		"priority group 25":  func(p *PTT) { p.PriorityGroup = 25 },
		"emergency group 51": func(p *PTT) { p.EmergencyGroup = 51 },
	} {
		p := good
		p.Channels = append([]PTTChannel(nil), good.Channels...)
		mutate(&p)
		if f := ValidatePTT(p); len(f) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestValidatePagingOptions fails if a barge priority outside 0 to 10 or a
// timeout outside 0 to 3600 seconds is accepted, or the defaults refused.
func TestValidatePagingOptions(t *testing.T) {
	if f := ValidatePagingOptions(DefaultPagingOptions()); len(f) != 0 {
		t.Fatalf("defaults refused: %v", f)
	}
	if f := ValidatePagingOptions(PagingOptions{Barge: 10, Timeout: 3600, AllowInDND: true, IntercomBarge: true}); len(f) != 0 {
		t.Errorf("limits refused: %v", f)
	}
	for _, bad := range []PagingOptions{{Barge: -1}, {Barge: 11}, {Timeout: -1}, {Timeout: 3601}} {
		if f := ValidatePagingOptions(bad); len(f) == 0 {
			t.Errorf("%+v accepted", bad)
		}
	}
}
