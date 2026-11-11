package store

// Locations, the E911 switch and alarm destination of a phone group, and the
// emergency settings (migration 00073, spec phone-feature-parity S-28 and
// S-29). hello-sip reads all of it from the snapshot (the alarm destination,
// the location of a device for the Geolocation header, the trunk flag, the
// notify group), so every change bumps the configuration revision; the
// provisioning reads the same rows when it renders a phone, and the HELD
// server answers from them.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/location"
	"github.com/azrtydxb/hello/internal/routing"
)

// Location is a stored location node with its times.
type Location struct {
	location.Node
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MaxLocations bounds the tree.
const MaxLocations = 5000

const locationCols = `id, kind, name, parent_id, country, state, city, street, house_number, postal_code,
	latitude, longitude, radius_m, created_at, updated_at`

func scanLocation(r interface{ Scan(...any) error }) (Location, error) {
	var (
		l          Location
		parent     sql.NullInt64
		lat, lon   sql.NullFloat64
		radius     sql.NullFloat64
		created    time.Time
		updated    time.Time
		kind, name string
	)
	if err := r.Scan(&l.ID, &kind, &name, &parent, &l.Country, &l.State, &l.City, &l.Street, &l.HouseNumber,
		&l.PostalCode, &lat, &lon, &radius, &created, &updated); err != nil {
		return Location{}, err
	}
	l.Kind, l.Name, l.CreatedAt, l.UpdatedAt = kind, name, created, updated
	if parent.Valid {
		l.ParentID = &parent.Int64
	}
	if lat.Valid {
		l.Latitude, l.Longitude = &lat.Float64, &lon.Float64
	}
	if radius.Valid {
		l.RadiusM = &radius.Float64
	}
	return l, nil
}

func loadLocations(ctx context.Context, q querier) ([]Location, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+locationCols+` FROM locations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Location{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LocationNodes maps a list of locations by id, for location.Resolve.
func LocationNodes(ls []Location) map[int64]location.Node {
	m := make(map[int64]location.Node, len(ls))
	for _, l := range ls {
		m[l.ID] = l.Node
	}
	return m
}

// ListLocations returns every location, parents before the children they
// were created for (by id).
func (s *Store) ListLocations(ctx context.Context) ([]Location, error) {
	return loadLocations(ctx, s.q(ctx))
}

// GetLocation returns one location; ErrNotFound when there is none.
func (s *Store) GetLocation(ctx context.Context, id int64) (Location, error) {
	l, err := scanLocation(s.q(ctx).QueryRowContext(ctx, `SELECT `+locationCols+` FROM locations WHERE id = $1`, id))
	if err != nil {
		return Location{}, mapErr(err)
	}
	return l, nil
}

func locationProblems(prefix string, ps []location.Problem) error {
	if len(ps) == 0 {
		return nil
	}
	fs := make([]routing.FieldError, len(ps))
	for i, p := range ps {
		fs[i] = routing.FieldError{Path: prefix + p.Path, Message: p.Msg}
	}
	return &ValidationError{Fields: fs}
}

// writeLocation validates n against the stored tree and inserts it (id 0) or
// updates it, inside the configuration change.
func writeLocation(ctx context.Context, tx *sql.Tx, id int64, n location.Node) (int64, error) {
	all, err := loadLocations(ctx, tx)
	if err != nil {
		return 0, err
	}
	if id == 0 && len(all) >= MaxLocations {
		return 0, fieldError("name", fmt.Sprintf("at most %d locations", MaxLocations))
	}
	nodes := LocationNodes(all)
	if id != 0 {
		old, ok := nodes[id]
		if !ok {
			return 0, ErrNotFound
		}
		if old.Kind != n.Kind {
			return 0, fieldError("kind", "cannot be changed; create a new location")
		}
	}
	n.ID = id
	if err := locationProblems("", location.Validate(n)); err != nil {
		return 0, err
	}
	if err := locationProblems("", location.ValidateParent(nodes, n)); err != nil {
		return 0, err
	}
	n.Name = strings.TrimSpace(n.Name)
	args := []any{n.Kind, n.Name, n.ParentID, n.Country, n.State, n.City, n.Street, n.HouseNumber, n.PostalCode, n.Latitude, n.Longitude, n.RadiusM}
	if id == 0 {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO locations (kind, name, parent_id, country, state, city, street, house_number, postal_code, latitude, longitude, radius_m)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`, args...).Scan(&id)
		return id, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE locations SET kind = $1, name = $2, parent_id = $3, country = $4, state = $5, city = $6, street = $7,
			house_number = $8, postal_code = $9, latitude = $10, longitude = $11, radius_m = $12, updated_at = now()
		WHERE id = $13`, append(args, id)...)
	return id, err
}

// uniqueLocationName turns the name index's violation into a field error.
func uniqueLocationName(err error) error {
	if errors.Is(err, ErrConflict) && strings.Contains(err.Error(), "locations_name") {
		return fieldError("name", "another location with this parent has this name")
	}
	return err
}

// CreateLocation adds a location. ValidationError for an invalid one.
func (s *Store) CreateLocation(ctx context.Context, actor string, n location.Node) (Location, error) {
	var id int64
	err := s.provChange(ctx, actor, "create", "location", func(tx *sql.Tx) (string, bool, error) {
		var err error
		id, err = writeLocation(ctx, tx, 0, n)
		return strconv.FormatInt(id, 10), true, err
	})
	if err != nil {
		return Location{}, uniqueLocationName(err)
	}
	return s.GetLocation(ctx, id)
}

// UpdateLocation replaces a location's fields. The kind cannot change.
func (s *Store) UpdateLocation(ctx context.Context, actor string, id int64, n location.Node) (Location, error) {
	err := s.provChange(ctx, actor, "update", "location", func(tx *sql.Tx) (string, bool, error) {
		_, err := writeLocation(ctx, tx, id, n)
		return strconv.FormatInt(id, 10), true, err
	})
	if err != nil {
		return Location{}, uniqueLocationName(err)
	}
	return s.GetLocation(ctx, id)
}

// DeleteLocation removes a location that has nothing inside it and is
// assigned to nothing; the error names what is in the way.
func (s *Store) DeleteLocation(ctx context.Context, actor string, id int64) error {
	return s.provChange(ctx, actor, "delete", "location", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM locations WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			return rid, false, err
		}
		var kids int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM locations WHERE parent_id = $1`, id).Scan(&kids); err != nil {
			return rid, false, err
		}
		if kids > 0 {
			return rid, false, conflict("the location has %d floors or rooms inside it; delete or move them first", kids)
		}
		as, err := listAssignments(ctx, tx, &id)
		if err != nil {
			return rid, false, err
		}
		if len(as) > 0 {
			names := make([]string, 0, 5)
			for i, a := range as {
				if i == 5 {
					break
				}
				names = append(names, a.Kind+" "+a.Name)
			}
			return rid, false, conflict("the location is assigned to %d phone groups, extensions or phones (%s); unassign it first", len(as), strings.Join(names, ", "))
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM locations WHERE id = $1`, id)
		return rid, true, err
	})
}

// Assignment kinds.
const (
	AssignGroup     = "group"
	AssignExtension = "extension"
	AssignPhone     = "phone"
)

// LocationAssignment is what a phone group, extension or phone is assigned
// to; LocationID is nil when it is assigned to none.
type LocationAssignment struct {
	Kind       string `json:"kind"`
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	LocationID *int64 `json:"locationId"`
}

func listAssignments(ctx context.Context, q querier, only *int64) ([]LocationAssignment, error) {
	where := ""
	var args []any
	if only != nil {
		where, args = ` WHERE location_id = $1`, []any{*only}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT 'group', id, name, location_id FROM phone_groups`+where+`
		UNION ALL SELECT 'extension', id, number || CASE WHEN name = '' THEN '' ELSE ' (' || name || ')' END, location_id FROM extensions`+where+`
		UNION ALL SELECT 'phone', id, mac || CASE WHEN label = '' THEN '' ELSE ' (' || label || ')' END, location_id FROM phones`+where+`
		ORDER BY 1, 3`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []LocationAssignment{}
	for rows.Next() {
		var a LocationAssignment
		var loc sql.NullInt64
		if err := rows.Scan(&a.Kind, &a.ID, &a.Name, &loc); err != nil {
			return nil, err
		}
		if loc.Valid {
			a.LocationID = &loc.Int64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListLocationAssignments returns every phone group, extension and phone with
// the location it is assigned to.
func (s *Store) ListLocationAssignments(ctx context.Context) ([]LocationAssignment, error) {
	return listAssignments(ctx, s.q(ctx), nil)
}

// SetLocationAssignment assigns a phone group, extension or phone to a
// location, or to none when locationID is nil. ErrNotFound when the object
// or the location does not exist.
func (s *Store) SetLocationAssignment(ctx context.Context, actor, kind string, id int64, locationID *int64) error {
	table := map[string]string{AssignGroup: "phone_groups", AssignExtension: "extensions", AssignPhone: "phones"}[kind]
	if table == "" {
		return ErrNotFound
	}
	return s.provChange(ctx, actor, "update", "location_assignment", func(tx *sql.Tx) (string, bool, error) {
		rid := kind + ":" + strconv.FormatInt(id, 10)
		if locationID != nil {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM locations WHERE id = $1`, *locationID).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return rid, false, fieldError("locationId", "no such location")
				}
				return rid, false, err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE `+table+` SET location_id = $1 WHERE id = $2`, locationID, id)
		if err != nil {
			return rid, false, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return rid, false, ErrNotFound
		}
		return rid, true, nil
	})
}

// PhoneEmergency is a phone group's E911 switch and alarm destination.
type PhoneEmergency struct {
	E911Enabled bool   `json:"e911Enabled"`
	AlarmTarget string `json:"alarmTarget"`
}

// GetPhoneGroupEmergency returns a group's E911 switch and alarm destination.
func (s *Store) GetPhoneGroupEmergency(ctx context.Context, id int64) (PhoneEmergency, error) {
	var e PhoneEmergency
	err := s.q(ctx).QueryRowContext(ctx, `SELECT e911_enabled, alarm_target FROM phone_groups WHERE id = $1`, id).Scan(&e.E911Enabled, &e.AlarmTarget)
	return e, mapErr(err)
}

// requireDialTarget checks that number is an extension or a ring group.
func requireDialTarget(ctx context.Context, tx *sql.Tx, field, number string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM extensions WHERE number = $1 UNION ALL SELECT 1 FROM ring_groups WHERE name = $1 LIMIT 1`, number).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fieldError(field, "must be an extension or a ring group")
	}
	return err
}

// SetPhoneGroupEmergency sets a group's E911 switch and alarm destination (an
// extension or ring group number, empty for none).
func (s *Store) SetPhoneGroupEmergency(ctx context.Context, actor string, id int64, in PhoneEmergency) (PhoneEmergency, error) {
	err := s.provChange(ctx, actor, "update", "phone_group_emergency", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			return rid, false, err
		}
		if in.AlarmTarget != "" {
			if err := requireDialTarget(ctx, tx, "alarmTarget", in.AlarmTarget); err != nil {
				return rid, false, err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE phone_groups SET e911_enabled = $1, alarm_target = $2, updated_at = now() WHERE id = $3`,
			in.E911Enabled, in.AlarmTarget, id)
		return rid, true, err
	})
	if err != nil {
		return PhoneEmergency{}, err
	}
	return s.GetPhoneGroupEmergency(ctx, id)
}

// EmergencySettings are the settings of emergency calls that belong to no
// phone group.
type EmergencySettings struct {
	// NotifyGroup is the ring group (by number) told of every emergency call.
	NotifyGroup string `json:"notifyGroup"`
}

// GetEmergencySettings returns the emergency settings.
func (s *Store) GetEmergencySettings(ctx context.Context) (EmergencySettings, error) {
	var e EmergencySettings
	err := s.q(ctx).QueryRowContext(ctx, `SELECT notify_group FROM emergency_settings WHERE id = 1`).Scan(&e.NotifyGroup)
	return e, mapErr(err)
}

// PutEmergencySettings replaces the emergency settings. The notify group must
// be a ring group.
func (s *Store) PutEmergencySettings(ctx context.Context, actor string, in EmergencySettings) (EmergencySettings, error) {
	err := s.provChange(ctx, actor, "update", "emergency_settings", func(tx *sql.Tx) (string, bool, error) {
		if in.NotifyGroup != "" {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM ring_groups WHERE name = $1`, in.NotifyGroup).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return "1", false, fieldError("notifyGroup", "must be a ring group")
				}
				return "1", false, err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE emergency_settings SET notify_group = $1, updated_at = now() WHERE id = 1`, in.NotifyGroup)
		return "1", true, err
	})
	if err != nil {
		return EmergencySettings{}, err
	}
	return s.GetEmergencySettings(ctx)
}

// PhoneLocation is the location a phone resolves to and what it came from.
type PhoneLocation struct {
	// Source is phone, extension or group; empty when none is assigned.
	Source     string         `json:"source"`
	LocationID int64          `json:"locationId"`
	Place      location.Place `json:"place"`
	// E911 reports the phone's group has E911 switched on.
	E911 bool `json:"e911"`
}

// ErrNoLocation is returned when a phone has no usable location.
var ErrNoLocation = errors.New("store: the phone has no location")

// PhoneLocation resolves the location of a phone: its own, else its
// extension's, else its phone group's. ErrNotFound when the phone is gone,
// ErrNoLocation when none of the three is assigned or the chain is broken.
func (s *Store) PhoneLocation(ctx context.Context, phoneID int64) (PhoneLocation, error) {
	q := s.q(ctx)
	var pl, el, gl sql.NullInt64
	var e911 sql.NullBool
	err := q.QueryRowContext(ctx, `
		SELECT p.location_id, e.location_id, g.location_id, g.e911_enabled
		FROM phones p
		LEFT JOIN devices d ON d.id = p.device_id
		LEFT JOIN extensions e ON e.id = d.extension_id
		LEFT JOIN phone_groups g ON g.id = p.group_id
		WHERE p.id = $1`, phoneID).Scan(&pl, &el, &gl, &e911)
	if err != nil {
		return PhoneLocation{}, mapErr(err)
	}
	out := PhoneLocation{E911: e911.Valid && e911.Bool}
	id, src := location.Pick(pl.Int64, el.Int64, gl.Int64)
	if id == 0 {
		return out, ErrNoLocation
	}
	all, err := loadLocations(ctx, q)
	if err != nil {
		return out, err
	}
	place, ok := location.Resolve(LocationNodes(all), id)
	if !ok {
		return out, ErrNoLocation
	}
	out.Source, out.LocationID, out.Place = src, id, place
	return out, nil
}
