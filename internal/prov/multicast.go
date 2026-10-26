package prov

import (
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

// PTTChannels is the number of PTT channels (1 to 25) and of group paging
// channels (26 to 50) of a Grandstream WP; RenderData.PTT.Channels and
// RenderData.PTT.Groups always have this many entries.
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

// PTTChannel is one of the 50 PTT (1 to 25) and group paging (26 to 50)
// channel slots.
type PTTChannel struct {
	Channel   int
	Label     string
	Available int // 1 when the group defines the channel
	Transmit  int
	Subscribe int
	// Base is the Grandstream P-value of the slot's Available setting on a
	// WP8x6 or WP856 (P82001 + 5 (n-1)); Transmit, Subscribe, Join and Name
	// follow at Base+1 to Base+4.
	Base int
}

// PTTSettings is what a phone's PTT is built from: its group's settings,
// or Hello's defaults, with the address and codec of its PTT channel.
// Channels are the defined channels, 1 to 50.
type PTTSettings struct {
	Enabled          bool
	Address          string
	Codec            string
	DefaultChannel   int
	PriorityChannel  int
	EmergencyChannel int
	AcceptBusy       bool
	GroupPaging      bool
	DefaultGroup     int
	PriorityGroup    int
	EmergencyGroup   int
	Channels         []PTTChannel
}

// PTTMasks is the WP820's form of 25 channel slots: one digit per channel
// (1 on) for available (and joined), transmit and subscribe, and the labels
// separated by "^".
type PTTMasks struct {
	Available string
	Transmit  string
	Subscribe string
	Labels    string
}

// PTTInfo is the push-to-talk and group paging of a phone that supports it
// (SupportsPTT): its group's, or the defaults (PTT on with channel 1 on the
// catalog's PTT channel, group paging off) for a phone without a group.
// Enabled is false when the group switched PTT off; the template then
// writes PTT off. Group paging shares PTT's address and codec.
type PTTInfo struct {
	Enabled          bool
	Address          string // host:port, shared by every channel
	CodecGS          int    // WP8x6, WP856, WP8xx: 0 PCMU, 9 G.722
	CodecWP820       int    // WP820: 0 G.711, 1 G.722
	DefaultChannel   int
	PriorityChannel  int
	EmergencyChannel int
	AcceptBusy       int
	Channels         []PTTChannel // always PTTChannels entries, channels 1 to 25
	GroupPaging      bool
	DefaultGroup     int
	PriorityGroup    int
	EmergencyGroup   int
	Groups           []PTTChannel // always PTTChannels entries, channels 26 to 50
	// ChannelMasks and GroupMasks are Channels and Groups in the WP820's form.
	ChannelMasks PTTMasks
	GroupMasks   PTTMasks
}

// NewPTTInfo builds the info from a group's settings.
func NewPTTInfo(in PTTSettings) *PTTInfo {
	p := &PTTInfo{Enabled: in.Enabled, Address: in.Address, DefaultChannel: in.DefaultChannel,
		PriorityChannel: in.PriorityChannel, EmergencyChannel: in.EmergencyChannel,
		GroupPaging: in.GroupPaging, DefaultGroup: in.DefaultGroup, PriorityGroup: in.PriorityGroup, EmergencyGroup: in.EmergencyGroup,
		Channels: make([]PTTChannel, PTTChannels), Groups: make([]PTTChannel, PTTChannels)}
	if in.Codec == "G722" {
		p.CodecGS, p.CodecWP820 = 9, 1
	}
	if in.AcceptBusy {
		p.AcceptBusy = 1
	}
	for i := range PTTChannels {
		p.Channels[i] = PTTChannel{Channel: i + 1, Base: 82001 + 5*i}
		p.Groups[i] = PTTChannel{Channel: PTTChannels + i + 1, Base: 82001 + 5*(PTTChannels+i)}
	}
	for _, c := range in.Channels {
		if c.Channel < 1 || c.Channel > 2*PTTChannels {
			continue
		}
		c.Base = 82001 + 5*(c.Channel-1)
		c.Available = 1
		if c.Channel <= PTTChannels {
			p.Channels[c.Channel-1] = c
		} else {
			p.Groups[c.Channel-PTTChannels-1] = c
		}
	}
	p.ChannelMasks, p.GroupMasks = masks(p.Channels), masks(p.Groups)
	return p
}

func masks(chans []PTTChannel) PTTMasks {
	var a, t, s strings.Builder
	labels := make([]string, len(chans))
	for i, c := range chans {
		a.WriteString(strconv.Itoa(c.Available))
		t.WriteString(strconv.Itoa(c.Transmit))
		s.WriteString(strconv.Itoa(c.Subscribe))
		labels[i] = strings.ReplaceAll(c.Label, "^", " ")
	}
	return PTTMasks{Available: a.String(), Transmit: t.String(), Subscribe: s.String(), Labels: strings.Join(labels, "^")}
}

// PagingOptions is how a phone takes a multicast page and an intercom call
// (its group's, else the defaults): Grandstream P8381 allowed in DND, P1566
// paging barge (0 off, 1 to 10), P1567 paging priority active, P22532
// multicast call timeout in seconds, and P26019 (Yealink
// features.intercom.barge) intercom barging. All are 0 or 1 except Barge
// and Timeout.
type PagingOptions struct {
	AllowInDND     int
	Barge          int
	PriorityActive int
	Timeout        int
	IntercomBarge  int
}

// DefaultPagingOptions is what a phone without a group gets: Grandstream's
// WP8x6 defaults, intercom barging off.
func DefaultPagingOptions() *PagingOptions { return &PagingOptions{PriorityActive: 1} }

// The Grandstream WP families, by the PTT and paging keys their
// configuration templates take.
const (
	// WP8x6 is the WP816, WP826 and WP836 (wp8x6_config_1.0.3.39): every
	// key, PTT channel slots at P82001 and up.
	WP8x6 = "WP8x6"
	// WP856 (wp856_config_1.0.3.16): the WP8x6 PTT keys and slots, without
	// the PTT/Paging switch P22556, P8381 and P22532.
	WP856 = "WP856"
	// WP8xx is the WP810, WP822 and WP825 (wp810_822_825_config_1.0.11.83):
	// multicast paging, P8381 and P26019 only; its handset has PTT and group
	// paging (WP8xx User Guide, "PTT (Push-to-talk) functionality") but the
	// template gives no key for them, so the WP8x6 keys are written
	// unverified.
	WP8xx = "WP8xx"
	// WP820 (wp820_config_1.0.7.90): PTT and group paging with the channel
	// slots as digit strings (P22187 and up, P22203 and up).
	WP820 = "WP820"
)

// WPFamily is the Grandstream WP family of a model, or "" for another
// phone.
func WPFamily(v Vendor, model string) string {
	if v != Grandstream {
		return ""
	}
	switch GrandstreamFamily(model) {
	case FamilyWP8x6:
		return WP8x6
	case FamilyWP856:
		return WP856
	case FamilyWP8xx:
		if strings.HasPrefix(normModel(model), "WP820") {
			return WP820
		}
		return WP8xx
	}
	return ""
}

// SupportsPTT reports whether the phone is given the PTT and group paging
// keys: a Grandstream WP of any family (WPFamily).
func SupportsPTT(v Vendor, model string) bool {
	return WPFamily(v, model) != ""
}
