package prov

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Phone-native multicast paging and push-to-talk (phonebooks-ptt spec P-15,
// phone-feature-parity S-30): the settings the built-in templates write
// from a phone's group. Hello is not in the media path of these.

// MaxMulticastListen is how many listening addresses the phones take
// (Grandstream, Yealink: 10 and more); a group has at most this many
// channels.
const MaxMulticastListen = 10

// PTTChannels is the number of PTT channels (1 to 25) of a Grandstream
// WP8x6; RenderData.PTT.Channels always has this many entries.
const PTTChannels = 25

// MulticastChannel is one channel of the phone's group.
type MulticastChannel struct {
	Label    string
	Address  string // IPv4 multicast
	Port     int
	Codec    string // PCMU, PCMA or G722
	Priority int
	// HostPort is "address:port", the form phones take a multicast address
	// in; empty for an unused slot of ListenSlots.
	HostPort string
	// Param is, in ListenSlots, the Grandstream P-value of the slot's
	// listening address (the label's is Param+1).
	Param int
}

// MulticastInfo is the multicast paging of the phone's group; nil when the
// group has no channel. Listen are the channels the phones listen on and
// Send those they send to, both in priority order (1 first, then the
// group's order).
type MulticastInfo struct {
	Listen []MulticastChannel
	Send   []MulticastChannel
	// ListenSlots is Listen padded with empty entries to the ten listening
	// slots a Grandstream phone has, so a template can clear the unused
	// ones.
	ListenSlots []MulticastChannel
	// PagingKey is set for a phone whose PTT/paging button needs the PTT and
	// paging feature switched on (SupportsPTT).
	PagingKey bool
	// Codec is the one codec the phones page with: the first channel's
	// (Grandstream and Yealink take a single multicast codec). GSCodec is its
	// Grandstream number (0 PCMU, 8 PCMA, 9 G.722) and PolyCodec its Poly
	// name.
	Codec     string
	GSCodec   int
	PolyCodec string
}

// NewMulticastInfo builds the info from a group's channels, in the group's
// order; roles are "listen", "send" or "both". It is nil for no channels.
func NewMulticastInfo(channels []MulticastChannel, roles []string) *MulticastInfo {
	if len(channels) == 0 {
		return nil
	}
	m := &MulticastInfo{Codec: channels[0].Codec}
	switch m.Codec {
	case "PCMA":
		m.GSCodec, m.PolyCodec = 8, "G.711A"
	case "G722":
		m.GSCodec, m.PolyCodec = 9, "G.722"
	default:
		m.GSCodec, m.PolyCodec = 0, "G.711Mu"
	}
	idx := make([]int, len(channels))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return channels[a].Priority - channels[b].Priority })
	for _, i := range idx {
		c := channels[i]
		c.HostPort = c.Address + ":" + strconv.Itoa(c.Port)
		if roles[i] != "send" && len(m.Listen) < MaxMulticastListen {
			m.Listen = append(m.Listen, c)
		}
		if roles[i] != "listen" && len(m.Send) < MaxMulticastListen {
			m.Send = append(m.Send, c)
		}
	}
	m.ListenSlots = make([]MulticastChannel, MaxMulticastListen)
	for i := range m.ListenSlots {
		if i < len(m.Listen) {
			m.ListenSlots[i] = m.Listen[i]
		}
		m.ListenSlots[i].Param = 1569 + 2*i
	}
	return m
}

// PTTChannel is one of the 25 PTT channel slots.
type PTTChannel struct {
	Channel   int
	Label     string
	Available int // 1 when the group defines the channel
	Transmit  int
	Subscribe int
	// Base is the Grandstream P-value of the slot's Available setting;
	// Transmit, Subscribe, Join and Name follow at Base+1 to Base+4.
	Base int
}

// PTTInfo is the push-to-talk of the phone's group, given only to phones
// that support it (SupportsPTT) and only when it is on.
type PTTInfo struct {
	Address        string // host:port, shared by every channel
	CodecGS        int    // 0 PCMU, 9 G.722
	DefaultChannel int
	AcceptBusy     int
	Channels       []PTTChannel // always PTTChannels entries, by channel number
}

// NewPTTInfo builds the info from a group's settings; defined are the
// channels the group names, by number.
func NewPTTInfo(address, codec string, defaultChannel int, acceptBusy bool, defined []PTTChannel) *PTTInfo {
	p := &PTTInfo{Address: address, DefaultChannel: defaultChannel, Channels: make([]PTTChannel, PTTChannels)}
	if codec == "G722" {
		p.CodecGS = 9
	}
	if acceptBusy {
		p.AcceptBusy = 1
	}
	for i := range p.Channels {
		p.Channels[i] = PTTChannel{Channel: i + 1, Base: 82001 + 5*i}
	}
	for _, c := range defined {
		if c.Channel >= 1 && c.Channel <= PTTChannels {
			c.Base = 82001 + 5*(c.Channel-1)
			c.Available = 1
			p.Channels[c.Channel-1] = c
		}
	}
	return p
}

var pttModelRE = regexp.MustCompile(`^WP8[1-3]6`)

// SupportsPTT reports whether the phone has the PTT and group paging
// feature set provisioned through the PTT keys (P22177 and following): the
// Grandstream WP816, WP826 and WP836. The WP822 has a PTT button but no PTT
// channel settings in its configuration template (only multicast paging),
// so it gets the multicast keys alone.
func SupportsPTT(v Vendor, model string) bool {
	return v == Grandstream && pttModelRE.MatchString(strings.ToUpper(strings.ReplaceAll(model, " ", "")))
}
