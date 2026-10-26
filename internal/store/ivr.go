package store

// IVR / auto-attendant menus (migration 00033). A menu plays an announcement,
// collects DTMF digits and sends the call to the destination of the chosen
// option. Every mutation runs through configChange, so it is audited, bumps
// the configuration revision and notifies hello-sip.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// IVR limits.
const (
	MaxIVRMenus   = 100
	MaxIVROptions = 12 // 0-9, * and #
)

// IVR destination kinds. The target of an extension or voicemail destination
// is an extension number, of ring_group a group name, of announcement an
// announcement name, of paging a page code, of ivr a menu name and of
// external a number routed through the outbound routes; hangup has none.
const (
	IVRHangup       = "hangup"
	IVRExtension    = "extension"
	IVRRingGroup    = "ring_group"
	IVRVoicemail    = "voicemail"
	IVRAnnouncement = "announcement"
	IVRPaging       = "paging"
	IVRMenuDest     = "ivr"
	IVRExternal     = "external"
)

// IVRDestinationKinds lists the destination kinds, in the order the UI shows
// them.
var IVRDestinationKinds = []string{IVRHangup, IVRExtension, IVRRingGroup, IVRVoicemail, IVRAnnouncement,
	IVRPaging, IVRMenuDest, IVRExternal, IVRQueueDest}

// IVRDestination is where an option, a timeout or an invalid choice sends
// the call.
type IVRDestination struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// IVROption is one digit of a menu and its destination.
type IVROption struct {
	Digit  string `json:"digit"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// IVRMenu is a menu as the API shows it. The prompt names are read-only
// display values of the announcement ids.
type IVRMenu struct {
	ID                int64          `json:"id"`
	Name              string         `json:"name"`
	Number            string         `json:"number"`
	Enabled           bool           `json:"enabled"`
	GreetingID        int64          `json:"greetingId"`
	GreetingName      string         `json:"greetingName"`
	InvalidPromptID   *int64         `json:"invalidPromptId"`
	InvalidPromptName string         `json:"invalidPromptName"`
	TimeoutPromptID   *int64         `json:"timeoutPromptId"`
	TimeoutPromptName string         `json:"timeoutPromptName"`
	DigitTimeout      int            `json:"digitTimeout"`
	InterDigitTimeout int            `json:"interDigitTimeout"`
	MaxRetries        int            `json:"maxRetries"`
	AllowDirectDial   bool           `json:"allowDirectDial"`
	Timeout           IVRDestination `json:"timeout"`
	Invalid           IVRDestination `json:"invalid"`
	Options           []IVROption    `json:"options"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

// IVRMenuInput is the writable shape of a menu; an update replaces the whole
// menu, its options included.
type IVRMenuInput struct {
	Name              string
	Number            string
	Enabled           bool
	GreetingID        int64
	InvalidPromptID   *int64
	TimeoutPromptID   *int64
	DigitTimeout      int
	InterDigitTimeout int
	MaxRetries        int
	AllowDirectDial   bool
	Timeout           IVRDestination
	Invalid           IVRDestination
	Options           []IVROption
}

const ivrCols = `m.id, m.name, m.number, m.enabled, m.greeting_id, g.name, m.invalid_prompt_id, COALESCE(i.name, ''),
	m.timeout_prompt_id, COALESCE(t.name, ''), m.digit_timeout, m.inter_digit_timeout, m.max_retries,
	m.allow_direct_dial, m.timeout_kind, m.timeout_target, m.invalid_kind, m.invalid_target, m.created_at, m.updated_at`

const ivrFrom = ` FROM ivr_menus m
	JOIN announcements g ON g.id = m.greeting_id
	LEFT JOIN announcements i ON i.id = m.invalid_prompt_id
	LEFT JOIN announcements t ON t.id = m.timeout_prompt_id `

func listIVRMenus(ctx context.Context, q querier, where string, args ...any) ([]IVRMenu, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+ivrCols+ivrFrom+where+` ORDER BY m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []IVRMenu{}
	var ids []int64
	for rows.Next() {
		var m IVRMenu
		if err := rows.Scan(&m.ID, &m.Name, &m.Number, &m.Enabled, &m.GreetingID, &m.GreetingName,
			&m.InvalidPromptID, &m.InvalidPromptName, &m.TimeoutPromptID, &m.TimeoutPromptName,
			&m.DigitTimeout, &m.InterDigitTimeout, &m.MaxRetries, &m.AllowDirectDial,
			&m.Timeout.Kind, &m.Timeout.Target, &m.Invalid.Kind, &m.Invalid.Target, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Options = []IVROption{}
		out = append(out, m)
		ids = append(ids, m.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	byID := map[int64]*IVRMenu{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	orows, err := q.QueryContext(ctx, `SELECT menu_id, digit, dest_kind, dest_target FROM ivr_menu_options
		WHERE menu_id = ANY($1) ORDER BY menu_id, digit`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = orows.Close() }()
	for orows.Next() {
		var (
			id int64
			o  IVROption
		)
		if err := orows.Scan(&id, &o.Digit, &o.Kind, &o.Target); err != nil {
			return nil, err
		}
		byID[id].Options = append(byID[id].Options, o)
	}
	return out, orows.Err()
}

// ListIVRMenus returns every IVR menu.
func (s *Store) ListIVRMenus(ctx context.Context) ([]IVRMenu, error) {
	return listIVRMenus(ctx, s.db, "")
}

// GetIVRMenu returns one IVR menu.
func (s *Store) GetIVRMenu(ctx context.Context, id int64) (IVRMenu, error) {
	ms, err := listIVRMenus(ctx, s.db, ` WHERE m.id = $1`, id)
	if err != nil {
		return IVRMenu{}, err
	}
	if len(ms) == 0 {
		return IVRMenu{}, ErrNotFound
	}
	return ms[0], nil
}

// ivrDest is one destination being checked, with the field path its error
// belongs to.
type ivrDest struct {
	path string
	IVRDestination
}

// destinations lists every destination of the input: the options, the
// timeout and the invalid-choice destination.
func (in IVRMenuInput) destinations() []ivrDest {
	out := []ivrDest{{"timeout", in.Timeout}, {"invalid", in.Invalid}}
	for i, o := range in.Options {
		out = append(out, ivrDest{fmt.Sprintf("options[%d]", i), IVRDestination{Kind: o.Kind, Target: o.Target}})
	}
	return out
}

// ValidIVRDigit reports whether d is a menu option digit.
func ValidIVRDigit(d string) bool {
	return len(d) == 1 && strings.Contains("0123456789*#", d)
}

// checkIVRDestination verifies that a destination names something that
// exists. self is the menu being written (its name, which may be new).
func checkIVRDestination(ctx context.Context, tx *sql.Tx, d ivrDest) error {
	exists := func(query, what string) error {
		var n int
		if err := tx.QueryRowContext(ctx, query, d.Target).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError(d.path+".target", what+" "+d.Target+" does not exist")
		}
		return nil
	}
	switch d.Kind {
	case IVRHangup:
		if d.Target != "" {
			return fieldError(d.path+".target", `must be empty when kind is "hangup"`)
		}
	case IVRExtension:
		return exists(`SELECT count(*) FROM extensions WHERE number = $1`, "extension")
	case IVRVoicemail:
		return exists(`SELECT count(*) FROM voicemail_boxes b JOIN extensions e ON e.id = b.extension_id
			WHERE e.number = $1`, "voicemail box of extension")
	case IVRRingGroup:
		return exists(`SELECT count(*) FROM ring_groups WHERE name = $1`, "ring group")
	case IVRAnnouncement:
		return exists(`SELECT count(*) FROM announcements WHERE name = $1`, "announcement")
	case IVRPaging:
		return exists(`SELECT count(*) FROM paging_groups WHERE code = $1`, "paging group with code")
	case IVRQueueDest:
		return exists(`SELECT count(*) FROM queues WHERE name = $1`, "queue")
	case IVRMenuDest:
		// A menu being created is not in the table yet; the caller handles
		// a reference to itself.
		return exists(`SELECT count(*) FROM ivr_menus WHERE name = $1`, "IVR menu")
	case IVRExternal:
		// The format is checked by the API; routing decides at call time.
	default:
		return fieldError(d.path+".kind", "is not a destination kind")
	}
	return nil
}

// checkIVRMenu verifies the references of a menu before it is written: the
// number is free, the destinations exist and the automatic transitions
// (timeout and invalid) do not loop.
func checkIVRMenu(ctx context.Context, tx *sql.Tx, id int64, in IVRMenuInput) error {
	if in.Number != "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM extensions WHERE number = $1)
			+ (SELECT count(*) FROM ring_groups WHERE name = $1)
			+ (SELECT count(*) FROM queues WHERE number = $1)`, in.Number).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fieldError("number", "is already an extension number, a ring group name or a queue number")
		}
		if err := checkNotSlot(ctx, tx, "number", in.Number); err != nil {
			return err
		}
	}
	selfRef := func(d IVRDestination) bool { return d.Kind == IVRMenuDest && d.Target == in.Name }
	for _, d := range in.destinations() {
		if d.Kind == IVRMenuDest && d.Target == in.Name {
			// A reference to the menu itself: allowed for an option (the
			// "repeat the menu" key), checked below for automatic exits.
			continue
		}
		if err := checkIVRDestination(ctx, tx, d); err != nil {
			return err
		}
	}
	if selfRef(in.Timeout) {
		return fieldError("timeout.target", "a menu cannot be its own timeout destination")
	}
	if selfRef(in.Invalid) {
		return fieldError("invalid.target", "a menu cannot be its own invalid-choice destination")
	}
	return checkIVRLoops(ctx, tx, id, in)
}

// checkIVRLoops refuses a menu whose automatic exits (timeout and invalid
// choice) lead back to itself through other menus: a caller who stays silent
// would be passed round for ever. A loop through options needs the caller to
// press a key each time, so it is allowed ("0 for the main menu").
func checkIVRLoops(ctx context.Context, tx *sql.Tx, id int64, in IVRMenuInput) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, timeout_kind, timeout_target, invalid_kind, invalid_target FROM ivr_menus WHERE id <> $1`, id)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	next := map[string][]string{}
	for rows.Next() {
		var name, tk, tt, ik, it string
		if err := rows.Scan(&name, &tk, &tt, &ik, &it); err != nil {
			return err
		}
		if tk == IVRMenuDest {
			next[name] = append(next[name], tt)
		}
		if ik == IVRMenuDest {
			next[name] = append(next[name], it)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range []struct {
		path string
		d    IVRDestination
	}{{"timeout.target", in.Timeout}, {"invalid.target", in.Invalid}} {
		if d.d.Kind != IVRMenuDest {
			continue
		}
		if reaches(next, d.d.Target, in.Name, map[string]bool{}) {
			return fieldError(d.path, fmt.Sprintf("menu %s would lead back to this menu through timeout or invalid-choice exits", d.d.Target))
		}
	}
	return nil
}

// reaches reports whether menu from leads, through automatic exits, to want.
func reaches(next map[string][]string, from, want string, seen map[string]bool) bool {
	if from == want {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	return slices.ContainsFunc(next[from], func(n string) bool { return reaches(next, n, want, seen) })
}

func writeIVROptions(ctx context.Context, tx *sql.Tx, id int64, in IVRMenuInput) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM ivr_menu_options WHERE menu_id = $1`, id); err != nil {
		return err
	}
	for _, o := range in.Options {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ivr_menu_options (menu_id, digit, dest_kind, dest_target)
			VALUES ($1, $2, $3, $4)`, id, o.Digit, o.Kind, o.Target); err != nil {
			return err
		}
	}
	return nil
}

// ivrFK turns an unknown announcement id into a field error.
func ivrFK(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		switch {
		case strings.Contains(pg.ConstraintName, "greeting"):
			return fieldError("greetingId", "names an announcement that does not exist")
		case strings.Contains(pg.ConstraintName, "invalid_prompt"):
			return fieldError("invalidPromptId", "names an announcement that does not exist")
		case strings.Contains(pg.ConstraintName, "timeout_prompt"):
			return fieldError("timeoutPromptId", "names an announcement that does not exist")
		}
	}
	return err
}

// CreateIVRMenu inserts a menu with its options.
func (s *Store) CreateIVRMenu(ctx context.Context, actor string, in IVRMenuInput, check Check) (IVRMenu, error) {
	var m IVRMenu
	err := s.configChange(ctx, actor, "create", "ivr_menu", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ivr_menus`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxIVRMenus {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d IVR menus", MaxIVRMenus)}
		}
		if err := checkIVRMenu(ctx, tx, 0, in); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO ivr_menus (name, number, enabled, greeting_id, invalid_prompt_id,
				timeout_prompt_id, digit_timeout, inter_digit_timeout, max_retries, allow_direct_dial,
				timeout_kind, timeout_target, invalid_kind, invalid_target)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
			in.Name, in.Number, in.Enabled, in.GreetingID, in.InvalidPromptID, in.TimeoutPromptID,
			in.DigitTimeout, in.InterDigitTimeout, in.MaxRetries, in.AllowDirectDial,
			in.Timeout.Kind, in.Timeout.Target, in.Invalid.Kind, in.Invalid.Target).Scan(&id); err != nil {
			return 0, ivrFK(err)
		}
		if err := writeIVROptions(ctx, tx, id, in); err != nil {
			return id, err
		}
		ms, err := listIVRMenus(ctx, tx, ` WHERE m.id = $1`, id)
		if err == nil && len(ms) == 1 {
			m = ms[0]
		}
		return id, err
	})
	return m, err
}

// ivrReferences names what points at the menu name: inbound routes, ring
// groups, feature codes and other menus.
func ivrReferences(ctx context.Context, tx *sql.Tx, id int64, name string) ([]string, error) {
	var refs []string
	for _, q := range []struct {
		query, label string
		args         []any
	}{
		{`SELECT name FROM inbound_routes WHERE destination_kind = 'ivr' AND destination = $1`, "inbound route", []any{name}},
		{`SELECT name FROM ring_groups WHERE failure_kind = 'ivr' AND failure_target = $1`, "ring group", []any{name}},
		{`SELECT code FROM feature_codes WHERE action = 'ivr' AND argument = $1`, "feature code", []any{name}},
		{`SELECT m.name FROM ivr_menus m WHERE m.id <> $2 AND (
			(m.timeout_kind = 'ivr' AND m.timeout_target = $1) OR (m.invalid_kind = 'ivr' AND m.invalid_target = $1)
			OR EXISTS (SELECT 1 FROM ivr_menu_options o WHERE o.menu_id = m.id AND o.dest_kind = 'ivr' AND o.dest_target = $1))`,
			"IVR menu", []any{name, id}},
	} {
		rows, err := tx.QueryContext(ctx, q.query, q.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return nil, err
			}
			refs = append(refs, q.label+" "+n)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func inUseBy(what string, refs []string) error {
	if len(refs) > 5 {
		refs = append(refs[:5:5], fmt.Sprintf("%d more", len(refs)-5))
	}
	return &InUseError{Msg: fmt.Sprintf("%s is used by %s; change or delete them first", what, strings.Join(refs, ", "))}
}

// UpdateIVRMenu replaces a menu. Renaming a menu that something points at is
// refused: the references name it.
func (s *Store) UpdateIVRMenu(ctx context.Context, actor string, id int64, in IVRMenuInput, check Check) (IVRMenu, error) {
	var m IVRMenu
	err := s.configChange(ctx, actor, "update", "ivr_menu", check, func(tx *sql.Tx) (int64, error) {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM ivr_menus WHERE id = $1 FOR UPDATE`, id).Scan(&cur); err != nil {
			return id, err
		}
		if cur != in.Name {
			refs, err := ivrReferences(ctx, tx, id, cur)
			if err != nil {
				return id, err
			}
			if len(refs) > 0 {
				return id, inUseBy("IVR menu "+cur+", which cannot be renamed while it", refs)
			}
		}
		if err := checkIVRMenu(ctx, tx, id, in); err != nil {
			return id, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ivr_menus SET name = $2, number = $3, enabled = $4, greeting_id = $5,
				invalid_prompt_id = $6, timeout_prompt_id = $7, digit_timeout = $8, inter_digit_timeout = $9,
				max_retries = $10, allow_direct_dial = $11, timeout_kind = $12, timeout_target = $13,
				invalid_kind = $14, invalid_target = $15, updated_at = now()
			WHERE id = $1`, id, in.Name, in.Number, in.Enabled, in.GreetingID, in.InvalidPromptID, in.TimeoutPromptID,
			in.DigitTimeout, in.InterDigitTimeout, in.MaxRetries, in.AllowDirectDial,
			in.Timeout.Kind, in.Timeout.Target, in.Invalid.Kind, in.Invalid.Target); err != nil {
			return id, ivrFK(err)
		}
		if err := writeIVROptions(ctx, tx, id, in); err != nil {
			return id, err
		}
		ms, err := listIVRMenus(ctx, tx, ` WHERE m.id = $1`, id)
		if err == nil && len(ms) == 1 {
			m = ms[0]
		}
		return id, err
	})
	return m, err
}

// DeleteIVRMenu removes a menu and, by cascade, its options. A menu that an
// inbound route, a ring group, a feature code or another menu names is
// refused.
func (s *Store) DeleteIVRMenu(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "ivr_menu", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM ivr_menus WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		refs, err := ivrReferences(ctx, tx, id, name)
		if err != nil {
			return id, err
		}
		if len(refs) > 0 {
			return id, inUseBy("IVR menu "+name, refs)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM ivr_menus WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// IVRMenuNameExists reports whether a menu with that name exists, for the
// settings that name one (ring group failover, feature codes).
func (s *Store) IVRMenuNameExists(ctx context.Context, name string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ivr_menus WHERE name = $1`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// refuseIfIVRPoints returns an *InUseError naming the IVR menus whose
// options or timeout and invalid exits point at target with one of kinds. It
// guards the extension, ring group, paging group and announcement a menu
// names against being deleted or renumbered under it.
func refuseIfIVRPoints(ctx context.Context, tx *sql.Tx, what, target string, kinds ...string) error {
	k2 := kinds[0]
	if len(kinds) > 1 {
		k2 = kinds[1]
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.name FROM ivr_menus m WHERE
		(m.timeout_kind IN ($1, $2) AND m.timeout_target = $3)
		OR (m.invalid_kind IN ($1, $2) AND m.invalid_target = $3)
		OR EXISTS (SELECT 1 FROM ivr_menu_options o WHERE o.menu_id = m.id AND o.dest_kind IN ($1, $2) AND o.dest_target = $3)
		ORDER BY m.name`, kinds[0], k2, target)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var refs []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		refs = append(refs, "IVR menu "+n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return inUseBy(what, refs)
	}
	return nil
}

// requireIVRMenuTx refuses a reference to a menu that does not exist. It runs
// in the configuration change's transaction, under the configuration lock,
// so a menu cannot be deleted between the check and the write that names it.
func requireIVRMenuTx(ctx context.Context, tx *sql.Tx, path, name string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ivr_menus WHERE name = $1`, name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fieldError(path, fmt.Sprintf("IVR menu %q does not exist", name))
	}
	return nil
}

// refuseIVRNumber refuses an extension number or ring group name that an IVR
// menu already answers on: the menu's number is matched first, so the
// extension or group would never ring.
func refuseIVRNumber(ctx context.Context, tx *sql.Tx, path, number string) error {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM ivr_menus WHERE number = $1 AND number <> ''`, number).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return fieldError(path, "is already the number of IVR menu "+name)
}
