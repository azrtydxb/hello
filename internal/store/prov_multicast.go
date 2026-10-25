package store

// The multicast paging and push-to-talk settings a phone's group gives it
// (phonebooks-ptt spec P-15, phone-feature-parity S-30).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/azrtydxb/hello/internal/prov"
)

// multicastInfo is RenderData.Multicast and RenderData.PTT for a phone:
// nil for a phone without a group, a group without channels, a group whose
// PTT is off or a phone that does not support PTT.
func (s *Store) multicastInfo(ctx context.Context, phoneID int64, v prov.Vendor, model string) (*prov.MulticastInfo, *prov.PTTInfo, error) {
	var group sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT group_id FROM phones WHERE id = $1`, phoneID).Scan(&group)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, prov.ErrNotFound
	}
	if err != nil || !group.Valid {
		return nil, nil, err
	}
	g, err := getPhoneGroup(ctx, s.db, group.Int64)
	if err != nil {
		return nil, nil, err
	}
	var chans []prov.MulticastChannel
	var roles []string
	for _, c := range g.Multicast {
		chans = append(chans, prov.MulticastChannel{Label: c.Label, Address: c.Address, Port: c.Port, Codec: c.Codec, Priority: c.Priority})
		roles = append(roles, c.Role)
	}
	mc := prov.NewMulticastInfo(chans, roles)
	if mc != nil {
		mc.PagingKey = prov.SupportsPTT(v, model)
	}
	if !g.PTT.Enabled || !prov.SupportsPTT(v, model) {
		return mc, nil, nil
	}
	defined := make([]prov.PTTChannel, len(g.PTT.Channels))
	for i, c := range g.PTT.Channels {
		defined[i] = prov.PTTChannel{Channel: c.Channel, Label: c.Label}
		if c.Transmit {
			defined[i].Transmit = 1
		}
		if c.Subscribe {
			defined[i].Subscribe = 1
		}
	}
	return mc, prov.NewPTTInfo(g.PTT.Address, g.PTT.Codec, g.PTT.DefaultChannel, g.PTT.AcceptBusy, defined), nil
}
