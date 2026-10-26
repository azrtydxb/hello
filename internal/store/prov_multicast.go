package store

// The multicast paging and push-to-talk settings a phone gets
// (phonebooks-ptt spec P-15, phone-feature-parity S-30): from its group,
// the multicast channel catalog and its own PTT channel override.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// multicastInfo is RenderData.Multicast and RenderData.PTT for a phone.
//
// The phone listens on its group's catalog paging channels; a group that
// names none, and a phone without a group, listen on every enabled paging
// channel. A phone that supports PTT (SupportsPTT) always gets PTT: its
// group's settings, or for a phone without a group PTT on with channel 1;
// the PTT address and codec are those of the phone's PTT channel override,
// else its group's, else the catalog's first enabled PTT channel, else
// Grandstream's default 224.0.1.116:5001 with PCMU. The paging options are
// the group's, else the defaults.
func (s *Store) multicastInfo(ctx context.Context, phoneID int64, v prov.Vendor, model string) (*prov.MulticastInfo, *prov.PTTInfo, *prov.PagingOptions, error) {
	var group, override sql.NullInt64
	err := s.q(ctx).QueryRowContext(ctx, `SELECT group_id, ptt_channel_id FROM phones WHERE id = $1`, phoneID).Scan(&group, &override)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, prov.ErrNotFound
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var g *phonebook.Group
	if group.Valid {
		gg, err := getPhoneGroup(ctx, s.q(ctx), group.Int64)
		if err != nil {
			return nil, nil, nil, err
		}
		g = &gg
	}
	var chans []prov.MulticastChannel
	var roles []string
	if g != nil && len(g.Multicast) > 0 {
		for _, c := range g.Multicast {
			if c.Enabled {
				chans = append(chans, prov.MulticastChannel{Label: c.Label, Address: c.Address, Port: c.Port, Codec: c.Codec, Priority: c.Priority})
				roles = append(roles, c.Role)
			}
		}
	} else {
		all, err := catalogChannels(ctx, s.q(ctx), phonebook.KindPaging, true)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, c := range all {
			chans = append(chans, prov.MulticastChannel{Label: c.Label, Address: c.Address, Port: c.Port, Codec: c.Codec, Priority: c.Priority})
			roles = append(roles, phonebook.RoleBoth)
		}
	}
	opts := prov.DefaultPagingOptions()
	if g != nil {
		o := g.PagingOptions
		opts = &prov.PagingOptions{AllowInDND: b2i(o.AllowInDND), Barge: o.Barge, PriorityActive: b2i(o.PriorityActive),
			Timeout: o.Timeout, IntercomBarge: b2i(o.IntercomBarge)}
	}
	ptt := prov.SupportsPTT(v, model)
	mc := prov.NewMulticastInfo(chans, roles)
	if mc != nil {
		mc.PagingKey = ptt
	}
	if !ptt {
		return mc, nil, opts, nil
	}
	ptts, err := catalogChannels(ctx, s.q(ctx), phonebook.KindPTT, true)
	if err != nil {
		return nil, nil, nil, err
	}
	return mc, phonePTT(g, ptts, nullInt(override)), opts, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// phonePTT is the PTT of a phone that supports it, from its group (nil for
// none), the enabled catalog PTT channels and the phone's override: the
// group's settings and channels (the default channel alone when PTT is on
// and it defines no PTT channel, the default group alone when group paging
// is on and it defines no group channel), and for a phone without a group
// PTT on with channel 1 and group paging off.
func phonePTT(g *phonebook.Group, ptts []phonebook.Channel, override *int64) *prov.PTTInfo {
	set := phonebook.DefaultPTT()
	set.Address, set.Codec = phonebook.DefaultPTTAddress, phonebook.CodecPCMU
	if g != nil {
		set = g.PTT
	}
	in := prov.PTTSettings{Enabled: set.Enabled, Address: set.Address, Codec: set.Codec,
		DefaultChannel: set.DefaultChannel, PriorityChannel: set.PriorityChannel, EmergencyChannel: set.EmergencyChannel,
		AcceptBusy: set.AcceptBusy, GroupPaging: set.GroupPaging, DefaultGroup: set.DefaultGroup,
		PriorityGroup: set.PriorityGroup, EmergencyGroup: set.EmergencyGroup}
	hasPTT, hasGroup := false, false
	for _, c := range set.Channels {
		d := prov.PTTChannel{Channel: c.Channel, Label: c.Label}
		if c.Transmit {
			d.Transmit = 1
		}
		if c.Subscribe {
			d.Subscribe = 1
		}
		in.Channels = append(in.Channels, d)
		if c.Channel <= phonebook.MaxPTTChannels {
			hasPTT = true
		} else {
			hasGroup = true
		}
	}
	if c, ok := resolvePTT(ptts, override, set.ChannelID); ok {
		in.Address, in.Codec = c.HostPort(), c.Codec
	}
	if in.Enabled && !hasPTT {
		in.Channels = append(in.Channels, prov.PTTChannel{Channel: in.DefaultChannel, Label: "Channel " + strconv.Itoa(in.DefaultChannel), Transmit: 1, Subscribe: 1})
	}
	if in.GroupPaging && !hasGroup {
		in.Channels = append(in.Channels, prov.PTTChannel{Channel: in.DefaultGroup, Label: "Group " + strconv.Itoa(in.DefaultGroup), Transmit: 1, Subscribe: 1})
	}
	return prov.NewPTTInfo(in)
}
