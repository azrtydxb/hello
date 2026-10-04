package store

// Phase 4 PBX features: voicemail boxes and messages, ring/hunt groups,
// feature codes and the voicemail email queue. Every configuration mutation
// runs through configChange, so it is audited, bumps the configuration
// revision and notifies hello-sip like every other change.

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// VoicemailBox is an extension's voicemail box. The password hash is never
// read into this type; HasPassword reports whether one is set.
type VoicemailBox struct {
	ID                int64     `json:"id"`
	ExtensionID       int64     `json:"extensionId"`
	HasPassword       bool      `json:"hasPassword"`
	Email             string    `json:"email"`
	GreetingObject    string    `json:"greetingObject"`
	UnreachableObject string    `json:"unreachableObject"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

const boxCols = `id, extension_id, password_hash <> '', email, greeting_object, unreachable_object, created_at, updated_at`

// GetVoicemailBox returns the box of extensionID.
func (s *Store) GetVoicemailBox(ctx context.Context, extensionID int64) (VoicemailBox, error) {
	b, err := scanBox(s.db.QueryRowContext(ctx,
		`SELECT `+boxCols+` FROM voicemail_boxes WHERE extension_id = $1`, extensionID))
	return b, mapErr(err)
}

func scanBox(r interface{ Scan(...any) error }) (VoicemailBox, error) {
	var b VoicemailBox
	err := r.Scan(&b.ID, &b.ExtensionID, &b.HasPassword, &b.Email, &b.GreetingObject, &b.UnreachableObject,
		&b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// VoicemailBoxChange holds the fields of a box update; nil keeps one.
// Password is the new plain password, hashed before it is stored. Greeting
// and Unreachable are MinIO object keys already uploaded by the caller.
type VoicemailBoxChange struct {
	Email       *string
	Password    *string
	Greeting    *string
	Unreachable *string
}

// UpdateVoicemailBox changes the box of extensionID, creating the box if the
// extension predates it. Password is hashed with bcrypt.
func (s *Store) UpdateVoicemailBox(ctx context.Context, actor string, extensionID int64, c VoicemailBoxChange, check Check) (VoicemailBox, error) {
	var hash []byte
	if c.Password != nil {
		h, err := auth.HashPassword(*c.Password)
		if err != nil {
			return VoicemailBox{}, err
		}
		hash = []byte(h)
	}
	var b VoicemailBox
	err := s.configChangeID(ctx, actor, "update", "voicemail_box", check, func(tx *sql.Tx) (string, error) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO voicemail_boxes (extension_id) VALUES ($1)
			ON CONFLICT (extension_id) DO NOTHING`, extensionID); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE voicemail_boxes SET
				email = COALESCE($2, email),
				password_hash = COALESCE($3, password_hash),
				greeting_object = COALESCE($4, greeting_object),
				unreachable_object = COALESCE($5, unreachable_object),
				updated_at = now()
			WHERE extension_id = $1`,
			extensionID, c.Email, hash, c.Greeting, c.Unreachable); err != nil {
			return "", err
		}
		var err error
		b, err = scanBox(tx.QueryRowContext(ctx,
			`SELECT `+boxCols+` FROM voicemail_boxes WHERE extension_id = $1`, extensionID))
		return strconv.FormatInt(b.ID, 10), err
	})
	return b, err
}

// VoicemailMessage is one recorded message. Object is its MinIO key and is
// internal: clients play audio through the audio route.
type VoicemailMessage struct {
	ID          int64     `json:"id"`
	BoxID       int64     `json:"boxId"`
	Caller      string    `json:"caller"`
	DurationMs  int64     `json:"durationMs"`
	Heard       bool      `json:"heard"`
	EmailStatus string    `json:"emailStatus"`
	CreatedAt   time.Time `json:"createdAt"`
	// Object is the MinIO key; it stays internal (clients play audio through
	// the audio route).
	Object string `json:"-"`
}

const messageCols = `id, box_id, caller, duration_ms, heard, email_status, created_at, minio_object`

func scanMessage(r interface{ Scan(...any) error }) (VoicemailMessage, error) {
	var m VoicemailMessage
	err := r.Scan(&m.ID, &m.BoxID, &m.Caller, &m.DurationMs, &m.Heard, &m.EmailStatus, &m.CreatedAt, &m.Object)
	return m, err
}

// ListVoicemailMessages returns a box's messages, newest first; unheardOnly
// keeps the ones not played yet.
func (s *Store) ListVoicemailMessages(ctx context.Context, boxID int64, unheardOnly bool) ([]VoicemailMessage, error) {
	where := ` WHERE box_id = $1`
	if unheardOnly {
		where += ` AND NOT heard`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM voicemail_messages`+where+` ORDER BY id DESC`, boxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoicemailMessage{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetVoicemailMessage returns one message.
func (s *Store) GetVoicemailMessage(ctx context.Context, id int64) (VoicemailMessage, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+` FROM voicemail_messages WHERE id = $1`, id))
	return m, mapErr(err)
}

// UpdateVoicemailMessageHeard sets a message's heard flag (playing it in the
// UI, or marking it unheard again).
func (s *Store) UpdateVoicemailMessageHeard(ctx context.Context, actor string, id int64, heard bool) error {
	return s.configChange(ctx, actor, "update", "voicemail_message", nil, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `UPDATE voicemail_messages SET heard = $2 WHERE id = $1`, id, heard)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// DeleteVoicemailMessage removes a message row and returns its MinIO object
// key; the caller removes the object, so this package never depends on the
// object store.
func (s *Store) DeleteVoicemailMessage(ctx context.Context, actor string, id int64) (string, error) {
	var object string
	err := s.configChange(ctx, actor, "delete", "voicemail_message", nil, func(tx *sql.Tx) (int64, error) {
		if err := tx.QueryRowContext(ctx,
			`SELECT minio_object FROM voicemail_messages WHERE id = $1`, id).Scan(&object); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM voicemail_messages WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
	return object, err
}

// VoicemailCounts is a box's MWI summary: unheard and total messages.
func (s *Store) VoicemailCounts(ctx context.Context, boxID int64) (unheard, total int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE NOT heard), count(*) FROM voicemail_messages WHERE box_id = $1`, boxID).
		Scan(&unheard, &total)
	return unheard, total, err
}

// EmailJob is one message waiting for voicemail-to-email delivery.
type EmailJob struct {
	MessageID  int64
	To         string
	Caller     string
	DurationMs int64
	CreatedAt  time.Time
	Object     string
}

// PendingEmails returns messages with email still queued and an address to
// send to, oldest first. The status stays pending while a worker sends, so a
// second node's poll can pick the row again in that window: a duplicated
// email is preferred over a lost one.
func (s *Store) PendingEmails(ctx context.Context, limit int) ([]EmailJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, b.email, m.caller, m.duration_ms, m.created_at, m.minio_object
		FROM voicemail_messages m JOIN voicemail_boxes b ON b.id = m.box_id
		WHERE m.email_status = 'pending' AND b.email <> ''
		ORDER BY m.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []EmailJob{}
	for rows.Next() {
		var j EmailJob
		if err := rows.Scan(&j.MessageID, &j.To, &j.Caller, &j.DurationMs, &j.CreatedAt, &j.Object); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MarkEmail records a voicemail email's delivery result: "sent" or "failed".
// Queue bookkeeping is not a configuration change: it is not audited and
// does not bump the revision hello-sip reloads on.
func (s *Store) MarkEmail(ctx context.Context, id int64, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE voicemail_messages SET email_status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// Ring groups.

// RingGroupMember is one extension in a ring/hunt group. Number and Name
// carry the extension for display; writes use ExtensionID only.
type RingGroupMember struct {
	ExtensionID int64  `json:"extensionId"`
	Number      string `json:"number"`
	Name        string `json:"name"`
	Position    int    `json:"position"`
	Weight      int    `json:"weight"`
	Delay       int    `json:"delay"`
}

// RingGroup is a named ring/hunt group with its members in position order.
type RingGroup struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Strategy      string            `json:"strategy"`
	Hunt          bool              `json:"hunt"`
	RingTimeout   int               `json:"ringTimeout"`
	MemberDelay   int               `json:"memberDelay"`
	IgnoreDND     bool              `json:"ignoreDnd"`
	FailureKind   string            `json:"failureKind"`
	FailureTarget string            `json:"failureTarget"`
	Members       []RingGroupMember `json:"members"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

const groupCols = `g.id, g.name, g.strategy, g.hunt, g.ring_timeout, g.member_delay, g.ignore_dnd,
	g.failure_kind, g.failure_target, g.created_at, g.updated_at`

// listRingGroups reads groups (optionally one) and then their members.
func listRingGroups(ctx context.Context, q querier, where string, args ...any) ([]RingGroup, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+groupCols+` FROM ring_groups g `+where+` ORDER BY g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RingGroup{}
	var ids []int64
	for rows.Next() {
		var g RingGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Strategy, &g.Hunt, &g.RingTimeout, &g.MemberDelay, &g.IgnoreDND,
			&g.FailureKind, &g.FailureTarget, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.Members = []RingGroupMember{}
		out = append(out, g)
		ids = append(ids, g.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	members, err := groupMembers(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Members = members[out[i].ID]
	}
	return out, nil
}

// groupMembers returns each group's members in position order, with the
// extension number and name joined for display.
func groupMembers(ctx context.Context, q querier, ids []int64) (map[int64][]RingGroupMember, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT m.group_id, m.extension_id, e.number, e.name, m.position, m.weight, m.delay
		FROM ring_group_members m JOIN extensions e ON e.id = m.extension_id
		WHERE m.group_id = ANY($1) ORDER BY m.group_id, m.position`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]RingGroupMember{}
	for rows.Next() {
		var groupID int64
		var m RingGroupMember
		if err := rows.Scan(&groupID, &m.ExtensionID, &m.Number, &m.Name, &m.Position, &m.Weight, &m.Delay); err != nil {
			return nil, err
		}
		out[groupID] = append(out[groupID], m)
	}
	return out, rows.Err()
}

// RingGroupInput is the writable shape of a ring group. Members is the full
// member list: an update replaces it.
type RingGroupInput struct {
	Name          string
	Strategy      string
	Hunt          bool
	RingTimeout   int
	MemberDelay   int
	IgnoreDND     bool
	FailureKind   string
	FailureTarget string
	Members       []RingGroupMember
}

// ListRingGroups returns every group with its members.
func (s *Store) ListRingGroups(ctx context.Context) ([]RingGroup, error) {
	return listRingGroups(ctx, s.db, "")
}

// GetRingGroup returns one group with its members.
func (s *Store) GetRingGroup(ctx context.Context, id int64) (RingGroup, error) {
	gs, err := listRingGroups(ctx, s.db, ` WHERE g.id = $1`, id)
	if err != nil {
		return RingGroup{}, err
	}
	if len(gs) == 0 {
		return RingGroup{}, ErrNotFound
	}
	return gs[0], nil
}

// CreateRingGroup inserts a group with its members.
func (s *Store) CreateRingGroup(ctx context.Context, actor string, in RingGroupInput, check Check) (RingGroup, error) {
	var g RingGroup
	err := s.configChange(ctx, actor, "create", "ring_group", check, func(tx *sql.Tx) (int64, error) {
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO ring_groups (name, strategy, hunt, ring_timeout, member_delay, ignore_dnd, failure_kind, failure_target)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			in.Name, in.Strategy, in.Hunt, in.RingTimeout, in.MemberDelay, in.IgnoreDND, in.FailureKind, in.FailureTarget).Scan(&id); err != nil {
			return 0, err
		}
		if err := writeGroupMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		var err error
		g, err = getGroupTx(ctx, tx, id)
		return id, err
	})
	return g, err
}

// UpdateRingGroup replaces a group's fields and member list.
func (s *Store) UpdateRingGroup(ctx context.Context, actor string, id int64, in RingGroupInput, check Check) (RingGroup, error) {
	var g RingGroup
	err := s.configChange(ctx, actor, "update", "ring_group", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `
			UPDATE ring_groups SET name = $2, strategy = $3, hunt = $4, ring_timeout = $5, member_delay = $6,
			       ignore_dnd = $7, failure_kind = $8, failure_target = $9, updated_at = now()
			WHERE id = $1`, id, in.Name, in.Strategy, in.Hunt, in.RingTimeout, in.MemberDelay, in.IgnoreDND,
			in.FailureKind, in.FailureTarget)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if err := writeGroupMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		var err2 error
		g, err2 = getGroupTx(ctx, tx, id)
		return id, err2
	})
	return g, err
}

// DeleteRingGroup removes a group and, by cascade, its members.
func (s *Store) DeleteRingGroup(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "ring_group", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM ring_groups WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

func getGroupTx(ctx context.Context, tx *sql.Tx, id int64) (RingGroup, error) {
	gs, err := listRingGroups(ctx, tx, ` WHERE g.id = $1`, id)
	if err != nil {
		return RingGroup{}, err
	}
	if len(gs) == 0 {
		return RingGroup{}, ErrNotFound
	}
	return gs[0], nil
}

// writeGroupMembers replaces a group's member rows. Positions are unique per
// group with a deferred constraint, so any order of inserts works.
func writeGroupMembers(ctx context.Context, tx *sql.Tx, id int64, members []RingGroupMember) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM ring_group_members WHERE group_id = $1`, id); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ring_group_members (group_id, extension_id, position, weight, delay)
			VALUES ($1, $2, $3, $4, $5)`, id, m.ExtensionID, m.Position, m.Weight, m.Delay); err != nil {
			return err
		}
	}
	return nil
}

// Feature codes.

// FeatureCode maps a DTMF code to an action; Argument carries what the
// action needs (for example "set" or "clear" for a forwarding action).
type FeatureCode struct {
	Code     string `json:"code"`
	Action   string `json:"action"`
	Argument string `json:"argument"`
}

// ListFeatureCodes returns every code ordered by code.
func (s *Store) ListFeatureCodes(ctx context.Context) ([]FeatureCode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code, action, argument FROM feature_codes ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FeatureCode{}
	for rows.Next() {
		var c FeatureCode
		if err := rows.Scan(&c.Code, &c.Action, &c.Argument); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutFeatureCodes syncs the code list: every given code is upserted and
// every code not in the list is removed, so the caller always sends the
// complete configuration.
func (s *Store) PutFeatureCodes(ctx context.Context, actor string, codes []FeatureCode, check Check) error {
	return s.configChangeID(ctx, actor, "update", "feature_code", check, func(tx *sql.Tx) (string, error) {
		for _, c := range codes {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO feature_codes (code, action, argument) VALUES ($1, $2, $3)
				ON CONFLICT (code) DO UPDATE SET action = EXCLUDED.action, argument = EXCLUDED.argument`,
				c.Code, c.Action, c.Argument); err != nil {
				return "", err
			}
		}
		kept := make([]string, len(codes))
		for i, c := range codes {
			kept[i] = c.Code
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM feature_codes WHERE code <> ALL($1)`, kept); err != nil {
			return "", err
		}
		return "list", nil
	})
}

// DefaultFeatureCodes is the Phase 4 default set (spec S-11). The forwarded
// actions carry "set" or "clear" in the argument; `*2` (blind transfer) is
// dispatched by hello-sip and is not stored.
func DefaultFeatureCodes() []FeatureCode {
	return []FeatureCode{
		{Code: "*72", Action: "forward_always", Argument: "set"},
		{Code: "*73", Action: "forward_always", Argument: "clear"},
		{Code: "*78", Action: "dnd_on"},
		{Code: "*79", Action: "dnd_off"},
		{Code: "*90", Action: "forward_busy", Argument: "set"},
		{Code: "*91", Action: "forward_busy", Argument: "clear"},
		{Code: "*92", Action: "forward_no_answer", Argument: "set"},
		{Code: "*93", Action: "forward_no_answer", Argument: "clear"},
		{Code: "*97", Action: "voicemail"},
		{Code: "##", Action: "attended_transfer"},
	}
}

// EnsureFeatureCodes inserts the defaults that are missing, without touching
// codes an administrator changed or removed. It runs at hello-control start.
func (s *Store) EnsureFeatureCodes(ctx context.Context, defaults []FeatureCode) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM feature_codes`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	existing, err := s.ListFeatureCodes(ctx)
	if err != nil {
		return err
	}
	return s.PutFeatureCodes(ctx, "system", append(existing, defaults...), nil)
}

// FeatureCodeArg is the argument the forwarding actions carry.
const (
	ArgSet   = "set"
	ArgClear = "clear"
)
