package snapshot

import (
	"context"
	"fmt"

	"github.com/azrtydxb/hello/internal/location"
)

// Emergency and alarm data (migration 00073, spec phone-feature-parity S-28
// and S-29): the alarm destination and the location of every device, and
// the ring group told of emergency calls. hello-sip reads it on the call
// path, so it is compiled per revision and never read from PostgreSQL then.

// EmergencyData is the emergency configuration of a revision.
type EmergencyData struct {
	// Alarm maps a device's SIP username to the alarm destination of its
	// phone's group (an extension or ring group number); a device without
	// one has no entry.
	Alarm map[string]string
	// Places maps a device's SIP username to the resolved location of the
	// phone, else of its extension, else of its phone's group.
	Places map[string]location.Place
	// NotifyGroup is the ring group number told of every emergency call.
	NotifyGroup string
}

// WithEmergency installs the emergency data (tests and Load).
func (s *Snapshot) WithEmergency(d EmergencyData) *Snapshot {
	s.emergency = d
	return s
}

// AlarmTarget is the alarm destination of the device's phone group, "" for
// none.
func (s *Snapshot) AlarmTarget(username string) string { return s.emergency.Alarm[username] }

// DevicePlace is the location of the device's phone.
func (s *Snapshot) DevicePlace(username string) (location.Place, bool) {
	p, ok := s.emergency.Places[username]
	return p, ok
}

// EmergencyNotifyGroup is the ring group told of every emergency call, "" for
// none.
func (s *Snapshot) EmergencyNotifyGroup() string { return s.emergency.NotifyGroup }

func loadEmergency(ctx context.Context, db Querier) (EmergencyData, error) {
	d := EmergencyData{Alarm: map[string]string{}, Places: map[string]location.Place{}}
	if err := db.QueryRow(ctx, `SELECT notify_group FROM emergency_settings WHERE id = 1`).Scan(&d.NotifyGroup); err != nil {
		return d, fmt.Errorf("snapshot: emergency settings: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT id, kind, name, parent_id, country, state, city, street, house_number, postal_code,
		latitude, longitude, radius_m FROM locations`)
	if err != nil {
		return d, fmt.Errorf("snapshot: locations: %w", err)
	}
	nodes := map[int64]location.Node{}
	for rows.Next() {
		var n location.Node
		if err := rows.Scan(&n.ID, &n.Kind, &n.Name, &n.ParentID, &n.Country, &n.State, &n.City, &n.Street, &n.HouseNumber,
			&n.PostalCode, &n.Latitude, &n.Longitude, &n.RadiusM); err != nil {
			rows.Close()
			return d, fmt.Errorf("snapshot: scan location: %w", err)
		}
		nodes[n.ID] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, fmt.Errorf("snapshot: locations: %w", err)
	}
	drows, err := db.Query(ctx, `
		SELECT d.sip_username, COALESCE(p.location_id, 0), COALESCE(e.location_id, 0), COALESCE(g.location_id, 0), COALESCE(g.alarm_target, '')
		FROM devices d
		JOIN extensions e ON e.id = d.extension_id
		LEFT JOIN phones p ON p.device_id = d.id
		LEFT JOIN phone_groups g ON g.id = p.group_id
		WHERE d.enabled`)
	if err != nil {
		return d, fmt.Errorf("snapshot: device locations: %w", err)
	}
	defer drows.Close()
	for drows.Next() {
		var user, alarm string
		var pl, el, gl int64
		if err := drows.Scan(&user, &pl, &el, &gl, &alarm); err != nil {
			return d, fmt.Errorf("snapshot: scan device location: %w", err)
		}
		if alarm != "" {
			d.Alarm[user] = alarm
		}
		if id, _ := location.Pick(pl, el, gl); id != 0 {
			if p, ok := location.Resolve(nodes, id); ok {
				d.Places[user] = p
			}
		}
	}
	return d, drows.Err()
}
