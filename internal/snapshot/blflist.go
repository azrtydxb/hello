package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
)

// BLFListMax bounds the members of one resource list (RFC 4662): a phone has
// at most a few hundred BLF keys, and one list NOTIFY carries all of them.
const BLFListMax = 100

// WithBLFLists installs the BLF key lists of the provisioned phones, by
// device SIP username (tests and Load). A list is the extension numbers on
// the phone's BLF keys, in key order; the phone subscribes to it as one
// resource list (phone-feature-parity S-35).
func (s *Snapshot) WithBLFLists(lists map[string][]string) *Snapshot {
	s.blf = lists
	return s
}

// BLFList returns the BLF key list of the phone bound to the device, capped
// at BLFListMax; ok is false for a device without a provisioned phone.
func (s *Snapshot) BLFList(username string) (members []string, ok bool) {
	l, ok := s.blf[username]
	if !ok {
		return nil, false
	}
	return l[:min(len(l), BLFListMax)], true
}

// loadBLFLists reads the BLF keys of every enabled phone bound to a device.
func loadBLFLists(ctx context.Context, db Querier) (map[string][]string, error) {
	rows, err := db.Query(ctx, `SELECT d.sip_username, p.blf::text
FROM phones p JOIN devices d ON d.id = p.device_id WHERE p.enabled`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: blf lists: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var user, raw string
		if err := rows.Scan(&user, &raw); err != nil {
			return nil, fmt.Errorf("snapshot: scan blf list: %w", err)
		}
		var nums []string
		if err := json.Unmarshal([]byte(raw), &nums); err != nil {
			continue // a malformed list is no list
		}
		out[user] = nums
	}
	return out, rows.Err()
}
