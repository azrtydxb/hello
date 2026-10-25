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

// MulticastChannel is one multicast paging channel of a phone group.
type MulticastChannel struct {
	ID       int64  `json:"id"`
	Label    string `json:"label"`
	Address  string `json:"address"` // IPv4 multicast
	Port     int    `json:"port"`
	Codec    string `json:"codec"`
	Role     string `json:"role"`
	Priority int    `json:"priority"` // 1 (highest) to 25
}

// Listens reports whether phones of the group listen on the channel.
func (c MulticastChannel) Listens() bool { return c.Role == RoleListen || c.Role == RoleBoth }

// MulticastInput is a channel as written; the list replaces the group's.
type MulticastInput struct {
	Label    string `json:"label"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Codec    string `json:"codec"`
	Role     string `json:"role"`
	Priority int    `json:"priority"`
}

// ValidateMulticast checks a group's whole channel list: at most
// MaxMulticastChannels, each with a label, an IPv4 multicast address that is
// not in 224.0.0.0/24 (link-local control traffic; administratively scoped
// 239/8 and the rest are accepted), a port of 1024 to 65535, a known codec
// and role and a priority of 1 to 25, and no listen address and port twice.
func ValidateMulticast(in []MulticastInput) []FieldError {
	var out []FieldError
	if len(in) > MaxMulticastChannels {
		return []FieldError{{"channels", fmt.Sprintf("at most %d channels", MaxMulticastChannels)}}
	}
	seen := map[string]int{}
	for i, c := range in {
		p := "channels[" + strconv.Itoa(i) + "]."
		if !ValidName(c.Label, MaxChannelLabelLen) {
			out = append(out, FieldError{p + "label", fmt.Sprintf("must be 1-%d printable characters without surrounding space", MaxChannelLabelLen)})
		}
		a, err := netip.ParseAddr(c.Address)
		switch {
		case err != nil || !a.Is4() || !a.IsMulticast():
			out = append(out, FieldError{p + "address", "must be an IPv4 multicast address (224.0.0.0 to 239.255.255.255)"})
		case a.As4()[0] == 224 && a.As4()[1] == 0 && a.As4()[2] == 0:
			out = append(out, FieldError{p + "address", "224.0.0.0/24 is reserved for network control traffic"})
		}
		if c.Port < MinMulticastPort || c.Port > MaxMulticastPort {
			out = append(out, FieldError{p + "port", fmt.Sprintf("must be %d-%d", MinMulticastPort, MaxMulticastPort)})
		}
		switch c.Codec {
		case CodecPCMU, CodecPCMA, CodecG722:
		default:
			out = append(out, FieldError{p + "codec", "must be PCMU, PCMA or G722"})
		}
		switch c.Role {
		case RoleListen, RoleSend, RoleBoth:
		default:
			out = append(out, FieldError{p + "role", "must be listen, send or both"})
		}
		if c.Priority < 1 || c.Priority > MaxMulticastPriority {
			out = append(out, FieldError{p + "priority", fmt.Sprintf("must be 1-%d", MaxMulticastPriority)})
		}
		if c.Role != RoleSend && err == nil {
			key := c.Address + ":" + strconv.Itoa(c.Port)
			if j, dup := seen[key]; dup {
				out = append(out, FieldError{p + "address", fmt.Sprintf("is already listened on by channels[%d]", j)})
			}
			seen[key] = i
		}
	}
	return out
}

// PTT codecs: Grandstream's PTT and group paging take PCMU or G.722 only.
const (
	DefaultPTTAddress = "224.0.1.116:5001"
)

// PTTChannel is one push-to-talk channel (1 to 25) of a phone group.
type PTTChannel struct {
	Channel   int    `json:"channel"`
	Label     string `json:"label"`
	Transmit  bool   `json:"transmit"`
	Subscribe bool   `json:"subscribe"`
}

// PTT is a phone group's push-to-talk settings (Grandstream WP8x6 PTT:
// every phone of the group uses one multicast address and the channel
// number selects who hears). Hello provisions them; the audio is the
// phones' own.
type PTT struct {
	Enabled        bool         `json:"enabled"`
	Address        string       `json:"address"` // host:port, default 224.0.1.116:5001
	Codec          string       `json:"codec"`   // PCMU or G722
	DefaultChannel int          `json:"defaultChannel"`
	AcceptBusy     bool         `json:"acceptWhileBusy"`
	Channels       []PTTChannel `json:"channels"`
}

// ValidatePTT checks the settings: an IPv4 multicast host:port (port 1024 to
// 65535, not 224.0.0.0/24), PCMU or G722, 1 to 25 channels numbered 1 to 25
// once each with a label, and a default channel that is one of them and can
// transmit.
func ValidatePTT(in PTT) []FieldError {
	var out []FieldError
	ap, err := netip.ParseAddrPort(in.Address)
	switch {
	case err != nil || !ap.Addr().Is4() || !ap.Addr().IsMulticast():
		out = append(out, FieldError{"address", "must be an IPv4 multicast address and port, such as " + DefaultPTTAddress})
	case ap.Addr().As4()[0] == 224 && ap.Addr().As4()[1] == 0 && ap.Addr().As4()[2] == 0:
		out = append(out, FieldError{"address", "224.0.0.0/24 is reserved for network control traffic"})
	case int(ap.Port()) < MinMulticastPort:
		out = append(out, FieldError{"address", fmt.Sprintf("port must be %d-%d", MinMulticastPort, MaxMulticastPort)})
	}
	if in.Codec != CodecPCMU && in.Codec != CodecG722 {
		out = append(out, FieldError{"codec", "must be PCMU or G722"})
	}
	if len(in.Channels) > MaxPTTChannels {
		return append(out, FieldError{"channels", fmt.Sprintf("at most %d channels", MaxPTTChannels)})
	}
	seen := map[int]bool{}
	defaultOK := false
	for i, c := range in.Channels {
		p := "channels[" + strconv.Itoa(i) + "]."
		switch {
		case c.Channel < 1 || c.Channel > MaxPTTChannels:
			out = append(out, FieldError{p + "channel", fmt.Sprintf("must be 1-%d", MaxPTTChannels)})
		case seen[c.Channel]:
			out = append(out, FieldError{p + "channel", "is used twice"})
		}
		seen[c.Channel] = true
		if !ValidName(c.Label, MaxChannelLabelLen) {
			out = append(out, FieldError{p + "label", fmt.Sprintf("must be 1-%d printable characters without surrounding space", MaxChannelLabelLen)})
		}
		if c.Channel == in.DefaultChannel && c.Transmit {
			defaultOK = true
		}
	}
	switch in.DefaultChannel {
	case 1, 2, 24, 25:
	default:
		if in.Enabled {
			out = append(out, FieldError{"defaultChannel", "must be 1, 2, 24 or 25 (the channels a Grandstream phone takes as its default)"})
		}
	}
	if in.Enabled && !defaultOK {
		out = append(out, FieldError{"defaultChannel", "must be one of the channels, and that channel must transmit"})
	}
	return out
}
