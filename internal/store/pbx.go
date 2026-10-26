package store

// Phase 4 PBX features: voicemail boxes and messages, ring/hunt groups,
// feature codes and the voicemail email queue. Every configuration mutation
// runs through configChange, so it is audited, bumps the configuration
// revision and notifies hello-sip like every other change.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// VoicemailBox is an extension's voicemail box. The password hash is never
// read into this type; HasPassword reports whether one is set.
type VoicemailBox struct {
	ID                int64  `json:"id"`
	ExtensionID       int64  `json:"extensionId"`
	HasPassword       bool   `json:"hasPassword"`
	Email             string `json:"email"`
	GreetingObject    string `json:"greetingObject"`
	UnreachableObject string `json:"unreachableObject"`
	// BusyObject is the custom greeting for a busy extension and NameObject
	// the owner's recorded name (migration 00036); "" means the default
	// spoken prompt is used.
	BusyObject string `json:"busyObject"`
	NameObject string `json:"nameObject"`
	// GreetingTTS, UnreachableTTS and BusyTTS describe how a greeting was
	// generated from text; nil for uploaded, recorded or picked audio.
	GreetingTTS    *TTSMeta `json:"greetingTts"`
	UnreachableTTS *TTSMeta `json:"unreachableTts"`
	BusyTTS        *TTSMeta `json:"busyTts"`
	// PromptLanguage is the language the box speaks (migration 00045): "en",
	// "nl", or "" to inherit the deployment language.
	PromptLanguage string    `json:"promptLanguage"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

const boxCols = `id, extension_id, password_hash <> '', email, greeting_object, unreachable_object, busy_object, name_object, greeting_tts, unreachable_tts, busy_tts, COALESCE(prompt_language, ''), created_at, updated_at`

// GetVoicemailBox returns the box of extensionID.
func (s *Store) GetVoicemailBox(ctx context.Context, extensionID int64) (VoicemailBox, error) {
	b, err := scanBox(s.db.QueryRowContext(ctx,
		`SELECT `+boxCols+` FROM voicemail_boxes WHERE extension_id = $1`, extensionID))
	return b, mapErr(err)
}

// GetVoicemailBoxByID returns the box with id boxID.
func (s *Store) GetVoicemailBoxByID(ctx context.Context, boxID int64) (VoicemailBox, error) {
	b, err := scanBox(s.db.QueryRowContext(ctx,
		`SELECT `+boxCols+` FROM voicemail_boxes WHERE id = $1`, boxID))
	return b, mapErr(err)
}

// VoicemailPromptLanguages lists the languages boxes have set for
// themselves, sorted, with no duplicates and no empty value. The static
// prompts of each are pre-generated.
func (s *Store) VoicemailPromptLanguages(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT prompt_language FROM voicemail_boxes WHERE prompt_language IS NOT NULL AND prompt_language <> '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func scanBox(r interface{ Scan(...any) error }) (VoicemailBox, error) {
	var b VoicemailBox
	var gt, ut, bt []byte
	err := r.Scan(&b.ID, &b.ExtensionID, &b.HasPassword, &b.Email, &b.GreetingObject, &b.UnreachableObject,
		&b.BusyObject, &b.NameObject, &gt, &ut, &bt, &b.PromptLanguage, &b.CreatedAt, &b.UpdatedAt)
	b.GreetingTTS, b.UnreachableTTS, b.BusyTTS = parseTTSMeta(gt), parseTTSMeta(ut), parseTTSMeta(bt)
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
	// Busy and Name are the busy greeting and the recorded name objects.
	Busy *string
	Name *string
	// GreetingTTS, UnreachableTTS and BusyTTS accompany a new Greeting,
	// Unreachable or Busy object: the prompt it was generated from, or nil
	// for uploaded audio (which clears an earlier record).
	GreetingTTS    *TTSMeta
	UnreachableTTS *TTSMeta
	BusyTTS        *TTSMeta
	// PromptLanguage sets the box language; "" clears it to inherit.
	PromptLanguage *string
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
				greeting_tts = CASE WHEN $4::text IS NOT NULL THEN $6::jsonb ELSE greeting_tts END,
				unreachable_tts = CASE WHEN $5::text IS NOT NULL THEN $7::jsonb ELSE unreachable_tts END,
				busy_object = COALESCE($8, busy_object),
				name_object = COALESCE($9, name_object),
				busy_tts = CASE WHEN $8::text IS NOT NULL THEN $10::jsonb ELSE busy_tts END,
				prompt_language = CASE WHEN $11::boolean THEN NULLIF($12::text, '') ELSE prompt_language END,
				updated_at = now()
			WHERE extension_id = $1`,
			extensionID, c.Email, hash, c.Greeting, c.Unreachable,
			metaJSON(c.GreetingTTS), metaJSON(c.UnreachableTTS), c.Busy, c.Name, metaJSON(c.BusyTTS),
			c.PromptLanguage != nil, strOrEmpty(c.PromptLanguage)); err != nil {
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
	ID     int64  `json:"id"`
	BoxID  int64  `json:"boxId"`
	Caller string `json:"caller"`
	// CallerName is the caller-ID display name the call carried ("" when it
	// carried none).
	CallerName  string    `json:"callerName"`
	DurationMs  int64     `json:"durationMs"`
	Heard       bool      `json:"heard"`
	EmailStatus string    `json:"emailStatus"`
	CreatedAt   time.Time `json:"createdAt"`
	// Object is the MinIO key; it stays internal (clients play audio through
	// the audio route).
	Object string `json:"-"`
}

const messageCols = `id, box_id, caller, caller_name, duration_ms, heard, email_status, created_at, minio_object`

func scanMessage(r interface{ Scan(...any) error }) (VoicemailMessage, error) {
	var m VoicemailMessage
	err := r.Scan(&m.ID, &m.BoxID, &m.Caller, &m.CallerName, &m.DurationMs, &m.Heard, &m.EmailStatus, &m.CreatedAt, &m.Object)
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
		var box int64
		err := tx.QueryRowContext(ctx,
			`UPDATE voicemail_messages SET heard = $2 WHERE id = $1 RETURNING box_id`, id, heard).Scan(&box)
		if err != nil {
			return id, err
		}
		return id, notifyMailbox(ctx, tx, box)
	})
}

// DeleteVoicemailMessage removes a message row and returns its MinIO object
// key; the caller removes the object, so this package never depends on the
// object store.
func (s *Store) DeleteVoicemailMessage(ctx context.Context, actor string, id int64) (string, error) {
	var object string
	err := s.configChange(ctx, actor, "delete", "voicemail_message", nil, func(tx *sql.Tx) (int64, error) {
		var box int64
		if err := tx.QueryRowContext(ctx,
			`SELECT minio_object, box_id FROM voicemail_messages WHERE id = $1`, id).Scan(&object, &box); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM voicemail_messages WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		return id, notifyMailbox(ctx, tx, box)
	})
	return object, err
}

// notifyMailbox queues the mailbox-changed NOTIFY in the changing
// transaction, so it reaches the SIP nodes only if the change commits.
func notifyMailbox(ctx context.Context, tx *sql.Tx, box int64) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, MailboxChannel, strconv.FormatInt(box, 10))
	return err
}

// VoicemailCounts is a box's MWI summary: unheard and total messages.
func (s *Store) VoicemailCounts(ctx context.Context, boxID int64) (unheard, total int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE NOT heard), count(*) FROM voicemail_messages WHERE box_id = $1`, boxID).
		Scan(&unheard, &total)
	return unheard, total, err
}

// VoicemailBoxSummary is one box in the box list: its extension and its
// message counts.
type VoicemailBoxSummary struct {
	ID          int64  `json:"id"`
	ExtensionID int64  `json:"extensionId"`
	Number      string `json:"number"`
	Name        string `json:"name"`
	Unheard     int64  `json:"unheard"`
	Total       int64  `json:"total"`
	// PromptLanguage is the box's own language; "" inherits.
	PromptLanguage string `json:"promptLanguage"`
}

// ListVoicemailBoxes returns every box with its extension and message
// counts, ordered by extension number.
func (s *Store) ListVoicemailBoxes(ctx context.Context) ([]VoicemailBoxSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.extension_id, e.number, e.name,
			count(m.id) FILTER (WHERE NOT m.heard), count(m.id), COALESCE(b.prompt_language, '')
		FROM voicemail_boxes b
		JOIN extensions e ON e.id = b.extension_id
		LEFT JOIN voicemail_messages m ON m.box_id = b.id
		GROUP BY b.id, e.number, e.name
		ORDER BY e.number`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []VoicemailBoxSummary{}
	for rows.Next() {
		var b VoicemailBoxSummary
		if err := rows.Scan(&b.ID, &b.ExtensionID, &b.Number, &b.Name, &b.Unheard, &b.Total, &b.PromptLanguage); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// The voicemail paths hello-sip uses (the *97 application, MWI). These are
// call-plane bookkeeping, not configuration: like MarkEmail they are not
// audited and do not bump the revision hello-sip reloads on — the snapshot
// carries no message rows, so a bump would only churn every node.

// VoicemailBoxByNumber returns the box of the extension with number
// (ok=false when the extension has no box or does not exist). This is the
// box lookup the *97 application and MWI make by dialled number.
func (s *Store) VoicemailBoxByNumber(ctx context.Context, number string) (VoicemailBox, bool, error) {
	var b VoicemailBox
	err := s.db.QueryRowContext(ctx, `
		SELECT b.id, b.email, b.greeting_object, b.unreachable_object, b.busy_object, b.name_object, COALESCE(b.prompt_language, '')
		FROM voicemail_boxes b JOIN extensions e ON e.id = b.extension_id
		WHERE e.number = $1`, number).
		Scan(&b.ID, &b.Email, &b.GreetingObject, &b.UnreachableObject, &b.BusyObject, &b.NameObject, &b.PromptLanguage)
	if errors.Is(err, sql.ErrNoRows) {
		return VoicemailBox{}, false, nil
	}
	if err != nil {
		return VoicemailBox{}, false, err
	}
	return b, true, nil
}

// VerifyVoicemailPassword reports whether password opens boxID's retrieval
// prompt. A box without a password accepts none. The comparison is bcrypt's,
// which is constant-time in the password.
func (s *Store) VerifyVoicemailPassword(ctx context.Context, boxID int64, password string) (bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT password_hash FROM voicemail_boxes WHERE id = $1`, boxID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash == "" {
		return false, nil
	}
	return auth.CheckPassword(hash, password), nil
}

// InsertVoicemailMessage stores one recorded message and returns its id.
// callerName is the caller-ID display name, "" when the call carried none.
func (s *Store) InsertVoicemailMessage(ctx context.Context, boxID int64, object, caller, callerName string, durationMs int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO voicemail_messages (box_id, minio_object, caller, caller_name, duration_ms)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		boxID, object, caller, callerName, durationMs).Scan(&id)
	return id, err
}

// greetingColumns maps a greeting slot of the guided session to its columns
// (object, and the generated-text record that goes with it, or "").
var greetingColumns = map[string][2]string{
	"greeting":    {"greeting_object", "greeting_tts"},
	"busy":        {"busy_object", "busy_tts"},
	"unreachable": {"unreachable_object", "unreachable_tts"},
	"name":        {"name_object", ""},
}

// SetVoicemailGreeting is the call plane's way to record a greeting from the
// phone (the 0 menu of *97): it points slot (greeting, busy, unreachable or
// name) at object and returns the object it replaced, which the caller
// removes. An empty object reverts the slot to the default spoken prompt.
// Like the other call-plane paths it is not audited and does not bump the
// revision hello-sip reloads on (the snapshot carries no greeting).
func (s *Store) SetVoicemailGreeting(ctx context.Context, boxID int64, slot, object string) (string, error) {
	cols, ok := greetingColumns[slot]
	if !ok {
		return "", errors.New("store: unknown greeting slot " + slot)
	}
	var prev string
	if err := s.db.QueryRowContext(ctx,
		`SELECT `+cols[0]+` FROM voicemail_boxes WHERE id = $1`, boxID).Scan(&prev); err != nil {
		return "", mapErr(err)
	}
	set := cols[0] + ` = $2`
	if cols[1] != "" {
		set += `, ` + cols[1] + ` = NULL`
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE voicemail_boxes SET `+set+`, updated_at = now() WHERE id = $1`, boxID, object); err != nil {
		return "", err
	}
	return prev, nil
}

// ResetVoicemailGreetings reverts the greeting, busy and unreachable slots of
// boxID to the default spoken prompts (the recorded name stays) and returns
// the objects it dropped.
func (s *Store) ResetVoicemailGreetings(ctx context.Context, boxID int64) ([]string, error) {
	var dropped []string
	for _, slot := range []string{"greeting", "busy", "unreachable"} {
		prev, err := s.SetVoicemailGreeting(ctx, boxID, slot, "")
		if err != nil {
			return dropped, err
		}
		if prev != "" {
			dropped = append(dropped, prev)
		}
	}
	return dropped, nil
}

// SetVoicemailPIN changes boxID's retrieval password (bcrypt-hashed).
func (s *Store) SetVoicemailPIN(ctx context.Context, boxID int64, pin string) error {
	h, err := auth.HashPassword(pin)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE voicemail_boxes SET password_hash = $2, updated_at = now() WHERE id = $1`, boxID, h)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// RemoveVoicemailMessage deletes message id of boxID from the phone and
// returns its object key for the caller to remove. A message of another box
// is not found. Not audited, like the other call-plane paths.
func (s *Store) RemoveVoicemailMessage(ctx context.Context, boxID, id int64) (string, error) {
	var object string
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM voicemail_messages WHERE id = $1 AND box_id = $2 RETURNING minio_object`, id, boxID).Scan(&object)
	return object, mapErr(err)
}

// SetVoicemailMessageHeard sets a message's heard flag after the phone
// played it (*97). The UI path is UpdateVoicemailMessageHeard, which audits.
func (s *Store) SetVoicemailMessageHeard(ctx context.Context, id int64, heard bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE voicemail_messages SET heard = $2 WHERE id = $1`, id, heard)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// Feature groups.

// FeatureUpdate is one hello-sip feature-code change to an extension: DND
// and the forwarding targets. Pointer fields are optional; nil leaves the
// column alone, and the empty string clears a forward.
type FeatureUpdate struct {
	Number          string
	DND             *bool
	ForwardAlways   *string
	ForwardBusy     *string
	ForwardNoAnswer *string
	CallWaiting     *bool
	// NoAnswerRings is the no-answer ring count (0 = system default).
	NoAnswerRings *int
}

// ApplyFeatureUpdate applies one feature-code change the same way every
// configuration change is applied: the extension row, the audit event, the
// revision bump and the NOTIFY, in one transaction, so hello-sip reloads its
// snapshot. Unlike the API paths it runs no routing check: a feature code
// touches only the extension's own feature columns.
func (s *Store) ApplyFeatureUpdate(ctx context.Context, actor string, u FeatureUpdate) error {
	return s.configChangeID(ctx, actor, "update", "extension", nil, func(tx *sql.Tx) (string, error) {
		res, err := tx.ExecContext(ctx, `
			UPDATE extensions SET
				dnd = COALESCE($2, dnd),
				forward_always = COALESCE($3, forward_always),
				forward_busy = COALESCE($4, forward_busy),
				forward_no_answer = COALESCE($5, forward_no_answer),
				call_waiting = COALESCE($6, call_waiting),
				no_answer_rings = COALESCE($7, no_answer_rings),
				updated_at = now()
			WHERE number = $1`,
			u.Number, u.DND, u.ForwardAlways, u.ForwardBusy, u.ForwardNoAnswer, u.CallWaiting, u.NoAnswerRings)
		if err != nil {
			return "", err
		}
		if err := requireRow(res); err != nil {
			return "", err
		}
		var id int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM extensions WHERE number = $1`, u.Number).Scan(&id); err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	})
}

// Ring groups.

// RingGroupMember is one extension in a ring/hunt group. Number and Name
// carry the member's number and name for display.
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
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Strategy      string `json:"strategy"`
	Hunt          bool   `json:"hunt"`
	RingTimeout   int    `json:"ringTimeout"`
	MemberDelay   int    `json:"memberDelay"`
	IgnoreDND     bool   `json:"ignoreDnd"`
	FailureKind   string `json:"failureKind"`
	FailureTarget string `json:"failureTarget"`
	// MOHClassID is the music on hold class of calls to this group (nil
	// uses the global default); MOHRingback plays it to the caller instead
	// of ringback while the group rings (migration 00021).
	MOHClassID  *int64            `json:"mohClassId"`
	MOHRingback bool              `json:"mohRingback"`
	Members     []RingGroupMember `json:"members"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

const groupCols = `g.id, g.name, g.strategy, g.hunt, g.ring_timeout, g.member_delay, g.ignore_dnd,
	g.failure_kind, g.failure_target, g.moh_class_id, g.moh_ringback, g.created_at, g.updated_at`

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
		var moh sql.NullInt64
		if err := rows.Scan(&g.ID, &g.Name, &g.Strategy, &g.Hunt, &g.RingTimeout, &g.MemberDelay, &g.IgnoreDND,
			&g.FailureKind, &g.FailureTarget, &moh, &g.MOHRingback, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		if moh.Valid {
			g.MOHClassID = &moh.Int64
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
		SELECT m.group_id, m.extension_id,
		       COALESCE(e.number, ''), COALESCE(e.name, ''),
		       m.position, m.weight, m.delay
		FROM ring_group_members m
		LEFT JOIN extensions e ON e.id = m.extension_id
		WHERE m.group_id = ANY($1) ORDER BY m.group_id, m.position`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]RingGroupMember{}
	for rows.Next() {
		var groupID int64
		var m RingGroupMember
		var extID sql.NullInt64
		if err := rows.Scan(&groupID, &extID, &m.Number, &m.Name, &m.Position, &m.Weight, &m.Delay); err != nil {
			return nil, err
		}
		m.ExtensionID = extID.Int64
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
	MOHClassID    *int64
	MOHRingback   bool
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
		if err := checkRingGroupIVR(ctx, tx, "", in); err != nil {
			return 0, err
		}
		var id int64
		if err := checkMOHClass(ctx, tx, "mohClassId", in.MOHClassID); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO ring_groups (name, strategy, hunt, ring_timeout, member_delay, ignore_dnd, failure_kind, failure_target,
			                         moh_class_id, moh_ringback)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			in.Name, in.Strategy, in.Hunt, in.RingTimeout, in.MemberDelay, in.IgnoreDND, in.FailureKind, in.FailureTarget,
			in.MOHClassID, in.MOHRingback).Scan(&id); err != nil {
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
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM ring_groups WHERE id = $1`, id).Scan(&cur); err != nil {
			return id, err
		}
		if cur != in.Name {
			if err := refuseIfIVRPoints(ctx, tx, "ring group "+cur+", which cannot be renamed while it is named by", cur, IVRRingGroup); err != nil {
				return id, err
			}
			if err := refuseIfGroupFailsTo(ctx, tx, "ring group "+cur+", which cannot be renamed while it is the fallback of", cur, "ring_group"); err != nil {
				return id, err
			}
		}
		if err := checkRingGroupIVR(ctx, tx, cur, in); err != nil {
			return id, err
		}
		if err := checkMOHClass(ctx, tx, "mohClassId", in.MOHClassID); err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE ring_groups SET name = $2, strategy = $3, hunt = $4, ring_timeout = $5, member_delay = $6,
			       ignore_dnd = $7, failure_kind = $8, failure_target = $9, moh_class_id = $10, moh_ringback = $11,
			       updated_at = now()
			WHERE id = $1`, id, in.Name, in.Strategy, in.Hunt, in.RingTimeout, in.MemberDelay, in.IgnoreDND,
			in.FailureKind, in.FailureTarget, in.MOHClassID, in.MOHRingback)
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

// checkRingGroupIVR checks a ring group's IVR interplay inside the change's
// transaction: a new name must not be an IVR menu's or a conference room's
// number, and an IVR or time condition failure destination must name an
// existing one. cur is the stored name ("" on create).
func checkRingGroupIVR(ctx context.Context, tx *sql.Tx, cur string, in RingGroupInput) error {
	if in.Name != cur {
		if err := checkDialNumber(ctx, tx, "name", in.Name, numRingGroup); err != nil {
			return err
		}
	}
	switch in.FailureKind {
	case "extension":
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM extensions WHERE number = $1`, in.FailureTarget).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError("failureTarget", fmt.Sprintf("extension %q does not exist", in.FailureTarget))
		}
	case "ring_group":
		if in.FailureTarget == in.Name {
			return fieldError("failureTarget", "a ring group cannot fail over to itself")
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ring_groups WHERE name = $1`, in.FailureTarget).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError("failureTarget", fmt.Sprintf("ring group %q does not exist", in.FailureTarget))
		}
	case "ivr":
		return requireIVRMenuTx(ctx, tx, "failureTarget", in.FailureTarget)
	case "time_condition":
		return requireTimeConditionTx(ctx, tx, "failureTarget", in.FailureTarget)
	}
	return nil
}

// refuseIfGroupFailsTo returns an *InUseError naming the ring groups whose
// failure destination is target of kind ("extension" or "ring_group"):
// a fallback names its target by number or name, so neither can change
// or go while a group falls back to it.
func refuseIfGroupFailsTo(ctx context.Context, tx *sql.Tx, what, target, kind string) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM ring_groups WHERE failure_kind = $1 AND failure_target = $2 ORDER BY name`,
		kind, target)
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
		refs = append(refs, "ring group "+n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) > 0 {
		return inUseBy(what, refs)
	}
	return nil
}

// DeleteRingGroup removes a group and, by cascade, its members.
func (s *Store) DeleteRingGroup(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "ring_group", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM ring_groups WHERE id = $1`, id).Scan(&name); err != nil {
			return id, err
		}
		if err := refuseIfIVRPoints(ctx, tx, "ring group "+name, name, IVRRingGroup); err != nil {
			return id, err
		}
		if err := refuseIfGroupFailsTo(ctx, tx, "ring group "+name, name, "ring_group"); err != nil {
			return id, err
		}
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
			VALUES ($1, NULLIF($2, 0), $3, $4, $5)`, id, m.ExtensionID, m.Position, m.Weight, m.Delay); err != nil {
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
		if err := checkFeatureCodesAgainstPaging(ctx, tx, codes); err != nil {
			return "", err
		}
		for i, c := range codes {
			var err error
			switch c.Action {
			case "ivr":
				err = requireIVRMenuTx(ctx, tx, fmt.Sprintf("items[%d].argument", i), c.Argument)
			case "time_condition":
				err = requireTimeConditionTx(ctx, tx, fmt.Sprintf("items[%d].argument", i), c.Argument)
			}
			if err != nil {
				return "", err
			}
		}
		err := dialGuard(ctx, tx, "items", func() error {
			for _, c := range codes {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO feature_codes (code, action, argument) VALUES ($1, $2, $3)
					ON CONFLICT (code) DO UPDATE SET action = EXCLUDED.action, argument = EXCLUDED.argument`,
					c.Code, c.Action, c.Argument); err != nil {
					return err
				}
			}
			kept := make([]string, len(codes))
			for i, c := range codes {
				kept[i] = c.Code
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM feature_codes WHERE code <> ALL($1)`, kept)
			return err
		})
		return "list", err
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
		{Code: "*80", Action: "intercom"},
		{Code: "*28", Action: "tc_override", Argument: "toggle"},
		{Code: "*70", Action: "call_waiting_off"},
		{Code: "*71", Action: "call_waiting_on"},
		{Code: "*74", Action: "park"},
		{Code: "*75", Action: "park_retrieve"},
		{Code: "*40", Action: "pickup_group"},
		{Code: "*41", Action: "pickup_directed"},
		{Code: "##", Action: "attended_transfer"},
		{Code: "*45", Action: ActionQueueLogin},
		{Code: "*46", Action: ActionQueueLogout},
		{Code: "*47", Action: ActionQueuePause, Argument: "break"},
		{Code: "*48", Action: ActionQueueUnpause},
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

// strOrEmpty reads an optional string, "" for nil.
func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
