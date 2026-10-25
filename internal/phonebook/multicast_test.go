package phonebook

import (
	"strings"
	"testing"
)

func mc(label, addr string, port int, codec, role string, prio int) MulticastInput {
	return MulticastInput{Label: label, Address: addr, Port: port, Codec: codec, Role: role, Priority: prio}
}

// TestValidateMulticast fails if an address outside the IPv4 multicast
// range or inside 224.0.0.0/24, a port below 1024, an unknown codec, role
// or priority, a duplicate listen address or an eleventh channel is
// accepted, or if administratively scoped addresses, a send-only channel
// sharing a listen address and an empty list are refused.
func TestValidateMulticast(t *testing.T) {
	ok := mc("Dock", "239.255.0.1", 5004, CodecPCMU, RoleBoth, 1)
	for name, tc := range map[string][]MulticastInput{
		"empty":                 nil,
		"scoped":                {ok, mc("Two", "239.192.1.1", 6000, CodecG722, RoleListen, 25)},
		"send shares a listen":  {ok, mc("Out", "239.255.0.1", 5004, CodecPCMA, RoleSend, 2)},
		"same address new port": {ok, mc("Two", "239.255.0.1", 5006, CodecPCMU, RoleListen, 2)},
		"top of range":          {mc("Edge", "239.255.255.255", 65535, CodecPCMU, RoleListen, 1)},
		"224.0.1.116 (PTT)":     {mc("Ptt", "224.0.1.116", 5001, CodecPCMU, RoleListen, 1)},
	} {
		if f := ValidateMulticast(tc); len(f) != 0 {
			t.Errorf("%s refused: %v", name, f)
		}
	}
	for name, tc := range map[string]struct {
		in   []MulticastInput
		path string
	}{
		"unicast":     {[]MulticastInput{mc("a", "10.0.0.1", 5004, CodecPCMU, RoleBoth, 1)}, "channels[0].address"},
		"reserved":    {[]MulticastInput{mc("a", "224.0.0.251", 5004, CodecPCMU, RoleBoth, 1)}, "channels[0].address"},
		"ipv6":        {[]MulticastInput{mc("a", "ff02::1", 5004, CodecPCMU, RoleBoth, 1)}, "channels[0].address"},
		"text":        {[]MulticastInput{mc("a", "lunch", 5004, CodecPCMU, RoleBoth, 1)}, "channels[0].address"},
		"low port":    {[]MulticastInput{mc("a", "239.1.1.1", 1023, CodecPCMU, RoleBoth, 1)}, "channels[0].port"},
		"high port":   {[]MulticastInput{mc("a", "239.1.1.1", 65536, CodecPCMU, RoleBoth, 1)}, "channels[0].port"},
		"codec":       {[]MulticastInput{mc("a", "239.1.1.1", 5004, "OPUS", RoleBoth, 1)}, "channels[0].codec"},
		"role":        {[]MulticastInput{mc("a", "239.1.1.1", 5004, CodecPCMU, "talk", 1)}, "channels[0].role"},
		"priority 0":  {[]MulticastInput{mc("a", "239.1.1.1", 5004, CodecPCMU, RoleBoth, 0)}, "channels[0].priority"},
		"priority 26": {[]MulticastInput{mc("a", "239.1.1.1", 5004, CodecPCMU, RoleBoth, 26)}, "channels[0].priority"},
		"label":       {[]MulticastInput{mc(" a", "239.1.1.1", 5004, CodecPCMU, RoleBoth, 1)}, "channels[0].label"},
		"long label":  {[]MulticastInput{mc(strings.Repeat("a", 41), "239.1.1.1", 5004, CodecPCMU, RoleBoth, 1)}, "channels[0].label"},
		"duplicate":   {[]MulticastInput{ok, mc("b", "239.255.0.1", 5004, CodecPCMA, RoleListen, 2)}, "channels[1].address"},
		"eleven": {func() []MulticastInput {
			var l []MulticastInput
			for range 11 {
				l = append(l, ok)
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

// TestValidatePTT fails if a PTT address outside the multicast range, a
// codec other than PCMU or G722, a channel number outside 1 to 25 or used
// twice, a default channel the phone does not take or that cannot transmit
// is accepted while PTT is on, or if a good setting is refused.
func TestValidatePTT(t *testing.T) {
	good := PTT{Enabled: true, Address: DefaultPTTAddress, Codec: CodecPCMU, DefaultChannel: 1,
		Channels: []PTTChannel{{Channel: 1, Label: "All", Transmit: true, Subscribe: true}, {Channel: 24, Label: "Priority", Transmit: true}}}
	if f := ValidatePTT(good); len(f) != 0 {
		t.Fatalf("good PTT refused: %v", f)
	}
	off := PTT{Address: DefaultPTTAddress, Codec: CodecG722, DefaultChannel: 1}
	if f := ValidatePTT(off); len(f) != 0 {
		t.Errorf("PTT off with no channels refused: %v", f)
	}
	for name, mutate := range map[string]func(p *PTT){
		"address":     func(p *PTT) { p.Address = "10.0.0.1:5001" },
		"no port":     func(p *PTT) { p.Address = "224.0.1.116" },
		"reserved":    func(p *PTT) { p.Address = "224.0.0.9:5001" },
		"low port":    func(p *PTT) { p.Address = "224.0.1.116:80" },
		"codec":       func(p *PTT) { p.Codec = CodecPCMA },
		"channel 0":   func(p *PTT) { p.Channels[0].Channel = 0 },
		"channel 26":  func(p *PTT) { p.Channels[1].Channel = 26 },
		"twice":       func(p *PTT) { p.Channels[1].Channel = 1 },
		"default":     func(p *PTT) { p.DefaultChannel = 5 },
		"no transmit": func(p *PTT) { p.Channels[0].Transmit = false },
		"missing":     func(p *PTT) { p.DefaultChannel = 2 },
		"label":       func(p *PTT) { p.Channels[0].Label = "" },
	} {
		p := good
		p.Channels = append([]PTTChannel(nil), good.Channels...)
		mutate(&p)
		if f := ValidatePTT(p); len(f) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
}
