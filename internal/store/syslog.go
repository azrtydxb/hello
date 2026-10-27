package store

// Phone syslog (phone-feature-parity S-27): which phone a source address
// belongs to, and what a phone's group tells it to log.

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// PhoneIPs maps each address a phone last fetched its configuration from to
// that phone. An address used by more than one phone (a NAT) is left out, so
// its syslog lines are kept as unknown instead of being given to a guess.
func (s *Store) PhoneIPs(ctx context.Context) (map[netip.Addr]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host(last_fetch_ip), min(id) FROM phones
		WHERE last_fetch_ip IS NOT NULL GROUP BY last_fetch_ip HAVING count(*) = 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[netip.Addr]int64{}
	for rows.Next() {
		var ip string
		var id int64
		if err := rows.Scan(&ip, &id); err != nil {
			return nil, err
		}
		if a, err := netip.ParseAddr(ip); err == nil {
			out[a.Unmap()] = id
		}
	}
	return out, rows.Err()
}

// syslogInfo is RenderData.Syslog: the sink address and level of a phone
// whose group turned syslog on, nil otherwise (including while the
// deployment has no syslog host).
func (s *Store) syslogInfo(ctx context.Context, phoneID int64) (*prov.SyslogInfo, error) {
	if s.prov.SyslogHost == "" {
		return nil, nil
	}
	var enabled bool
	var level string
	err := s.db.QueryRowContext(ctx, `SELECT g.syslog_enabled, g.syslog_level
		FROM phones p JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&enabled, &level)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { // a phone without a group
			return nil, nil
		}
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	n := 0
	for i, l := range phonebook.SyslogLevels {
		if l == level {
			n = i + 1
		}
	}
	return &prov.SyslogInfo{Host: s.prov.SyslogHost, Level: n}, nil
}
