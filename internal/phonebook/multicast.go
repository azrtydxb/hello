package phonebook

// Phone-native multicast paging and push-to-talk of a phone group
// (phonebooks-ptt spec P-15, phone-feature-parity S-30). Hello is not in
// the media path of these: it only writes each phone's settings, except for
// a paging group that names a multicast channel, where hello-sip also sends
// the page's audio to the channel's address.

import (
	"fmt"
	"net/netip"
	"strconv"
)

// Limits (spec P-15, S-30).
const (
	MaxMulticastChannels = 10
	MaxPTTChannels       = 25
	// Group paging channels follow the PTT channels: 26 to 50.
	MinGroupChannel      = 26
	MaxGroupChannel      = 50
	MaxPagingBarge       = 10
	MaxPagingTimeout     = 3600
	MaxChannelLabelLen   = 40
	MinMulticastPort     = 1024
	MaxMulticastPort     = 65535
	MaxMulticastPriority = 25
)

// Multicast channel codecs and roles.
const (
	CodecPCMU = "PCMU"
	CodecPCMA = "PCMA"
	CodecG722 = "G722"

	RoleListen = "listen"
	RoleSend   = "send"
	RoleBoth   = "both"
)

// Channel kinds: a paging channel carries plain RTP multicast paging (the
// phones listen on it, a paging group may send to it); a PTT channel is a
// Grandstream PTT/group paging address (P22183), whose packets carry the
// PTT channel number (1 to 25) inside.
const (
	KindPaging = "paging"
	KindPTT    = "ptt"
)

// Channel is one channel of the system-wide multicast channel catalog
// (Paging > Multicast channels).
type Channel struct {
	ID       int64  `json:"id"`
	Label    string `json:"label"`
	Address  string `json:"address"` // IPv4 multicast
	Port     int    `json:"port"`
	Codec    string `json:"codec"`
	Priority int    `json:"priority"` // 1 (highest) to 25
	Kind     string `json:"kind"`     // paging or ptt
	Enabled  bool   `json:"enabled"`
}

// HostPort is "address:port".
func (c Channel) HostPort() string { return c.Address + ":" + strconv.Itoa(c.Port) }

// ChannelInput is a catalog channel as written.
type ChannelInput struct {
	Label    string `json:"label"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Codec    string `json:"codec"`
	Priority int    `json:"priority"`
	Kind     string `json:"kind"`
	Enabled  *bool  `json:"enabled"`
}

// ValidateChannel checks a catalog channel: a label, an IPv4 multicast
// address that is not in 224.0.0.0/24 (link-local control traffic;
// administratively scoped 239/8 and the rest are accepted), a port of 1024
// to 65535, a priority of 1 to 25, a known kind and a codec the kind takes
// (PCMU, PCMA or G722 for paging; PCMU or G722 for Grandstream PTT).
func ValidateChannel(in ChannelInput) []FieldError {
	var out []FieldError
	if !ValidName(in.Label, MaxChannelLabelLen) {
		out = append(out, FieldError{"label", fmt.Sprintf("must be 1-%d printable characters without surrounding space", MaxChannelLabelLen)})
	}
	a, err := netip.ParseAddr(in.Address)
	switch {
	case err != nil || !a.Is4() || !a.IsMulticast():
		out = append(out, FieldError{"address", "must be an IPv4 multicast address (224.0.0.0 to 239.255.255.255)"})
	case a.As4()[0] == 224 && a.As4()[1] == 0 && a.As4()[2] == 0:
		out = append(out, FieldError{"address", "224.0.0.0/24 is reserved for network control traffic"})
	}
	if in.Port < MinMulticastPort || in.Port > MaxMulticastPort {
		out = append(out, FieldError{"port", fmt.Sprintf("must be %d-%d", MinMulticastPort, MaxMulticastPort)})
	}
	if in.Priority < 1 || in.Priority > MaxMulticastPriority {
		out = append(out, FieldError{"priority", fmt.Sprintf("must be 1-%d", MaxMulticastPriority)})
	}
	switch in.Kind {
	case KindPaging:
		switch in.Codec {
		case CodecPCMU, CodecPCMA, CodecG722:
		default:
			out = append(out, FieldError{"codec", "must be PCMU, PCMA or G722"})
		}
	case KindPTT:
		if in.Codec != CodecPCMU && in.Codec != CodecG722 {
			out = append(out, FieldError{"codec", "must be PCMU or G722 (the codecs Grandstream PTT takes)"})
		}
	default:
		out = append(out, FieldError{"kind", "must be paging or ptt"})
	}
	return out
}

// MulticastChannel is one catalog paging channel as a phone group uses it
// (ID is the catalog channel's).
type MulticastChannel struct {
	ID       int64  `json:"id"`
	Label    string `json:"label"`
	Address  string `json:"address"` // IPv4 multicast
	Port     int    `json:"port"`
	Codec    string `json:"codec"`
	Role     string `json:"role"`
	Priority int    `json:"priority"` // 1 (highest) to 25
	Enabled  bool   `json:"enabled"`
}

// Listens reports whether phones of the group listen on the channel.
func (c MulticastChannel) Listens() bool { return c.Role == RoleListen || c.Role == RoleBoth }

// MulticastInput is one catalog channel a group uses; the list replaces the
// group's. An empty list means every enabled paging channel, listen and send.
type MulticastInput struct {
	ChannelID int64  `json:"channelId"`
	Role      string `json:"role"`
}

// ValidateMulticast checks a group's channel list: at most
// MaxMulticastChannels, each a channel id once with a known role. That the
// channels exist and are paging channels is the store's check.
func ValidateMulticast(in []MulticastInput) []FieldError {
	var out []FieldError
	if len(in) > MaxMulticastChannels {
		return []FieldError{{"channels", fmt.Sprintf("at most %d channels", MaxMulticastChannels)}}
	}
	seen := map[int64]int{}
	for i, c := range in {
		p := "channels[" + strconv.Itoa(i) + "]."
		if c.ChannelID <= 0 {
			out = append(out, FieldError{p + "channelId", "must be a multicast channel id"})
		} else if j, dup := seen[c.ChannelID]; dup {
			out = append(out, FieldError{p + "channelId", fmt.Sprintf("is already used by channels[%d]", j)})
		}
		seen[c.ChannelID] = i
		switch c.Role {
		case RoleListen, RoleSend, RoleBoth:
		default:
			out = append(out, FieldError{p + "role", "must be listen, send or both"})
		}
	}
	return out
}

// PTT codecs: Grandstream's PTT and group paging take PCMU or G.722 only.
const (
	DefaultPTTAddress = "224.0.1.116:5001"
)

// PTTChannel is one channel of a phone group: a push-to-talk channel (1
// to 25) or a group paging channel (26 to 50).
type PTTChannel struct {
	Channel   int    `json:"channel"`
	Label     string `json:"label"`
	Transmit  bool   `json:"transmit"`
	Subscribe bool   `json:"subscribe"`
}

// PTT is a phone group's push-to-talk and group paging settings
// (Grandstream WP8x6 "PTT/Paging": every phone of the group uses one
// multicast address and the channel number selects who hears; channels 1
// to 25 are PTT, 26 to 50 group paging). Hello provisions them; the audio
// is the phones' own.
type PTT struct {
	Enabled bool `json:"enabled"`
	// ChannelID is the catalog PTT channel (kind ptt) the group's phones
	// use; nil means the catalog's first enabled PTT channel.
	ChannelID *int64 `json:"channelId"`
	// Address and Codec are the effective PTT address and codec, from the
	// channel (read-only; DefaultPTTAddress and PCMU with no PTT channel).
	Address        string `json:"address"` // host:port, default 224.0.1.116:5001
	Codec          string `json:"codec"`   // PCMU or G722
	DefaultChannel int    `json:"defaultChannel"`
	// PriorityChannel and EmergencyChannel (1 to 25, default 24 and 25)
	// take over a PTT in progress; an emergency PTT plays at the emergency
	// volume.
	PriorityChannel  int  `json:"priorityChannel"`
	EmergencyChannel int  `json:"emergencyChannel"`
	AcceptBusy       bool `json:"acceptWhileBusy"`
	// GroupPaging switches group paging (channels 26 to 50, one way) on;
	// DefaultGroup (26, 27, 49 or 50) is the channel the paging button pages,
	// PriorityGroup and EmergencyGroup (26 to 50, default 49 and 50) the
	// channels that take over a page.
	GroupPaging    bool         `json:"groupPaging"`
	DefaultGroup   int          `json:"defaultGroup"`
	PriorityGroup  int          `json:"priorityGroup"`
	EmergencyGroup int          `json:"emergencyGroup"`
	Channels       []PTTChannel `json:"channels"`
}

// DefaultPTT is a new group's push-to-talk: on, channel 1, Grandstream's
// priority and emergency channels, group paging off.
func DefaultPTT() PTT {
	return PTT{Enabled: true, DefaultChannel: 1, PriorityChannel: 24, EmergencyChannel: 25,
		DefaultGroup: MinGroupChannel, PriorityGroup: 49, EmergencyGroup: MaxGroupChannel}
}

// ValidatePTT checks the settings: at most 50 channels numbered 1 to 50
// once each with a label; while PTT is on, a default channel the phone takes
// (1, 2, 24 or 25) that is one of the PTT channels and can transmit, and a
// priority and emergency channel of 1 to 25; while group paging is on, a
// default group (26, 27, 49 or 50) that is one of the group channels and can
// transmit, and a priority and emergency group of 26 to 50. No channels of a
// kind at all is accepted: the store then defines the default one.
func ValidatePTT(in PTT) []FieldError {
	var out []FieldError
	if len(in.Channels) > MaxGroupChannel {
		return append(out, FieldError{"channels", fmt.Sprintf("at most %d channels", MaxGroupChannel)})
	}
	seen := map[int]bool{}
	defaultOK, groupOK := false, false
	pttChans, groupChans := 0, 0
	for i, c := range in.Channels {
		p := "channels[" + strconv.Itoa(i) + "]."
		switch {
		case c.Channel < 1 || c.Channel > MaxGroupChannel:
			out = append(out, FieldError{p + "channel", fmt.Sprintf("must be 1-%d (1-%d push-to-talk, %d-%d group paging)", MaxGroupChannel, MaxPTTChannels, MinGroupChannel, MaxGroupChannel)})
		case seen[c.Channel]:
			out = append(out, FieldError{p + "channel", "is used twice"})
		case c.Channel <= MaxPTTChannels:
			pttChans++
		default:
			groupChans++
		}
		seen[c.Channel] = true
		if !ValidName(c.Label, MaxChannelLabelLen) {
			out = append(out, FieldError{p + "label", fmt.Sprintf("must be 1-%d printable characters without surrounding space", MaxChannelLabelLen)})
		}
		if c.Channel == in.DefaultChannel && c.Transmit {
			defaultOK = true
		}
		if c.Channel == in.DefaultGroup && c.Transmit {
			groupOK = true
		}
	}
	if in.Enabled {
		switch in.DefaultChannel {
		case 1, 2, 24, 25:
			if !defaultOK && pttChans > 0 {
				out = append(out, FieldError{"defaultChannel", "must be one of the channels, and that channel must transmit"})
			}
		default:
			out = append(out, FieldError{"defaultChannel", "must be 1, 2, 24 or 25 (the channels a Grandstream phone takes as its default)"})
		}
		if in.PriorityChannel < 1 || in.PriorityChannel > MaxPTTChannels {
			out = append(out, FieldError{"priorityChannel", fmt.Sprintf("must be 1-%d", MaxPTTChannels)})
		}
		if in.EmergencyChannel < 1 || in.EmergencyChannel > MaxPTTChannels {
			out = append(out, FieldError{"emergencyChannel", fmt.Sprintf("must be 1-%d", MaxPTTChannels)})
		}
	}
	if in.GroupPaging {
		switch in.DefaultGroup {
		case 26, 27, 49, 50:
			if !groupOK && groupChans > 0 {
				out = append(out, FieldError{"defaultGroup", "must be one of the group channels, and that channel must transmit"})
			}
		default:
			out = append(out, FieldError{"defaultGroup", "must be 26, 27, 49 or 50 (the group channels a Grandstream phone takes as its default)"})
		}
		if in.PriorityGroup < MinGroupChannel || in.PriorityGroup > MaxGroupChannel {
			out = append(out, FieldError{"priorityGroup", fmt.Sprintf("must be %d-%d", MinGroupChannel, MaxGroupChannel)})
		}
		if in.EmergencyGroup < MinGroupChannel || in.EmergencyGroup > MaxGroupChannel {
			out = append(out, FieldError{"emergencyGroup", fmt.Sprintf("must be %d-%d", MinGroupChannel, MaxGroupChannel)})
		}
	}
	return out
}

// PagingOptions is how a group's Grandstream WP phones take a multicast
// page and an intercom call: AllowInDND plays a page in do-not-disturb
// (P8381), Barge (0 off, 1 to 10) is the lowest channel priority that
// interrupts a call (P1566), PriorityActive lets a higher-priority page
// interrupt a lower one (P1567), Timeout ends a page after so many seconds
// (P22532, 0 never), and IntercomBarge lets an intercom call
// (Call-Info/Alert-Info auto answer) break into a call in progress
// (P26019). The defaults are Grandstream's WP8x6 ones, with intercom
// barging off.
type PagingOptions struct {
	AllowInDND     bool `json:"allowInDnd"`
	Barge          int  `json:"barge"`
	PriorityActive bool `json:"priorityActive"`
	Timeout        int  `json:"timeout"`
	IntercomBarge  bool `json:"intercomBarge"`
}

// DefaultPagingOptions is a new group's: priority active, the rest off.
func DefaultPagingOptions() PagingOptions { return PagingOptions{PriorityActive: true} }

// ValidatePagingOptions checks the barge priority (0 to 10) and the timeout
// (0 to 3600 seconds).
func ValidatePagingOptions(in PagingOptions) []FieldError {
	var out []FieldError
	if in.Barge < 0 || in.Barge > MaxPagingBarge {
		out = append(out, FieldError{"options.barge", fmt.Sprintf("must be 0 (off) to %d", MaxPagingBarge)})
	}
	if in.Timeout < 0 || in.Timeout > MaxPagingTimeout {
		out = append(out, FieldError{"options.timeout", fmt.Sprintf("must be 0 (no timeout) to %d seconds", MaxPagingTimeout)})
	}
	return out
}
