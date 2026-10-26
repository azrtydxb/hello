package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
)

// querier is what both *sql.DB and *sql.Tx offer.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// trunkAAD binds a sealed trunk password to its row (contract 3).
func trunkAAD(id int64) string { return "trunk:" + strconv.FormatInt(id, 10) }

// Dialing is a trunk's internal-number dialing policy (trunk plan contract
// 5) as the API shows it and as the internal_dialing column stores it.
type Dialing struct {
	Mode     string   `json:"mode"` // "off" (default) | "patterns" | "all"
	Patterns []string `json:"patterns"`
}

// dialing returns d as the routing engine sees it.
func (d Dialing) dialing() routing.Dialing {
	return routing.Dialing{Mode: d.Mode, Patterns: d.Patterns}
}

// Trunk is a trunk as the API shows it. The password is never read into
// this type; HasPassword reports whether one is set.
type Trunk struct {
	ID              int64                 `json:"id"`
	Name            string                `json:"name"`
	Mode            string                `json:"mode"`
	Username        string                `json:"username"`
	HasPassword     bool                  `json:"hasPassword"`
	Realm           string                `json:"realm"`
	FromDomain      string                `json:"fromDomain"`
	RegisterExpires int                   `json:"registerExpires"` // seconds
	OptionsInterval int                   `json:"optionsInterval"` // seconds
	SourceCIDRs     []string              `json:"sourceCidrs"`
	MaxCalls        int                   `json:"maxCalls"`
	DefaultCallerID string                `json:"defaultCallerId"`
	Enabled         bool                  `json:"enabled"`
	Destinations    []routing.Destination `json:"destinations"`
	InternalDialing Dialing               `json:"internalDialing"`
	// Codecs is the audio codec allow-list offered to the trunk, in order.
	Codecs    []string  `json:"codecs"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TrunkInput is the writable part of a trunk, without its password.
type TrunkInput struct {
	Name            string
	Mode            string
	Username        string
	Realm           string
	FromDomain      string
	RegisterExpires int
	OptionsInterval int
	SourceCIDRs     []string
	MaxCalls        int
	DefaultCallerID string
	Enabled         bool
	Destinations    []routing.Destination
	InternalDialing Dialing
	Codecs          []string
}

// Input returns t's writable fields.
func (t Trunk) Input() TrunkInput {
	return TrunkInput{
		Name: t.Name, Mode: t.Mode, Username: t.Username, Realm: t.Realm, FromDomain: t.FromDomain,
		RegisterExpires: t.RegisterExpires, OptionsInterval: t.OptionsInterval,
		SourceCIDRs: slices.Clone(t.SourceCIDRs), MaxCalls: t.MaxCalls, DefaultCallerID: t.DefaultCallerID,
		Enabled: t.Enabled, Destinations: slices.Clone(t.Destinations),
		InternalDialing: Dialing{Mode: t.InternalDialing.Mode, Patterns: slices.Clone(t.InternalDialing.Patterns)},
		Codecs:          slices.Clone(t.Codecs),
	}
}

// PasswordChange says what an update does to a trunk password: nothing
// (Set false), clear it (Set, Value ""), or replace it.
type PasswordChange struct {
	Set   bool
	Value string
}

const trunkCols = `id, name, mode, username, password_enc IS NOT NULL, realm, from_domain, register_expires,
	options_interval, to_json(source_cidrs::text[]), max_calls, default_caller_id, enabled, to_json(internal_dialing),
	to_json(codecs), created_at, updated_at`

func scanTrunk(r interface{ Scan(...any) error }) (Trunk, error) {
	var t Trunk
	var cidrs, dialing, codecs []byte
	if err := r.Scan(&t.ID, &t.Name, &t.Mode, &t.Username, &t.HasPassword, &t.Realm, &t.FromDomain, &t.RegisterExpires,
		&t.OptionsInterval, &cidrs, &t.MaxCalls, &t.DefaultCallerID, &t.Enabled, &dialing, &codecs, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return t, err
	}
	if err := json.Unmarshal(codecs, &t.Codecs); err != nil || len(t.Codecs) == 0 {
		t.Codecs = slices.Clone(routing.DefaultTrunkCodecs)
	}
	t.SourceCIDRs = []string{}
	if err := json.Unmarshal(cidrs, &t.SourceCIDRs); err != nil {
		return t, err
	}
	if err := json.Unmarshal(dialing, &t.InternalDialing); err != nil {
		return t, fmt.Errorf("store: decode internal_dialing: %w", err)
	}
	if t.InternalDialing.Mode == "" {
		t.InternalDialing.Mode = "off"
	}
	if t.InternalDialing.Patterns == nil {
		t.InternalDialing.Patterns = []string{}
	}
	return t, nil
}

// destinations returns each listed trunk's destinations in saved order.
func destinations(ctx context.Context, q querier, ids []int64) (map[int64][]routing.Destination, error) {
	rows, err := q.QueryContext(ctx, `SELECT trunk_id, host, port, priority, weight FROM trunk_destinations
		WHERE trunk_id = ANY($1) ORDER BY trunk_id, id`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]routing.Destination{}
	for rows.Next() {
		var id int64
		var d routing.Destination
		if err := rows.Scan(&id, &d.Host, &d.Port, &d.Priority, &d.Weight); err != nil {
			return nil, err
		}
		out[id] = append(out[id], d)
	}
	return out, rows.Err()
}

func listTrunks(ctx context.Context, q querier, where, lock string, args ...any) ([]Trunk, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+trunkCols+` FROM trunks `+where+` ORDER BY id `+lock, args...)
	if err != nil {
		return nil, err
	}
	out := []Trunk{}
	var ids []int64
	for rows.Next() {
		t, err := scanTrunk(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, t)
		ids = append(ids, t.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dests, err := destinations(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Destinations = dests[out[i].ID]
		if out[i].Destinations == nil {
			out[i].Destinations = []routing.Destination{}
		}
	}
	return out, nil
}

// ListTrunks returns every trunk ordered by id.
func (s *Store) ListTrunks(ctx context.Context) ([]Trunk, error) {
	return listTrunks(ctx, s.db, "", "")
}

// GetTrunk returns one trunk.
func (s *Store) GetTrunk(ctx context.Context, id int64) (Trunk, error) {
	return getTrunk(ctx, s.db, id, "")
}

func getTrunk(ctx context.Context, q querier, id int64, suffix string) (Trunk, error) {
	ts, err := listTrunks(ctx, q, "WHERE id = $1", suffix, id)
	if err != nil {
		return Trunk{}, err
	}
	if len(ts) == 0 {
		return Trunk{}, ErrNotFound
	}
	return ts[0], nil
}

// setPassword seals password for trunk id and stores it, or clears it when
// password is "". The sealed value must open again under the same key
// before it is stored.
func (s *Store) setPassword(ctx context.Context, tx *sql.Tx, id int64, password string) error {
	var sealed []byte
	if password != "" {
		if s.box == nil {
			return errors.New("store: no secret box configured for trunk passwords")
		}
		var err error
		if sealed, err = s.box.Seal(password, trunkAAD(id)); err != nil {
			return fmt.Errorf("store: seal trunk %d password: %w", id, err)
		}
		if back, err := s.box.Open(sealed, trunkAAD(id)); err != nil || back != password {
			return fmt.Errorf("store: trunk %d password does not round-trip under HELLO_SECRET_KEY", id)
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE trunks SET password_enc = $2 WHERE id = $1`, id, sealed)
	return err
}

func writeDestinations(ctx context.Context, tx *sql.Tx, id int64, ds []routing.Destination) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM trunk_destinations WHERE trunk_id = $1`, id); err != nil {
		return err
	}
	for _, d := range ds {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trunk_destinations (trunk_id, host, port, priority, weight)
			VALUES ($1, $2, $3, $4, $5)`, id, d.Host, d.Port, d.Priority, d.Weight); err != nil {
			return err
		}
	}
	return nil
}

// CreateTrunk inserts a trunk, then seals its password with the new id in
// the same transaction.
func (s *Store) CreateTrunk(ctx context.Context, actor string, in TrunkInput, password string, check Check) (Trunk, error) {
	var t Trunk
	err := s.configChange(ctx, actor, "create", "trunk", check, func(tx *sql.Tx) (int64, error) {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO trunks (name, mode, username, realm, from_domain, register_expires,
				options_interval, source_cidrs, max_calls, default_caller_id, enabled, internal_dialing, codecs)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::cidr[], $9, $10, $11, $12::jsonb, $13::jsonb) RETURNING id`,
			in.Name, in.Mode, in.Username, in.Realm, in.FromDomain, in.RegisterExpires, in.OptionsInterval,
			nonNil(in.SourceCIDRs), in.MaxCalls, in.DefaultCallerID, in.Enabled, dialingArg(in.InternalDialing),
			codecsArg(in.Codecs)).Scan(&id); err != nil {
			return 0, err
		}
		if err := s.setPassword(ctx, tx, id, password); err != nil {
			return id, err
		}
		if err := writeDestinations(ctx, tx, id, in.Destinations); err != nil {
			return id, err
		}
		var err error
		t, err = getTrunk(ctx, tx, id, "")
		return id, err
	})
	return t, err
}

// UpdateTrunk locks trunk id, lets apply change its fields (apply learns
// whether the trunk will have a password and returns field errors), then
// writes the result and applies pw.
func (s *Store) UpdateTrunk(ctx context.Context, actor string, id int64, pw PasswordChange,
	apply func(in *TrunkInput, hasPassword bool) []routing.FieldError, check Check,
) (Trunk, error) {
	var t Trunk
	err := s.configChange(ctx, actor, "update", "trunk", check, func(tx *sql.Tx) (int64, error) {
		cur, err := getTrunk(ctx, tx, id, "FOR UPDATE")
		if err != nil {
			return id, err
		}
		in := cur.Input()
		has := cur.HasPassword
		if pw.Set {
			has = pw.Value != ""
		}
		if fields := apply(&in, has); len(fields) > 0 {
			return id, &ValidationError{Fields: fields}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE trunks SET name = $2, mode = $3, username = $4, realm = $5,
				from_domain = $6, register_expires = $7, options_interval = $8, source_cidrs = $9::cidr[],
				max_calls = $10, default_caller_id = $11, enabled = $12, internal_dialing = $13::jsonb,
				codecs = $14::jsonb, updated_at = now()
			WHERE id = $1`, id, in.Name, in.Mode, in.Username, in.Realm, in.FromDomain, in.RegisterExpires,
			in.OptionsInterval, nonNil(in.SourceCIDRs), in.MaxCalls, in.DefaultCallerID, in.Enabled,
			dialingArg(in.InternalDialing), codecsArg(in.Codecs)); err != nil {
			return id, err
		}
		if pw.Set {
			if err := s.setPassword(ctx, tx, id, pw.Value); err != nil {
				return id, err
			}
		}
		if err := writeDestinations(ctx, tx, id, in.Destinations); err != nil {
			return id, err
		}
		t, err = getTrunk(ctx, tx, id, "")
		return id, err
	})
	return t, err
}

// routeNames returns the first column of query, sorted.
func routeNames(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
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
	slices.Sort(out)
	return out, rows.Err()
}

// extensionRoutes names the routes that refer to the extension with this
// id — inbound routes ringing it and outbound routes restricted to it as a
// source — and locks the extension row.
func extensionRoutes(ctx context.Context, tx *sql.Tx, id int64) (string, []string, error) {
	var number string
	if err := tx.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1 FOR UPDATE`, id).Scan(&number); err != nil {
		return "", nil, err
	}
	routes, err := routeNames(ctx, tx, `
		SELECT name FROM inbound_routes WHERE destination_kind = 'extension' AND destination = $1
		UNION ALL SELECT name FROM outbound_routes WHERE $1 = ANY (source_extensions)`, number)
	return number, routes, err
}

// DeleteTrunk removes a trunk. A trunk an inbound or outbound route still
// names is an *InUseError naming them: deleting it would break routing (an
// outbound route would lose a trunk, and the schema would cascade-delete
// inbound routes).
func (s *Store) DeleteTrunk(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "trunk", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM trunks WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		routes, err := routeNames(ctx, tx, `
			SELECT r.name FROM outbound_routes r JOIN outbound_route_trunks t ON t.route_id = r.id WHERE t.trunk_id = $1
			UNION ALL SELECT name FROM inbound_routes WHERE trunk_id = $1`, id)
		if err != nil {
			return id, err
		}
		if len(routes) > 0 {
			return id, inUse("trunk "+strconv.Quote(name), routes)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM trunks WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// Outbound routes.

// OutboundRoute is an outbound route as the API shows it.
type OutboundRoute struct {
	ID                int64             `json:"id"`
	Position          int               `json:"position"`
	Name              string            `json:"name"`
	MatchKind         string            `json:"matchKind"`
	Match             string            `json:"match"`
	SourceExtensions  []string          `json:"sourceExtensions"`
	Schedule          *routing.Schedule `json:"schedule"`
	NumberTransform   routing.Transform `json:"numberTransform"`
	CallerIDTransform routing.Transform `json:"callerIdTransform"`
	Trunks            []int64           `json:"trunks"`
	FailoverCodes     []int             `json:"failoverCodes"`
	Emergency         bool              `json:"emergency"`
	Enabled           bool              `json:"enabled"`
}

// DefaultFailoverCodes are the SIP codes that fail over when a route sets
// none (spec S-4), matching the column default.
var DefaultFailoverCodes = []int{408, 480, 500, 502, 503, 504}

const outboundCols = `id, position, name, match_kind, match, to_json(source_extensions), schedule,
	number_transform, callerid_transform, to_json(failover_codes), emergency, enabled`

func scanOutbound(r interface{ Scan(...any) error }) (OutboundRoute, error) {
	var o OutboundRoute
	var exts, sched, num, cid, codes []byte
	if err := r.Scan(&o.ID, &o.Position, &o.Name, &o.MatchKind, &o.Match, &exts, &sched, &num, &cid, &codes,
		&o.Emergency, &o.Enabled); err != nil {
		return o, err
	}
	o.SourceExtensions, o.FailoverCodes = []string{}, []int{}
	return o, unmarshalAll(
		jsonField{exts, &o.SourceExtensions}, jsonField{sched, &o.Schedule}, jsonField{num, &o.NumberTransform},
		jsonField{cid, &o.CallerIDTransform}, jsonField{codes, &o.FailoverCodes})
}

type jsonField struct {
	raw []byte
	v   any
}

func unmarshalAll(fs ...jsonField) error {
	for _, f := range fs {
		if f.raw == nil {
			continue // SQL NULL
		}
		if err := json.Unmarshal(f.raw, f.v); err != nil {
			return fmt.Errorf("store: decode column: %w", err)
		}
	}
	return nil
}

func listOutbound(ctx context.Context, q querier, where, lock string, args ...any) ([]OutboundRoute, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+outboundCols+` FROM outbound_routes `+where+` ORDER BY position, id `+lock, args...)
	if err != nil {
		return nil, err
	}
	out := []OutboundRoute{}
	for rows.Next() {
		o, err := scanOutbound(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		o.Trunks = []int64{}
		out = append(out, o)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT route_id, trunk_id FROM outbound_route_trunks ORDER BY route_id, position`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	idx := map[int64]int{}
	for i, o := range out {
		idx[o.ID] = i
	}
	for rows.Next() {
		var route, trunk int64
		if err := rows.Scan(&route, &trunk); err != nil {
			return nil, err
		}
		if i, ok := idx[route]; ok {
			out[i].Trunks = append(out[i].Trunks, trunk)
		}
	}
	return out, rows.Err()
}

// ListOutboundRoutes returns every outbound route in position order.
func (s *Store) ListOutboundRoutes(ctx context.Context) ([]OutboundRoute, error) {
	return listOutbound(ctx, s.db, "", "")
}

// GetOutboundRoute returns one outbound route.
func (s *Store) GetOutboundRoute(ctx context.Context, id int64) (OutboundRoute, error) {
	return getOutbound(ctx, s.db, id, "")
}

func getOutbound(ctx context.Context, q querier, id int64, suffix string) (OutboundRoute, error) {
	os, err := listOutbound(ctx, q, "WHERE id = $1", suffix, id)
	if err != nil {
		return OutboundRoute{}, err
	}
	if len(os) == 0 {
		return OutboundRoute{}, ErrNotFound
	}
	return os[0], nil
}

// dialingArg encodes an internal dialing policy for the jsonb column: the
// mode always present ("off", never ""), patterns a JSON array. Marshalling
// cannot fail (strings and a string slice only).
func dialingArg(d Dialing) string {
	if d.Mode == "" {
		d.Mode = "off"
	}
	if d.Patterns == nil {
		d.Patterns = []string{}
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// codecsArg encodes a trunk's codec allow-list for the jsonb column; an
// empty list is the default, PCMU and PCMA.
func codecsArg(c []string) string {
	if len(c) == 0 {
		c = routing.DefaultTrunkCodecs
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// jsonArg encodes v for a jsonb parameter; a nil pointer is SQL NULL.
func jsonArg(v any) (any, error) {
	if s, ok := v.(*routing.Schedule); ok && s == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// checkTrunksExist reports each listed trunk id that does not exist.
func checkTrunksExist(ctx context.Context, tx *sql.Tx, path string, ids []int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM trunks WHERE id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var fields []routing.FieldError
	for i, id := range ids {
		if !found[id] {
			fields = append(fields, routing.FieldError{Path: fmt.Sprintf("%s[%d]", path, i), Message: fmt.Sprintf("trunk %d does not exist", id)})
		}
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func writeOutbound(ctx context.Context, tx *sql.Tx, id int64, o OutboundRoute) error {
	if err := checkTrunksExist(ctx, tx, "trunks", o.Trunks); err != nil {
		return err
	}
	sched, err := jsonArg(o.Schedule)
	if err != nil {
		return err
	}
	num, err := jsonArg(o.NumberTransform)
	if err != nil {
		return err
	}
	cid, err := jsonArg(o.CallerIDTransform)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbound_routes SET name = $2, match_kind = $3, match = $4,
			source_extensions = $5::text[], schedule = $6::jsonb, number_transform = $7::jsonb,
			callerid_transform = $8::jsonb, failover_codes = $9::int[], emergency = $10, enabled = $11
		WHERE id = $1`, id, o.Name, o.MatchKind, o.Match, nonNil(o.SourceExtensions), sched, num, cid,
		o.FailoverCodes, o.Emergency, o.Enabled); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbound_route_trunks WHERE route_id = $1`, id); err != nil {
		return err
	}
	for i, t := range o.Trunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbound_route_trunks (route_id, trunk_id, position) VALUES ($1, $2, $3)`,
			id, t, i+1); err != nil {
			return err
		}
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CreateOutboundRoute appends a route after the last one. Its ID and
// Position are assigned.
func (s *Store) CreateOutboundRoute(ctx context.Context, actor string, o OutboundRoute, check Check) (OutboundRoute, error) {
	var out OutboundRoute
	err := s.configChange(ctx, actor, "create", "outbound_route", check, func(tx *sql.Tx) (int64, error) {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO outbound_routes (position, name, match_kind, match)
			VALUES ((SELECT COALESCE(max(position), 0) + 1 FROM outbound_routes), $1, $2, $3) RETURNING id`,
			o.Name, o.MatchKind, o.Match).Scan(&id); err != nil {
			return 0, err
		}
		if err := writeOutbound(ctx, tx, id, o); err != nil {
			return id, err
		}
		var err error
		out, err = getOutbound(ctx, tx, id, "")
		return id, err
	})
	return out, err
}

// UpdateOutboundRoute locks route id, lets apply change it, and writes it.
func (s *Store) UpdateOutboundRoute(ctx context.Context, actor string, id int64,
	apply func(*OutboundRoute) []routing.FieldError, check Check,
) (OutboundRoute, error) {
	var out OutboundRoute
	err := s.configChange(ctx, actor, "update", "outbound_route", check, func(tx *sql.Tx) (int64, error) {
		cur, err := getOutbound(ctx, tx, id, "FOR UPDATE")
		if err != nil {
			return id, err
		}
		if fields := apply(&cur); len(fields) > 0 {
			return id, &ValidationError{Fields: fields}
		}
		if err := writeOutbound(ctx, tx, id, cur); err != nil {
			return id, err
		}
		out, err = getOutbound(ctx, tx, id, "")
		return id, err
	})
	return out, err
}

// DeleteOutboundRoute removes an outbound route.
func (s *Store) DeleteOutboundRoute(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "outbound_route", check,
		deleteByID(ctx, `DELETE FROM outbound_routes WHERE id = $1`, id))
}

func deleteByID(ctx context.Context, query string, id int64) func(*sql.Tx) (int64, error) {
	return func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, query, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	}
}

// Inbound routes.

// InboundRoute is an inbound route as the API shows it. A nil TrunkID
// matches calls from any trunk.
type InboundRoute struct {
	ID                int64             `json:"id"`
	Position          int               `json:"position"`
	Name              string            `json:"name"`
	DIDKind           string            `json:"didKind"`
	DID               string            `json:"did"`
	TrunkID           *int64            `json:"trunkId"`
	SIPDomain         string            `json:"sipDomain"`
	HeaderName        string            `json:"headerName"`
	HeaderRegex       string            `json:"headerRegex"`
	Schedule          *routing.Schedule `json:"schedule"`
	CallerIDTransform routing.Transform `json:"callerIdTransform"`
	DestinationKind   string            `json:"destinationKind"`
	Destination       string            `json:"destination"`
	Enabled           bool              `json:"enabled"`
	// MOHClassID is the music on hold class of calls this route delivers
	// (nil uses the global default); MOHRingback plays it to the caller
	// instead of ringback while the destination rings (migration 00021).
	MOHClassID  *int64 `json:"mohClassId"`
	MOHRingback bool   `json:"mohRingback"`
}

const inboundCols = `id, position, name, did_kind, did, trunk_id, sip_domain, header_name, header_regex, schedule,
	callerid_transform, destination_kind, destination, enabled, moh_class_id, moh_ringback`

func scanInbound(r interface{ Scan(...any) error }) (InboundRoute, error) {
	var in InboundRoute
	var trunk, moh sql.NullInt64
	var sched, cid []byte
	if err := r.Scan(&in.ID, &in.Position, &in.Name, &in.DIDKind, &in.DID, &trunk, &in.SIPDomain, &in.HeaderName,
		&in.HeaderRegex, &sched, &cid, &in.DestinationKind, &in.Destination, &in.Enabled, &moh, &in.MOHRingback); err != nil {
		return in, err
	}
	if moh.Valid {
		in.MOHClassID = &moh.Int64
	}
	if trunk.Valid {
		in.TrunkID = &trunk.Int64
	}
	return in, unmarshalAll(jsonField{sched, &in.Schedule}, jsonField{cid, &in.CallerIDTransform})
}

func listInbound(ctx context.Context, q querier, where, lock string, args ...any) ([]InboundRoute, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+inboundCols+` FROM inbound_routes `+where+` ORDER BY position, id `+lock, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []InboundRoute{}
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// ListInboundRoutes returns every inbound route in position order.
func (s *Store) ListInboundRoutes(ctx context.Context) ([]InboundRoute, error) {
	return listInbound(ctx, s.db, "", "")
}

// GetInboundRoute returns one inbound route.
func (s *Store) GetInboundRoute(ctx context.Context, id int64) (InboundRoute, error) {
	return getInbound(ctx, s.db, id, "")
}

func getInbound(ctx context.Context, q querier, id int64, suffix string) (InboundRoute, error) {
	rs, err := listInbound(ctx, q, "WHERE id = $1", suffix, id)
	if err != nil {
		return InboundRoute{}, err
	}
	if len(rs) == 0 {
		return InboundRoute{}, ErrNotFound
	}
	return rs[0], nil
}

func writeInbound(ctx context.Context, tx *sql.Tx, id int64, in InboundRoute) error {
	if in.TrunkID != nil {
		if err := checkTrunksExist(ctx, tx, "trunkId", []int64{*in.TrunkID}); err != nil {
			var v *ValidationError
			if errors.As(err, &v) {
				v.Fields[0].Path = "trunkId"
			}
			return err
		}
	}
	if err := checkMOHClass(ctx, tx, "mohClassId", in.MOHClassID); err != nil {
		return err
	}
	sched, err := jsonArg(in.Schedule)
	if err != nil {
		return err
	}
	cid, err := jsonArg(in.CallerIDTransform)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE inbound_routes SET name = $2, did_kind = $3, did = $4, trunk_id = $5,
			sip_domain = $6, header_name = $7, header_regex = $8, schedule = $9::jsonb, callerid_transform = $10::jsonb,
			destination_kind = $11, destination = $12, enabled = $13, moh_class_id = $14, moh_ringback = $15
		WHERE id = $1`, id, in.Name, in.DIDKind, in.DID, in.TrunkID, in.SIPDomain, in.HeaderName, in.HeaderRegex,
		sched, cid, in.DestinationKind, in.Destination, in.Enabled, in.MOHClassID, in.MOHRingback)
	return err
}

// CreateInboundRoute appends a route after the last one.
func (s *Store) CreateInboundRoute(ctx context.Context, actor string, in InboundRoute, check Check) (InboundRoute, error) {
	var out InboundRoute
	err := s.configChange(ctx, actor, "create", "inbound_route", check, func(tx *sql.Tx) (int64, error) {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO inbound_routes (position, name, did_kind, destination_kind, destination)
			VALUES ((SELECT COALESCE(max(position), 0) + 1 FROM inbound_routes), $1, $2, $3, $4) RETURNING id`,
			in.Name, in.DIDKind, in.DestinationKind, in.Destination).Scan(&id); err != nil {
			return 0, err
		}
		if err := writeInbound(ctx, tx, id, in); err != nil {
			return id, err
		}
		var err error
		out, err = getInbound(ctx, tx, id, "")
		return id, err
	})
	return out, err
}

// UpdateInboundRoute locks route id, lets apply change it, and writes it.
func (s *Store) UpdateInboundRoute(ctx context.Context, actor string, id int64,
	apply func(*InboundRoute) []routing.FieldError, check Check,
) (InboundRoute, error) {
	var out InboundRoute
	err := s.configChange(ctx, actor, "update", "inbound_route", check, func(tx *sql.Tx) (int64, error) {
		cur, err := getInbound(ctx, tx, id, "FOR UPDATE")
		if err != nil {
			return id, err
		}
		if fields := apply(&cur); len(fields) > 0 {
			return id, &ValidationError{Fields: fields}
		}
		if err := writeInbound(ctx, tx, id, cur); err != nil {
			return id, err
		}
		out, err = getInbound(ctx, tx, id, "")
		return id, err
	})
	return out, err
}

// DeleteInboundRoute removes an inbound route.
func (s *Store) DeleteInboundRoute(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "inbound_route", check,
		deleteByID(ctx, `DELETE FROM inbound_routes WHERE id = $1`, id))
}

// Route kinds for ReorderRoutes.
const (
	Outbound = "outbound"
	Inbound  = "inbound"
)

var routeTables = map[string]struct{ resource, lockIDs, setPosition string }{
	Outbound: {"outbound_route", `SELECT id FROM outbound_routes FOR UPDATE`, `UPDATE outbound_routes SET position = $1 WHERE id = $2`},
	Inbound:  {"inbound_route", `SELECT id FROM inbound_routes FOR UPDATE`, `UPDATE inbound_routes SET position = $1 WHERE id = $2`},
}

// ReorderRoutes gives the routes of kind positions 1..n in the order of
// ids, which must list every route of that kind exactly once. It runs in one
// transaction: the deferred unique constraint on position lets positions
// swap, and a failure leaves the old order intact.
func (s *Store) ReorderRoutes(ctx context.Context, actor, kind string, ids []int64, check Check) error {
	t, ok := routeTables[kind]
	if !ok {
		return fmt.Errorf("store: unknown route kind %q", kind)
	}
	return s.configChangeID(ctx, actor, "reorder", t.resource, check, func(tx *sql.Tx) (string, error) {
		rows, err := tx.QueryContext(ctx, t.lockIDs)
		if err != nil {
			return "", err
		}
		have := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return "", err
			}
			have[id] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		seen := map[int64]bool{}
		for i, id := range ids {
			if !have[id] || seen[id] {
				return "", fieldError(fmt.Sprintf("ids[%d]", i), "must list each existing route exactly once")
			}
			seen[id] = true
		}
		if len(seen) != len(have) {
			return "", fieldError("ids", fmt.Sprintf("must list all %d routes", len(have)))
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, t.setPosition, i+1, id); err != nil {
				return "", err
			}
		}
		return "order", nil
	})
}

// Routing configuration.

// RoutingSnapshot is the routing configuration as hello-sip's snapshot
// would load it. Misconfigured lists trunks whose sealed password does not
// open under this node's key.
type RoutingSnapshot struct {
	Config        routing.Config
	Misconfigured map[int64]bool
}

// RoutingConfig loads the routing configuration in one read-only,
// repeatable-read transaction, so it is consistent and writes nothing.
func (s *Store) RoutingConfig(ctx context.Context) (RoutingSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return RoutingSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.loadRouting(ctx, tx)
}

func (s *Store) loadRouting(ctx context.Context, q querier) (RoutingSnapshot, error) {
	snap := RoutingSnapshot{Misconfigured: map[int64]bool{}}
	cfg := &snap.Config
	trunks, err := listTrunks(ctx, q, "", "")
	if err != nil {
		return snap, err
	}
	sealed, err := sealedPasswords(ctx, q)
	if err != nil {
		return snap, err
	}
	if err := routingTrunks(s, trunks, sealed, snap, cfg); err != nil {
		return snap, err
	}
	outbound, err := listOutbound(ctx, q, "", "")
	if err != nil {
		return snap, err
	}
	for _, o := range outbound {
		cfg.Outbound = append(cfg.Outbound, routing.OutboundRoute{
			ID: o.ID, Position: o.Position, Name: o.Name, MatchKind: o.MatchKind, Match: o.Match,
			SourceExtensions: o.SourceExtensions, Schedule: o.Schedule, Number: o.NumberTransform,
			CallerID: o.CallerIDTransform, Trunks: o.Trunks, FailoverCodes: o.FailoverCodes,
			Emergency: o.Emergency, Enabled: o.Enabled,
		})
	}
	inbound, err := listInbound(ctx, q, "", "")
	if err != nil {
		return snap, err
	}
	for _, in := range inbound {
		r := routing.InboundRoute{
			ID: in.ID, Position: in.Position, Name: in.Name, DIDKind: in.DIDKind, DID: in.DID,
			SIPDomain: in.SIPDomain, HeaderName: in.HeaderName, HeaderRegex: in.HeaderRegex, Schedule: in.Schedule,
			CallerID: in.CallerIDTransform, DestinationKind: in.DestinationKind, Destination: in.Destination,
			Enabled: in.Enabled, MOHRingback: in.MOHRingback,
		}
		if in.MOHClassID != nil {
			r.MOHClassID = *in.MOHClassID
		}
		if in.TrunkID != nil {
			r.TrunkID = *in.TrunkID
		}
		cfg.Inbound = append(cfg.Inbound, r)
	}
	rows, err := q.QueryContext(ctx, `SELECT number, external_number FROM extensions`)
	if err != nil {
		return snap, err
	}
	defer func() { _ = rows.Close() }()
	cfg.Extensions = map[string]string{}
	for rows.Next() {
		var n, ext string
		if err := rows.Scan(&n, &ext); err != nil {
			return snap, err
		}
		cfg.Extensions[n] = ext
	}
	if err := rows.Err(); err != nil {
		return snap, err
	}
	_ = rows.Close()
	mrows, err := q.QueryContext(ctx, `SELECT name FROM ivr_menus`)
	if err != nil {
		return snap, err
	}
	cfg.IVRMenus = map[string]bool{}
	for mrows.Next() {
		var n string
		if err := mrows.Scan(&n); err != nil {
			_ = mrows.Close()
			return snap, err
		}
		cfg.IVRMenus[n] = true
	}
	_ = mrows.Close()
	if err := mrows.Err(); err != nil {
		return snap, err
	}
	crows, err := q.QueryContext(ctx, `SELECT number, name FROM conference_rooms`)
	if err != nil {
		return snap, err
	}
	cfg.Conferences = map[string]string{}
	for crows.Next() {
		var n, name string
		if err := crows.Scan(&n, &name); err != nil {
			_ = crows.Close()
			return snap, err
		}
		cfg.Conferences[n] = name
	}
	_ = crows.Close()
	if err := crows.Err(); err != nil {
		return snap, err
	}
	trows, err := q.QueryContext(ctx, `SELECT name FROM time_conditions`)
	if err != nil {
		return snap, err
	}
	defer func() { _ = trows.Close() }()
	cfg.TimeConditions = map[string]bool{}
	for trows.Next() {
		var n string
		if err := trows.Scan(&n); err != nil {
			return snap, err
		}
		cfg.TimeConditions[n] = true
	}
	return snap, trows.Err()
}

// routingTrunks converts the stored trunks to the engine's type. A trunk
// whose sealed password does not open, or whose stored internal dialing
// policy does not compile (only reachable by writing the column directly),
// is misconfigured: routing skips it, and the policy drops to "off" so the
// rest of the configuration still compiles.
func routingTrunks(s *Store, trunks []Trunk, sealed map[int64][]byte, snap RoutingSnapshot, cfg *routing.Config) error {
	for _, t := range trunks {
		rt := routing.Trunk{
			ID: t.ID, Name: t.Name, Mode: t.Mode, Username: t.Username, Realm: t.Realm, FromDomain: t.FromDomain,
			RegisterExpires: time.Duration(t.RegisterExpires) * time.Second,
			OptionsInterval: time.Duration(t.OptionsInterval) * time.Second,
			MaxCalls:        t.MaxCalls, DefaultCallerID: t.DefaultCallerID, Enabled: t.Enabled, Destinations: t.Destinations,
			InternalDialing: t.InternalDialing.dialing(),
			Codecs:          slices.Clone(t.Codecs),
		}
		if errs := routing.CompileDialing(rt.InternalDialing); len(errs) > 0 {
			snap.Misconfigured[t.ID] = true
			rt.InternalDialing = routing.Dialing{Mode: "off"}
		}
		for _, c := range t.SourceCIDRs {
			p, err := netip.ParsePrefix(c)
			if err != nil {
				return fmt.Errorf("store: trunk %d source cidr: %w", t.ID, err)
			}
			rt.SourceCIDRs = append(rt.SourceCIDRs, p)
		}
		if enc, ok := sealed[t.ID]; ok {
			if s.box == nil {
				snap.Misconfigured[t.ID] = true
			} else if pw, err := s.box.Open(enc, trunkAAD(t.ID)); err != nil {
				snap.Misconfigured[t.ID] = true
			} else {
				rt.Password = pw
			}
		}
		cfg.Trunks = append(cfg.Trunks, rt)
	}
	return nil
}

// sealedPasswords reads every set password ciphertext. It is used only to
// build a routing.Config; it never reaches an API type.
func sealedPasswords(ctx context.Context, q querier) (map[int64][]byte, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, password_enc FROM trunks WHERE password_enc IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]byte{}
	for rows.Next() {
		var id int64
		var enc []byte
		if err := rows.Scan(&id, &enc); err != nil {
			return nil, err
		}
		out[id] = enc
	}
	return out, rows.Err()
}
