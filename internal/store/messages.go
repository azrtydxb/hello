package store

// SIP MESSAGE text messaging (RFC 3428): the durable queue and history
// between hello-control (which accepts sends from the API) and hello-sip
// (which accepts MESSAGEs from phones and carriers and delivers them).
//
// A send is one row per recipient. A hello-sip node claims pending rows with
// FOR UPDATE SKIP LOCKED, so exactly one node delivers a row, whatever the
// number of nodes. Message bodies are never written to a log or an audit row.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// MessageChannel is the PostgreSQL channel a queued message notifies; SIP
// nodes LISTEN on it so they deliver at once instead of waiting for a poll.
const MessageChannel = "hello_messages"

// Message limits.
const (
	// MaxMessageBytes is the largest body accepted. A MESSAGE travels as one
	// UDP datagram (RFC 3428 advises under 1300 bytes in all), headers
	// included, so the text has to be short.
	MaxMessageBytes = 700
	// MaxMessageRecipients bounds one send's fan-out.
	MaxMessageRecipients = 5000
	// MaxMessageAttempts is how many failed deliveries a message gets.
	MaxMessageAttempts = 6
	// messageStaleAfter is how long a claimed message may stay "sending"
	// before it is returned to the queue (its node died).
	messageStaleAfter = 2 * time.Minute
)

// Message scopes, recipient kinds, sender kinds and statuses.
const (
	MsgScopeDirect       = "direct"
	MsgScopeRingGroup    = "ring_group"
	MsgScopePagingGroup  = "paging_group"
	MsgScopeBroadcast    = "broadcast"
	MsgKindExtension     = "extension"
	MsgKindExternal      = "external"
	MsgSenderSystem      = "system"
	MsgSenderSystemName  = "system"
	MsgPending           = "pending"
	MsgSending           = "sending"
	MsgDelivered         = "delivered"
	MsgFailed            = "failed"
	messageDefaultCType  = "text/plain"
	messageMaxCTypeBytes = 128
)

// Errors a send can fail with; the API and the SIP node map them to a status.
var (
	// ErrMessageTarget: the extension or group does not exist, or has no
	// one to send to.
	ErrMessageTarget = errors.New("store: message target not found")
	// ErrMessageForbidden: the sender may not message this group.
	ErrMessageForbidden = errors.New("store: sender may not message this target")
	// ErrMessageRate: the sender exceeded the per-minute limit.
	ErrMessageRate = errors.New("store: message rate limit exceeded")
	// ErrMessagesDisabled: an administrator turned messaging off.
	ErrMessagesDisabled = errors.New("store: messaging is disabled")
	// ErrMessageTooLarge: the body or content type is over the limit.
	ErrMessageTooLarge = errors.New("store: message too large")
	// ErrNoExternalTrunk: no enabled trunk has messaging turned on.
	ErrNoExternalTrunk = errors.New("store: no trunk has messaging enabled")
)

// Message is one delivery of a message to one recipient, with the counts of
// its batch (the other recipients of the same send).
type Message struct {
	ID            int64      `json:"id"`
	Batch         string     `json:"batch"`
	Sender        string     `json:"sender"`
	SenderKind    string     `json:"senderKind"`
	SenderName    string     `json:"senderName"`
	Actor         string     `json:"actor"`
	Recipient     string     `json:"recipient"`
	RecipientKind string     `json:"recipientKind"`
	Scope         string     `json:"scope"`
	ScopeRef      string     `json:"scopeRef"`
	Trunk         string     `json:"trunk"`
	ContentType   string     `json:"contentType"`
	Body          string     `json:"body"`
	BodyBytes     int        `json:"bodyBytes"`
	Status        string     `json:"status"`
	Detail        string     `json:"detail"`
	Attempts      int        `json:"attempts"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	DeliveredAt   *time.Time `json:"deliveredAt"`
	// Recipients, Delivered, Pending and Failed count the batch's rows that
	// match the listing's filter; Status is the batch's overall status.
	Recipients int `json:"recipients"`
	Delivered  int `json:"delivered"`
	Pending    int `json:"pending"`
	FailedN    int `json:"failed"`
}

// MessageConversation is a thread: the messages between two parties, or to
// one group.
type MessageConversation struct {
	Key         string    `json:"key"`
	Scope       string    `json:"scope"`
	ScopeRef    string    `json:"scopeRef"`
	Messages    int       `json:"messages"`
	LastID      int64     `json:"lastId"`
	LastAt      time.Time `json:"lastAt"`
	LastSender  string    `json:"lastSender"`
	LastRecip   string    `json:"lastRecipient"`
	LastSnippet string    `json:"lastSnippet"`
	LastStatus  string    `json:"lastStatus"`
	// Parties are the two sides of a direct thread; empty otherwise.
	Parties []string `json:"parties"`
}

// MessageSettings are the administrator-set messaging limits.
type MessageSettings struct {
	Enabled               bool `json:"enabled"`
	TTLHours              int  `json:"ttlHours"`
	RetentionDays         int  `json:"retentionDays"`
	RatePerMinute         int  `json:"ratePerMinute"`
	MaxQueuedPerExtension int  `json:"maxQueuedPerExtension"`
}

// MessageInput is one send.
type MessageInput struct {
	// Sender is an extension number (SenderKind extension), an external
	// number (external) or empty for the system.
	Sender     string
	SenderKind string
	SenderName string
	// Actor is the user or credential that asked, for the API's sends.
	Actor string
	// TargetKind is extension, ring_group, paging_group, broadcast or
	// external; Target names it (an extension number, a ring group name, a
	// paging group code or name, a number; empty for broadcast).
	TargetKind  string
	Target      string
	ContentType string
	Body        []byte
	// TrunkID is the carrier an inbound external message came in on.
	TrunkID int64
	// ClaimBy, for a single-recipient send, stores the row already claimed
	// by that node so it can deliver at once; no other node will.
	ClaimBy string
	// Audit writes an audit row (API sends).
	Audit bool
}

// MessageJob is a claimed message with what the SIP node needs to send it.
type MessageJob struct {
	ID            int64
	Batch         string
	Sender        string
	SenderKind    string
	SenderName    string
	Recipient     string
	RecipientKind string
	Scope         string
	ScopeRef      string
	ContentType   string
	Body          []byte
	TrunkID       int64
	Attempts      int
	CreatedAt     time.Time
}

// MessageSent is what a send produced.
type MessageSent struct {
	Batch      string
	Scope      string
	Recipients int
	// Jobs holds the rows stored already claimed (MessageInput.ClaimBy).
	Jobs []MessageJob
}

// MessageFilter selects messages for a listing.
type MessageFilter struct {
	// Extension keeps messages the extension sent or received.
	Extension    string
	Conversation string
	Status       string
	Query        string
}

var (
	externalNumberRe = regexp.MustCompile(`^\+?[0-9*#]{2,32}$`)
	contentTypeRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*(\s*;.*)?$`)
)

// ValidExternalNumber reports whether n can be dialled as an external number.
func ValidExternalNumber(n string) bool { return externalNumberRe.MatchString(n) }

// ValidMessageContentType reports whether ct is a syntactically valid media
// type (parameters allowed).
func ValidMessageContentType(ct string) bool {
	return len(ct) <= messageMaxCTypeBytes && contentTypeRe.MatchString(ct)
}

// IsTextContentType reports whether ct is a type whose body can be shown.
func IsTextContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return strings.HasPrefix(ct, "text/") || strings.HasPrefix(ct, "message/cpim")
}

// splitBody returns the text or raw column a body belongs in.
func splitBody(ct string, body []byte) (text *string, raw []byte) {
	if IsTextContentType(ct) && utf8.Valid(body) && !strings.ContainsRune(string(body), 0) {
		s := string(body)
		return &s, nil
	}
	if body == nil {
		body = []byte{}
	}
	return nil, body
}

func newBatch() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GetMessageSettings returns the messaging settings.
func (s *Store) GetMessageSettings(ctx context.Context) (MessageSettings, error) {
	return getMessageSettings(ctx, s.db)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getMessageSettings(ctx context.Context, q rowQuerier) (MessageSettings, error) {
	var m MessageSettings
	err := q.QueryRowContext(ctx, `SELECT enabled, ttl_hours, retention_days, rate_per_minute, max_queued_per_extension
		FROM message_settings`).Scan(&m.Enabled, &m.TTLHours, &m.RetentionDays, &m.RatePerMinute, &m.MaxQueuedPerExtension)
	return m, err
}

// PutMessageSettings replaces the settings and audits it.
func (s *Store) PutMessageSettings(ctx context.Context, actor string, m MessageSettings) (MessageSettings, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE message_settings SET enabled = $1, ttl_hours = $2, retention_days = $3,
			rate_per_minute = $4, max_queued_per_extension = $5`,
			m.Enabled, m.TTLHours, m.RetentionDays, m.RatePerMinute, m.MaxQueuedPerExtension); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "message_settings", "messages")
	})
	if err != nil {
		return MessageSettings{}, err
	}
	return m, nil
}

// resolveMessageTargets returns the recipient extension numbers of a send
// and its scope and scope reference.
func resolveMessageTargets(ctx context.Context, tx *sql.Tx, in MessageInput) (recipients []string, scope, ref string, err error) {
	switch in.TargetKind {
	case MsgKindExtension:
		var n string
		if err = tx.QueryRowContext(ctx, `SELECT number FROM extensions WHERE number = $1`, in.Target).Scan(&n); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", "", ErrMessageTarget
			}
			return nil, "", "", err
		}
		return []string{n}, MsgScopeDirect, "", nil
	case MsgKindExternal:
		if !ValidExternalNumber(in.Target) {
			return nil, "", "", ErrMessageTarget
		}
		return []string{in.Target}, MsgScopeDirect, "", nil
	case MsgScopeRingGroup:
		var id int64
		var name string
		if err = tx.QueryRowContext(ctx, `SELECT id, name FROM ring_groups WHERE name = $1`, in.Target).Scan(&id, &name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", "", ErrMessageTarget
			}
			return nil, "", "", err
		}
		recipients, err = queryStrings(ctx, tx, `SELECT e.number FROM ring_group_members m
			JOIN extensions e ON e.id = m.extension_id WHERE m.group_id = $1 ORDER BY m.position`, id)
		return recipients, MsgScopeRingGroup, name, err
	case MsgScopePagingGroup:
		var id int64
		var name string
		if err = tx.QueryRowContext(ctx, `SELECT id, name FROM paging_groups WHERE enabled AND (code = $1 OR name = $1)
			ORDER BY (code = $1) DESC LIMIT 1`, in.Target).Scan(&id, &name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", "", ErrMessageTarget
			}
			return nil, "", "", err
		}
		if in.SenderKind == MsgKindExtension {
			// The page's caller list applies: no list means any internal
			// extension, a list means those extensions only.
			var restricted, allowed bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM paging_group_callers WHERE group_id = $1),
				EXISTS (SELECT 1 FROM paging_group_callers c JOIN extensions e ON e.id = c.extension_id
				        WHERE c.group_id = $1 AND e.number = $2)`, id, in.Sender).Scan(&restricted, &allowed); err != nil {
				return nil, "", "", err
			}
			if restricted && !allowed {
				return nil, "", "", ErrMessageForbidden
			}
		}
		recipients, err = queryStrings(ctx, tx, `SELECT e.number FROM paging_group_members m
			JOIN extensions e ON e.id = m.extension_id WHERE m.group_id = $1 ORDER BY m.position, e.number`, id)
		return recipients, MsgScopePagingGroup, name, err
	case MsgScopeBroadcast:
		recipients, err = queryStrings(ctx, tx, `SELECT DISTINCT e.number FROM extensions e
			JOIN devices d ON d.extension_id = e.id AND d.enabled ORDER BY e.number`)
		return recipients, MsgScopeBroadcast, "", err
	}
	return nil, "", "", fmt.Errorf("store: unknown message target kind %q", in.TargetKind)
}

func queryStrings(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SendMessage stores a message for every recipient of its target and
// notifies the SIP nodes. An extension sending to a group is left out of the
// group's recipients.
func (s *Store) SendMessage(ctx context.Context, in MessageInput) (MessageSent, error) {
	var out MessageSent
	if len(in.Body) > MaxMessageBytes {
		return out, ErrMessageTooLarge
	}
	if in.ContentType == "" {
		in.ContentType = messageDefaultCType
	}
	if !ValidMessageContentType(in.ContentType) {
		return out, fmt.Errorf("store: invalid content type")
	}
	if in.SenderKind == "" {
		in.SenderKind = MsgSenderSystem
	}
	if in.SenderKind == MsgSenderSystem && in.Sender == "" {
		in.Sender = MsgSenderSystemName
	}
	text, raw := splitBody(in.ContentType, in.Body)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		out = MessageSent{}
		set, err := getMessageSettings(ctx, tx)
		if err != nil {
			return err
		}
		if !set.Enabled {
			return ErrMessagesDisabled
		}
		if in.SenderKind != MsgSenderSystem {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT batch) FROM messages
				WHERE sender = $1 AND sender_kind = $2 AND created_at > now() - interval '1 minute'`,
				in.Sender, in.SenderKind).Scan(&n); err != nil {
				return err
			}
			if n >= set.RatePerMinute {
				return ErrMessageRate
			}
		}
		if in.TargetKind == MsgKindExternal {
			var ok bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM trunks WHERE enabled AND messaging)`).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return ErrNoExternalTrunk
			}
		}
		recipients, scope, ref, err := resolveMessageTargets(ctx, tx, in)
		if err != nil {
			return err
		}
		if scope != MsgScopeDirect && in.SenderKind == MsgKindExtension {
			kept := recipients[:0:0]
			for _, r := range recipients {
				if r != in.Sender {
					kept = append(kept, r)
				}
			}
			recipients = kept
		}
		if len(recipients) == 0 {
			return fmt.Errorf("%w: no recipients", ErrMessageTarget)
		}
		if len(recipients) > MaxMessageRecipients {
			recipients = recipients[:MaxMessageRecipients]
		}
		recipientKind := MsgKindExtension
		if in.TargetKind == MsgKindExternal {
			recipientKind = MsgKindExternal
		}
		claimed := in.ClaimBy != "" && len(recipients) == 1
		status, claimBy := MsgPending, ""
		if claimed {
			status, claimBy = MsgSending, in.ClaimBy
		}
		var trunk any
		if in.TrunkID > 0 {
			trunk = in.TrunkID
		}
		batch := newBatch()
		rows, err := tx.QueryContext(ctx, `
			INSERT INTO messages (batch, sender, sender_kind, sender_name, actor, recipient, recipient_kind, scope, scope_ref,
				trunk_id, content_type, body_text, body_raw, status, claimed_by, claimed_at, expires_at)
			SELECT $1, $2, $3, $4, $5, r, $6, $7, $8, $9::bigint, $10, $11::text, $12::bytea, $13::text, $14,
				CASE WHEN $13::text = 'sending' THEN now() END, now() + make_interval(hours => $15::int)
			FROM unnest($16::text[]) WITH ORDINALITY AS t(r, ord) ORDER BY ord
			RETURNING id, recipient, created_at`,
			batch, in.Sender, in.SenderKind, in.SenderName, in.Actor, recipientKind, scope, ref,
			trunk, in.ContentType, text, raw, status, claimBy, set.TTLHours, recipients)
		if err != nil {
			return err
		}
		var jobs []MessageJob
		for rows.Next() {
			var j MessageJob
			if err := rows.Scan(&j.ID, &j.Recipient, &j.CreatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			jobs = append(jobs, j)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if recipientKind == MsgKindExtension {
			if _, err := tx.ExecContext(ctx, `
				UPDATE messages SET status = 'failed', detail = 'queue full: dropped'
				WHERE id IN (SELECT id FROM (
					SELECT id, row_number() OVER (PARTITION BY recipient ORDER BY id DESC) AS rn FROM messages
					WHERE status = 'pending' AND recipient_kind = 'extension' AND recipient = ANY($1)) q
					WHERE rn > $2)`, recipients, set.MaxQueuedPerExtension); err != nil {
				return err
			}
		}
		if !claimed {
			if _, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2)`, MessageChannel, batch); err != nil {
				return err
			}
		}
		out.Batch, out.Scope, out.Recipients = batch, scope, len(jobs)
		if claimed {
			for i := range jobs {
				jobs[i].Batch, jobs[i].Sender, jobs[i].SenderKind, jobs[i].SenderName = batch, in.Sender, in.SenderKind, in.SenderName
				jobs[i].RecipientKind, jobs[i].Scope, jobs[i].ContentType = recipientKind, scope, in.ContentType
				jobs[i].ScopeRef = ref
				jobs[i].Body, jobs[i].TrunkID = in.Body, in.TrunkID
			}
			out.Jobs = jobs
		}
		if in.Audit {
			return insertAudit(ctx, tx, in.Actor, "send_"+scope, "message", batch)
		}
		return nil
	})
	return out, err
}

// ExternalMessagingEnabled reports whether any enabled trunk has messaging
// turned on.
func (s *Store) ExternalMessagingEnabled(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM trunks WHERE enabled AND messaging)`).Scan(&ok)
	return ok, err
}

// UserExtensionNumber returns the number of the extension a user is linked
// to, or ErrNoExtensionLink.
func (s *Store) UserExtensionNumber(ctx context.Context, userID int64) (string, error) {
	var n sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT e.number FROM users u LEFT JOIN extensions e ON e.id = u.extension_id
		WHERE u.id = $1`, userID).Scan(&n)
	if err != nil {
		return "", mapErr(err)
	}
	if !n.Valid {
		return "", ErrNoExtensionLink
	}
	return n.String, nil
}

// MessageExtensionName returns an extension's display name ("" if unknown).
func (s *Store) MessageExtensionName(ctx context.Context, number string) string {
	var n string
	_ = s.db.QueryRowContext(ctx, `SELECT name FROM extensions WHERE number = $1`, number).Scan(&n)
	return n
}

// MessagePending lists what is waiting: the extensions with undelivered
// messages that may be tried now, and whether any external message waits.
func (s *Store) MessagePending(ctx context.Context) (extensions []string, external bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT recipient FROM messages
		WHERE status = 'pending' AND recipient_kind = 'extension' AND next_attempt_at <= now() AND expires_at > now()
		LIMIT 2000`)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, false, err
		}
		extensions = append(extensions, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE status = 'pending'
		AND recipient_kind = 'external' AND next_attempt_at <= now() AND expires_at > now())`).Scan(&external)
	return extensions, external, err
}

const jobCols = `id, batch, sender, sender_kind, sender_name, recipient, recipient_kind, scope, scope_ref, content_type,
	body_text, body_raw, COALESCE(trunk_id, 0), attempts, created_at`

func scanJob(r interface{ Scan(...any) error }) (MessageJob, error) {
	var (
		j    MessageJob
		text sql.NullString
		raw  []byte
	)
	if err := r.Scan(&j.ID, &j.Batch, &j.Sender, &j.SenderKind, &j.SenderName, &j.Recipient, &j.RecipientKind,
		&j.Scope, &j.ScopeRef, &j.ContentType, &text, &raw, &j.TrunkID, &j.Attempts, &j.CreatedAt); err != nil {
		return j, err
	}
	if text.Valid {
		j.Body = []byte(text.String)
	} else {
		j.Body = raw
	}
	return j, nil
}

// ClaimMessages claims up to limit pending messages for node: those to the
// given extensions and, when external is set, those to external numbers.
// The row lock is taken with SKIP LOCKED and the status moves to sending in
// the same statement, so a message is never given to two nodes.
func (s *Store) ClaimMessages(ctx context.Context, node string, extensions []string, external bool, limit int) ([]MessageJob, error) {
	if len(extensions) == 0 && !external {
		return nil, nil
	}
	if extensions == nil {
		extensions = []string{}
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH pick AS (
			SELECT id FROM messages
			WHERE status = 'pending' AND next_attempt_at <= now() AND expires_at > now()
			  AND ((recipient_kind = 'extension' AND recipient = ANY($2)) OR ($3 AND recipient_kind = 'external'))
			ORDER BY id LIMIT $4 FOR UPDATE SKIP LOCKED)
		UPDATE messages SET status = 'sending', claimed_at = now(), claimed_by = $1
		WHERE id IN (SELECT id FROM pick)
		RETURNING `+jobCols, node, extensions, external, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var jobs []MessageJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortJobs(jobs)
	return jobs, nil
}

func sortJobs(jobs []MessageJob) {
	for i := 1; i < len(jobs); i++ {
		for k := i; k > 0 && jobs[k].ID < jobs[k-1].ID; k-- {
			jobs[k], jobs[k-1] = jobs[k-1], jobs[k]
		}
	}
}

// Message delivery results.
const (
	// MsgResultDelivered: at least one contact took it.
	MsgResultDelivered = "delivered"
	// MsgResultNotRegistered: no contact is registered after all; the
	// message goes back to the queue without using an attempt.
	MsgResultNotRegistered = "not_registered"
	// MsgResultRetry: a transient failure; retried after a back-off.
	MsgResultRetry = "retry"
	// MsgResultFailed: a permanent failure.
	MsgResultFailed = "failed"
)

// FinishMessage records a claimed message's delivery result.
func (s *Store) FinishMessage(ctx context.Context, id int64, result, detail string) error {
	var (
		res sql.Result
		err error
	)
	switch result {
	case MsgResultDelivered:
		res, err = s.db.ExecContext(ctx, `UPDATE messages SET status = 'delivered', detail = $2, delivered_at = now(),
			attempts = attempts + 1 WHERE id = $1 AND status = 'sending'`, id, detail)
	case MsgResultNotRegistered:
		res, err = s.db.ExecContext(ctx, `UPDATE messages SET status = 'pending', detail = $2, next_attempt_at = now()
			WHERE id = $1 AND status = 'sending'`, id, detail)
	case MsgResultRetry:
		res, err = s.db.ExecContext(ctx, `
			UPDATE messages SET attempts = attempts + 1, detail = $2,
				status = CASE WHEN attempts + 1 >= $3 THEN 'failed' ELSE 'pending' END,
				next_attempt_at = now() + make_interval(secs => LEAST(15 * power(2, attempts), 900))
			WHERE id = $1 AND status = 'sending'`, id, detail, MaxMessageAttempts)
	default:
		res, err = s.db.ExecContext(ctx, `UPDATE messages SET status = 'failed', detail = $2, attempts = attempts + 1
			WHERE id = $1 AND status = 'sending'`, id, detail)
	}
	if err != nil {
		return err
	}
	return requireRow(res)
}

// MaintainMessages expires undelivered messages past their TTL, returns
// messages whose node died to the queue and deletes history older than the
// retention. Every step is idempotent, so any node may run it.
func (s *Store) MaintainMessages(ctx context.Context) (expired, purged int64, err error) {
	set, err := s.GetMessageSettings(ctx)
	if err != nil {
		return 0, 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET status = 'failed', detail = 'expired: not delivered in time'
		WHERE status = 'pending' AND expires_at <= now()`)
	if err != nil {
		return 0, 0, err
	}
	expired, _ = res.RowsAffected()
	if _, err = s.db.ExecContext(ctx, `
		UPDATE messages SET attempts = attempts + 1, detail = 'node lost',
			status = CASE WHEN attempts + 1 >= $2 THEN 'failed' ELSE 'pending' END, next_attempt_at = now()
		WHERE status = 'sending' AND claimed_at < now() - make_interval(secs => $1)`,
		messageStaleAfter.Seconds(), MaxMessageAttempts); err != nil {
		return expired, 0, err
	}
	res, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE created_at < now() - make_interval(days => $1)
		AND status IN ('delivered', 'failed')`, set.RetentionDays)
	if err != nil {
		return expired, 0, err
	}
	purged, _ = res.RowsAffected()
	return expired, purged, nil
}

// PurgeMessages deletes finished messages older than the given days (0
// deletes every finished message) and audits it.
func (s *Store) PurgeMessages(ctx context.Context, actor string, olderThanDays int) (int64, error) {
	var n int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE created_at < now() - make_interval(days => $1)
			AND status IN ('delivered', 'failed')`, olderThanDays)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return insertAudit(ctx, tx, actor, "purge", "message", fmt.Sprintf("older-than-%dd", olderThanDays))
	})
	return n, err
}

// convKey is the SQL expression naming a message's conversation: a direct
// thread by its two sorted parties, a group thread by group, broadcasts as
// one thread.
const convKey = `CASE m.scope
	WHEN 'direct' THEN least(m.sender, m.recipient) || '>' || greatest(m.sender, m.recipient)
	WHEN 'broadcast' THEN 'broadcast'
	ELSE m.scope || ':' || m.scope_ref END`

// messageWhere builds the filter shared by the listings.
func messageWhere(f MessageFilter, args *[]any) string {
	var w []string
	add := func(cond string, v any) {
		*args = append(*args, v)
		w = append(w, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(*args))))
	}
	if f.Extension != "" {
		add("(m.sender = ? OR m.recipient = ?)", f.Extension)
	}
	if f.Conversation != "" {
		add("("+convKey+") = ?", f.Conversation)
	}
	if f.Status != "" {
		add("m.status = ?", f.Status)
	}
	if f.Query != "" {
		add(`(position(lower(?) in lower(coalesce(m.body_text, ''))) > 0 OR position(lower(?) in lower(m.sender)) > 0
			OR position(lower(?) in lower(m.recipient)) > 0 OR position(lower(?) in lower(m.scope_ref)) > 0)`, f.Query)
	}
	if len(w) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w, " AND ")
}

// ListMessages returns one entry per send (a group send is one entry with
// its recipient counts), newest first, before the cursor id.
func (s *Store) ListMessages(ctx context.Context, f MessageFilter, before int64, limit int) ([]Message, error) {
	args := []any{}
	where := messageWhere(f, &args)
	if before > 0 {
		args = append(args, before)
		cond := fmt.Sprintf("m.id < $%d", len(args))
		if where == "" {
			where = " WHERE " + cond
		} else {
			where += " AND " + cond
		}
	}
	args = append(args, limit)
	//nolint:gosec // G202: only constants and numbered placeholders are joined into the SQL; values are bound.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, batch, sender, sender_kind, sender_name, actor, recipient, recipient_kind, scope, scope_ref, trunk, content_type,
		       body_text, body_len, status, detail, attempts, created_at, expires_at, delivered_at, n, nd, np, nf
		FROM (
			SELECT DISTINCT ON (m.batch) m.id, m.batch, m.sender, m.sender_kind, m.sender_name, m.actor, m.recipient,
			       m.recipient_kind, m.scope, m.scope_ref, COALESCE(t.name, '') AS trunk, m.content_type, m.body_text,
			       COALESCE(length(m.body_raw), octet_length(m.body_text)) AS body_len, m.status, m.detail, m.attempts,
			       m.created_at, m.expires_at, m.delivered_at,
			       count(*) OVER w AS n,
			       count(*) FILTER (WHERE m.status = 'delivered') OVER w AS nd,
			       count(*) FILTER (WHERE m.status IN ('pending', 'sending')) OVER w AS np,
			       count(*) FILTER (WHERE m.status = 'failed') OVER w AS nf
			FROM messages m LEFT JOIN trunks t ON t.id = m.trunk_id`+where+`
			WINDOW w AS (PARTITION BY m.batch)
			ORDER BY m.batch, m.id) q
		ORDER BY id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Message{}
	for rows.Next() {
		var (
			m    Message
			text sql.NullString
			dat  sql.NullTime
		)
		if err := rows.Scan(&m.ID, &m.Batch, &m.Sender, &m.SenderKind, &m.SenderName, &m.Actor, &m.Recipient, &m.RecipientKind,
			&m.Scope, &m.ScopeRef, &m.Trunk, &m.ContentType, &text, &m.BodyBytes, &m.Status, &m.Detail, &m.Attempts,
			&m.CreatedAt, &m.ExpiresAt, &dat, &m.Recipients, &m.Delivered, &m.Pending, &m.FailedN); err != nil {
			return nil, err
		}
		m.Body = text.String
		m.DeliveredAt = nullTime(dat)
		m.Status = BatchStatus(m.Recipients, m.Delivered, m.Pending, m.FailedN, m.Status)
		out = append(out, m)
	}
	return out, rows.Err()
}

// BatchStatus is a send's overall status: pending while any recipient still
// waits, else delivered when any recipient got it, else failed.
func BatchStatus(total, delivered, pending, failed int, own string) string {
	switch {
	case total <= 1:
		if own == MsgSending {
			return MsgPending
		}
		return own
	case pending > 0:
		return MsgPending
	case delivered > 0:
		return MsgDelivered
	case failed > 0:
		return MsgFailed
	}
	return own
}

// ListMessageConversations returns the conversations matching f, the most
// recently active first.
func (s *Store) ListMessageConversations(ctx context.Context, f MessageFilter, limit int) ([]MessageConversation, error) {
	f.Conversation = ""
	args := []any{}
	where := messageWhere(f, &args)
	args = append(args, limit)
	//nolint:gosec // G202: only constants and numbered placeholders are joined into the SQL; values are bound.
	rows, err := s.db.QueryContext(ctx, `
		WITH keyed AS (
			SELECT m.id, m.batch, m.scope, m.scope_ref, (`+convKey+`) AS k FROM messages m`+where+`)
		SELECT c.k, c.scope, c.scope_ref, c.n, c.last_id, m.created_at, m.sender, m.recipient, m.status,
		       left(coalesce(m.body_text, ''), 120)
		FROM (SELECT k, (array_agg(scope))[1] AS scope, (array_agg(scope_ref))[1] AS scope_ref,
		             count(DISTINCT batch) AS n, max(id) AS last_id FROM keyed GROUP BY k) c
		JOIN messages m ON m.id = c.last_id
		ORDER BY c.last_id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []MessageConversation{}
	for rows.Next() {
		var c MessageConversation
		if err := rows.Scan(&c.Key, &c.Scope, &c.ScopeRef, &c.Messages, &c.LastID, &c.LastAt, &c.LastSender,
			&c.LastRecip, &c.LastStatus, &c.LastSnippet); err != nil {
			return nil, err
		}
		if c.Scope == MsgScopeDirect {
			c.Parties = strings.SplitN(c.Key, ">", 2)
		} else {
			c.Parties = []string{}
		}
		if c.LastStatus == MsgSending {
			c.LastStatus = MsgPending
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
