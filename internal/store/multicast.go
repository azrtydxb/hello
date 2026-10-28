package store

// The system-wide multicast channel catalog (Paging > Multicast channels):
// paging channels phone groups listen on and paging groups send to, and
// Grandstream PTT channels (the PTT/group paging address of a WP8x6).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// MaxCatalogChannels bounds the catalog.
const MaxCatalogChannels = 100

const channelCols = `id, label, address, port, codec, priority, kind, enabled`

func scanChannel(r interface{ Scan(...any) error }) (phonebook.Channel, error) {
	var c phonebook.Channel
	err := r.Scan(&c.ID, &c.Label, &c.Address, &c.Port, &c.Codec, &c.Priority, &c.Kind, &c.Enabled)
	return c, err
}

// catalogChannels returns the catalog's channels of a kind ("" for all),
// enabled ones only when enabledOnly, by kind, priority and id.
func catalogChannels(ctx context.Context, q querier, kind string, enabledOnly bool) ([]phonebook.Channel, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+channelCols+` FROM multicast_channels
		WHERE group_id IS NULL AND ($1 = '' OR kind = $1) AND (enabled OR NOT $2)
		ORDER BY kind, priority, id`, kind, enabledOnly)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []phonebook.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// resolvePTT picks a phone's PTT channel from the enabled PTT channels: the
// first of ids that is one of them (the phone's override, then the
// group's), else the first by priority. ok is false with no PTT channel.
func resolvePTT(ptts []phonebook.Channel, ids ...*int64) (phonebook.Channel, bool) {
	for _, id := range ids {
		if id == nil {
			continue
		}
		for _, c := range ptts {
			if c.ID == *id {
				return c, true
			}
		}
	}
	if len(ptts) == 0 {
		return phonebook.Channel{}, false
	}
	return ptts[0], true
}

// ListMulticastChannels returns the whole catalog, paging channels first.
func (s *Store) ListMulticastChannels(ctx context.Context) ([]phonebook.Channel, error) {
	return catalogChannels(ctx, s.q(ctx), "", false)
}

// GetMulticastChannel returns one catalog channel.
func (s *Store) GetMulticastChannel(ctx context.Context, id int64) (phonebook.Channel, error) {
	c, err := scanChannel(s.q(ctx).QueryRowContext(ctx, `SELECT `+channelCols+` FROM multicast_channels WHERE id = $1 AND group_id IS NULL`, id))
	return c, mapErr(err)
}

// checkChannelAddress refuses an address and port another catalog channel
// already has.
func checkChannelAddress(ctx context.Context, tx *sql.Tx, id int64, in phonebook.ChannelInput) error {
	var label string
	err := tx.QueryRowContext(ctx, `SELECT label FROM multicast_channels
		WHERE group_id IS NULL AND address = $1 AND port = $2 AND id <> $3 ORDER BY id LIMIT 1`,
		in.Address, in.Port, id).Scan(&label)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return fieldError("address", fmt.Sprintf("%s:%d is already used by channel %q", in.Address, in.Port, label))
}

func channelEnabled(in phonebook.ChannelInput) bool { return in.Enabled == nil || *in.Enabled }

// CreateMulticastChannel adds a catalog channel. It is a configuration
// change: hello-sip sends pages to paging channels.
func (s *Store) CreateMulticastChannel(ctx context.Context, actor string, in phonebook.ChannelInput) (phonebook.Channel, error) {
	var out phonebook.Channel
	err := s.configChange(ctx, actor, "create", "multicast_channel", nil, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM multicast_channels WHERE group_id IS NULL`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxCatalogChannels {
			return 0, conflict("the catalog already has %d multicast channels, the most it takes", MaxCatalogChannels)
		}
		if err := checkChannelAddress(ctx, tx, 0, in); err != nil {
			return 0, err
		}
		var err error
		out, err = scanChannel(tx.QueryRowContext(ctx, `INSERT INTO multicast_channels (label, address, port, codec, priority, kind, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+channelCols,
			in.Label, in.Address, in.Port, in.Codec, in.Priority, in.Kind, channelEnabled(in)))
		return out.ID, err
	})
	return out, err
}

// channelUsers names what refers to a catalog channel in a way its kind
// must keep: paging groups and phone groups for a paging channel, phone
// groups and phones for a PTT channel.
func channelUsers(ctx context.Context, q querier, id int64, kind string) ([]string, error) {
	query := `SELECT 'paging group ' || name FROM paging_groups WHERE multicast_channel_id = $1
		UNION ALL SELECT 'phone group ' || g.name FROM phone_group_multicast l JOIN phone_groups g ON g.id = l.group_id WHERE l.channel_id = $1`
	if kind == phonebook.KindPTT {
		query = `SELECT 'phone group ' || name FROM phone_groups WHERE ptt_channel_id = $1
			UNION ALL SELECT 'phone ' || COALESCE(NULLIF(label, ''), mac) FROM phones WHERE ptt_channel_id = $1`
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY 1 LIMIT 6`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func usedBy(label string, users []string) error {
	return conflict("multicast channel %q is used by %s; change them first", label, joinUsers(users))
}

func joinUsers(users []string) string {
	out := ""
	for i, u := range users {
		switch {
		case i == 5:
			return out + " and more"
		case i > 0:
			out += ", "
		}
		out += u
	}
	return out
}

// UpdateMulticastChannel replaces a catalog channel. Its kind changes only
// while nothing that needs the old kind uses it.
func (s *Store) UpdateMulticastChannel(ctx context.Context, actor string, id int64, in phonebook.ChannelInput) (phonebook.Channel, error) {
	var out phonebook.Channel
	err := s.configChange(ctx, actor, "update", "multicast_channel", nil, func(tx *sql.Tx) (int64, error) {
		cur, err := scanChannel(tx.QueryRowContext(ctx, `SELECT `+channelCols+` FROM multicast_channels
			WHERE id = $1 AND group_id IS NULL FOR UPDATE`, id))
		if err != nil {
			return id, mapErr(err)
		}
		if cur.Kind != in.Kind {
			users, err := channelUsers(ctx, tx, id, cur.Kind)
			if err != nil {
				return id, err
			}
			if len(users) > 0 {
				return id, usedBy(cur.Label, users)
			}
		}
		if err := checkChannelAddress(ctx, tx, id, in); err != nil {
			return id, err
		}
		out, err = scanChannel(tx.QueryRowContext(ctx, `UPDATE multicast_channels SET label = $2, address = $3, port = $4,
			codec = $5, priority = $6, kind = $7, enabled = $8 WHERE id = $1 RETURNING `+channelCols,
			id, in.Label, in.Address, in.Port, in.Codec, in.Priority, in.Kind, channelEnabled(in)))
		return id, err
	})
	return out, err
}

// DeleteMulticastChannel removes a catalog channel. A paging group that
// sends to it refuses the delete; phone groups and phones drop it.
func (s *Store) DeleteMulticastChannel(ctx context.Context, actor string, id int64) error {
	return s.configChange(ctx, actor, "delete", "multicast_channel", nil, func(tx *sql.Tx) (int64, error) {
		var label string
		if err := tx.QueryRowContext(ctx, `SELECT label FROM multicast_channels WHERE id = $1 AND group_id IS NULL FOR UPDATE`, id).Scan(&label); err != nil {
			return id, mapErr(err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT 'paging group ' || name FROM paging_groups WHERE multicast_channel_id = $1 ORDER BY 1 LIMIT 6`, id)
		if err != nil {
			return id, err
		}
		var users []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return id, err
			}
			users = append(users, n)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return id, err
		}
		if len(users) > 0 {
			return id, usedBy(label, users)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM multicast_channels WHERE id = $1`, id)
		return id, err
	})
}

// checkCatalogChannel refuses a channel id that is not an enabled catalog
// channel of the kind.
func checkCatalogChannel(ctx context.Context, q querier, path string, id int64, kind string) error {
	var k string
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT kind, enabled FROM multicast_channels WHERE id = $1 AND group_id IS NULL`, id).Scan(&k, &enabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fieldError(path, "no such multicast channel")
	case err != nil:
		return err
	case k != kind:
		return fieldError(path, "must be a "+kindName(kind)+" channel; channel "+strconv.FormatInt(id, 10)+" is a "+kindName(k)+" channel")
	case !enabled:
		return fieldError(path, "the multicast channel is disabled")
	}
	return nil
}

// checkPTTChannel refuses a PTT channel override that is not an enabled
// catalog PTT channel.
func checkPTTChannel(ctx context.Context, q querier, path string, id int64) error {
	return checkCatalogChannel(ctx, q, path, id, phonebook.KindPTT)
}

func kindName(kind string) string {
	if kind == phonebook.KindPTT {
		return "PTT"
	}
	return kind
}
