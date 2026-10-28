package store

// The unified inbox (spec messaging-channels M-12, M-22, M-25): conversations
// and their messages, the idempotent ingest the SIP nodes drive, sending a
// reply through the SIP text queue, assignment by hand and claim, and the
// live-update log the inbox stream resumes from.
//
// Everything is keyed by ids and rows in PostgreSQL; nothing is held only in
// memory, so a pod restart loses nothing. Message text is never logged and
// never put in an audit row.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
)

// InboxAccess says what part of the inbox a caller reaches. A supervisor
// (All) reaches every conversation; anyone else reaches the conversations
// assigned to them and those they replied to.
type InboxAccess struct {
	UserID int64
	All    bool
	// Actor is the audit identity ("user:alice").
	Actor string
	// Name is the display name written on the lines the caller adds.
	Name string
}

// visible is the SQL condition for a conversation c the caller reaches; it
// uses $1 (the user) and $2 (All).
const visible = `($2::boolean OR c.assignee_user_id = $1 OR EXISTS (
	SELECT 1 FROM conversation_messages vx WHERE vx.conversation_id = c.id AND vx.sender_user_id = $1 AND vx.kind = 'message'))`

// Conversation is one customer on one of our numbers.
type Conversation struct {
	ID              int64      `json:"id"`
	ChannelID       int64      `json:"channelId"`
	ChannelKind     string     `json:"channelKind"`
	ChannelName     string     `json:"channelName"`
	AddressID       int64      `json:"addressId"`
	Address         string     `json:"address"`
	Peer            string     `json:"peer"`
	PeerKind        string     `json:"peerKind"`
	PeerName        string     `json:"peerName"`
	PeerNameSource  string     `json:"peerNameSource"`
	Status          string     `json:"status"`
	SnoozedUntil    *time.Time `json:"snoozedUntil"`
	AssigneeUserID  *int64     `json:"assigneeUserId"`
	AssigneeName    string     `json:"assigneeName"`
	Priority        string     `json:"priority"`
	Tags            []string   `json:"tags"`
	Preview         string     `json:"preview"`
	Unread          int        `json:"unread"`
	OptedOut        bool       `json:"optedOut"`
	LastMessageAt   time.Time  `json:"lastMessageAt"`
	LastInboundAt   *time.Time `json:"lastInboundAt"`
	FirstResponseAt *time.Time `json:"firstResponseAt"`
	ResolvedAt      *time.Time `json:"resolvedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ConvMessage is one line of a conversation.
type ConvMessage struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversationId"`
	Direction      string     `json:"direction"`
	Kind           string     `json:"kind"`
	SenderKind     string     `json:"senderKind"`
	SenderUserID   *int64     `json:"senderUserId"`
	SenderName     string     `json:"senderName"`
	Body           string     `json:"body"`
	Status         string     `json:"status"`
	ErrorCode      string     `json:"errorCode"`
	ClientMsgID    string     `json:"clientMsgId"`
	CreatedAt      time.Time  `json:"createdAt"`
	SentAt         *time.Time `json:"sentAt"`
	DeliveredAt    *time.Time `json:"deliveredAt"`
	ReadAt         *time.Time `json:"readAt"`
}

const convSelect = `SELECT c.id, c.channel_id, ch.kind, ch.name, c.address_id, a.address, c.peer_key, c.peer_kind,
	c.peer_name, c.peer_name_source, c.status, c.snoozed_until, c.assignee_user_id,
	coalesce(NULLIF(u.display_name, ''), u.username, ''), c.priority, to_json(c.tags)::text,
	coalesce(left(m.body, 160), ''),
	(SELECT count(*) FROM conversation_messages x WHERE x.conversation_id = c.id AND x.direction = 'in'
		AND x.kind = 'message' AND x.id > coalesce(r.last_read_message_id, 0)),
	EXISTS (SELECT 1 FROM sms_opt_outs o WHERE o.address_id = c.address_id AND o.peer_key = c.peer_key AND o.state = 'out'),
	c.last_message_at, c.last_inbound_at, c.first_response_at, c.resolved_at, c.created_at
	FROM conversations c
	JOIN msg_channels ch ON ch.id = c.channel_id
	JOIN msg_addresses a ON a.id = c.address_id
	LEFT JOIN users u ON u.id = c.assignee_user_id
	LEFT JOIN conversation_messages m ON m.id = c.last_message_id
	LEFT JOIN conversation_reads r ON r.conversation_id = c.id AND r.user_id = $1`

func scanConversation(r interface{ Scan(...any) error }) (Conversation, error) {
	var (
		c        Conversation
		snoozed  sql.NullTime
		assignee sql.NullInt64
		lastIn   sql.NullTime
		first    sql.NullTime
		resolved sql.NullTime
		tags     string
	)
	if err := r.Scan(&c.ID, &c.ChannelID, &c.ChannelKind, &c.ChannelName, &c.AddressID, &c.Address, &c.Peer, &c.PeerKind,
		&c.PeerName, &c.PeerNameSource, &c.Status, &snoozed, &assignee, &c.AssigneeName, &c.Priority, &tags,
		&c.Preview, &c.Unread, &c.OptedOut, &c.LastMessageAt, &lastIn, &first, &resolved, &c.CreatedAt); err != nil {
		return c, err
	}
	c.SnoozedUntil, c.LastInboundAt, c.FirstResponseAt, c.ResolvedAt = nullTime(snoozed), nullTime(lastIn), nullTime(first), nullTime(resolved)
	if assignee.Valid {
		c.AssigneeUserID = &assignee.Int64
	}
	if err := json.Unmarshal([]byte(tags), &c.Tags); err != nil {
		return c, err
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	return c, nil
}

// ConvCursor is the position after the last conversation of a page.
type ConvCursor struct {
	At time.Time
	ID int64
}

// String encodes the cursor as the value a client passes back.
func (c ConvCursor) String() string {
	return strconv.FormatInt(c.At.UnixMicro(), 10) + "_" + strconv.FormatInt(c.ID, 10)
}

// ParseConvCursor decodes a cursor String made.
func ParseConvCursor(v string) (ConvCursor, bool) {
	a, b, ok := strings.Cut(v, "_")
	if !ok {
		return ConvCursor{}, false
	}
	us, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || id <= 0 || us <= 0 {
		return ConvCursor{}, false
	}
	return ConvCursor{At: time.UnixMicro(us).UTC(), ID: id}, true
}

// ConvFilter selects conversations.
type ConvFilter struct {
	// View is mine, unassigned or all (empty is all).
	View string
	// Status is open, pending, snoozed, resolved or spam; empty means the
	// active ones (open, pending, snoozed); "any" means every one.
	Status    string
	Q         string
	ChannelID int64
	After     *ConvCursor
}

// ListConversations returns a page of conversations, most recently active
// first, and the cursor of the next page ("" when this is the last).
func (s *Store) ListConversations(ctx context.Context, acc InboxAccess, f ConvFilter, limit int) ([]Conversation, string, error) {
	args := []any{acc.UserID, acc.All}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{visible}
	switch f.View {
	case "mine":
		where = append(where, "c.assignee_user_id = $1")
	case "unassigned":
		where = append(where, "c.assignee_user_id IS NULL")
	}
	switch f.Status {
	case "":
		where = append(where, "c.status IN ('open', 'pending', 'snoozed')")
	case "any":
	default:
		where = append(where, "c.status = "+arg(f.Status))
	}
	if f.ChannelID > 0 {
		where = append(where, "c.channel_id = "+arg(f.ChannelID))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		like, text := arg(escapeLike(q)), arg(q)
		where = append(where, `(c.peer_key LIKE '%' || `+like+` OR c.peer_name ILIKE '%' || `+like+` || '%'
			OR EXISTS (SELECT 1 FROM conversation_messages sx WHERE sx.conversation_id = c.id
				AND to_tsvector('simple', sx.body) @@ plainto_tsquery('simple', `+text+`)))`)
	}
	if f.After != nil {
		where = append(where, "(c.last_message_at, c.id) < ("+arg(f.After.At)+"::timestamptz, "+arg(f.After.ID)+"::bigint)")
	}
	rows, err := s.db.QueryContext(ctx, convSelect+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY c.last_message_at DESC, c.id DESC LIMIT `+arg(limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = ConvCursor{At: out[limit-1].LastMessageAt, ID: out[limit-1].ID}.String()
	}
	return out, next, nil
}

// GetConversation returns one conversation the caller reaches; one they do
// not is not found. A supervisor opening one that is not theirs leaves an
// audit row (once an hour per conversation).
func (s *Store) GetConversation(ctx context.Context, acc InboxAccess, id int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx, convSelect+` WHERE c.id = $3 AND `+visible, acc.UserID, acc.All, id))
	if err != nil {
		return c, mapErr(err)
	}
	s.auditSupervisorView(ctx, acc, c)
	return c, nil
}

// auditSupervisorView records that a supervisor read a conversation that is
// not assigned to them. It never carries content and never fails a read.
func (s *Store) auditSupervisorView(ctx context.Context, acc InboxAccess, c Conversation) {
	if !acc.All || (c.AssigneeUserID != nil && *c.AssigneeUserID == acc.UserID) {
		return
	}
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (actor, action, resource, resource_id, via)
		SELECT $1, 'supervisor_view', 'conversation', $2, $3
		WHERE NOT EXISTS (SELECT 1 FROM audit_events WHERE actor = $1 AND action = 'supervisor_view'
			AND resource = 'conversation' AND resource_id = $2 AND at > now() - interval '1 hour')`,
		acc.Actor, strconv.FormatInt(c.ID, 10), auditVia(ctx))
}

const convMessageCols = `id, conversation_id, direction, kind, sender_kind, sender_user_id, sender_name, body, status, error_code,
	coalesce(client_msg_id, ''), created_at, sent_at, delivered_at, read_at`

func scanConvMessage(r interface{ Scan(...any) error }) (ConvMessage, error) {
	var (
		m                 ConvMessage
		user              sql.NullInt64
		sent, deliv, read sql.NullTime
	)
	if err := r.Scan(&m.ID, &m.ConversationID, &m.Direction, &m.Kind, &m.SenderKind, &user, &m.SenderName, &m.Body,
		&m.Status, &m.ErrorCode, &m.ClientMsgID, &m.CreatedAt, &sent, &deliv, &read); err != nil {
		return m, err
	}
	if user.Valid {
		m.SenderUserID = &user.Int64
	}
	m.SentAt, m.DeliveredAt, m.ReadAt = nullTime(sent), nullTime(deliv), nullTime(read)
	return m, nil
}

// ListConversationMessages returns a page of a conversation's lines, newest
// first, the ones before id `before` when it is above 0, and the id to pass
// as `before` for the next page (0 when this is the last).
func (s *Store) ListConversationMessages(ctx context.Context, acc InboxAccess, convID, before int64, limit int) ([]ConvMessage, int64, error) {
	c, err := s.GetConversation(ctx, acc, convID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+convMessageCols+` FROM conversation_messages
		WHERE conversation_id = $1 AND ($2 = 0 OR id < $2) ORDER BY id DESC LIMIT $3`, c.ID, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConvMessage
	for rows.Next() {
		m, err := scanConvMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// convRow is a locked conversation, what the mutations need of it.
type convRow struct {
	ID        int64
	ChannelID int64
	AddressID int64
	PeerKey   string
	Status    string
	Assignee  sql.NullInt64
}

// lockConversation locks a conversation the caller reaches for update.
func lockConversation(ctx context.Context, tx *sql.Tx, acc InboxAccess, id int64) (convRow, error) {
	var c convRow
	err := tx.QueryRowContext(ctx, `
		SELECT c.id, c.channel_id, c.address_id, c.peer_key, c.status, c.assignee_user_id
		FROM conversations c WHERE c.id = $3 AND `+visible+` FOR UPDATE OF c`,
		acc.UserID, acc.All, id).Scan(&c.ID, &c.ChannelID, &c.AddressID, &c.PeerKey, &c.Status, &c.Assignee)
	return c, mapErr(err)
}

// inboxEventLock serialises the writers of conversation_events, so the ids
// of committed events are in commit order and a stream resuming from the
// last id it saw never skips one.
const inboxEventLock = 0x68656c6c6f696e62 // "helloinb"

// emitConversationEvent tells everyone the conversation concerns: its
// assignee, or every supervisor while it is unassigned, plus extra users.
func emitConversationEvent(ctx context.Context, tx *sql.Tx, convID, msgID int64, typ string, extra []int64) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(inboxEventLock)); err != nil {
		return err
	}
	var msg any
	if msgID > 0 {
		msg = msgID
	}
	if extra == nil {
		extra = []int64{}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_events (user_id, type, conversation_id, message_id)
		SELECT u.id, $2, $1, $3::bigint FROM users u, (SELECT assignee_user_id AS a FROM conversations WHERE id = $1) c
		WHERE u.enabled AND (u.id = c.a OR (c.a IS NULL AND u.role IN ('operator', 'admin')) OR u.id = ANY($4::bigint[]))`,
		convID, typ, msg, extra)
	return err
}

// addLine writes a note or a system line into a conversation. It does not
// move the conversation's last message.
func addLine(ctx context.Context, tx *sql.Tx, c convRow, kind, senderKind string, userID int64, name, body string) (int64, error) {
	var user any
	if userID > 0 {
		user = userID
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, sender_user_id, sender_name, body, status)
		VALUES ($1, $2, 'out', $3, $4, $5, $6, $7, 'delivered') RETURNING id`,
		c.ID, c.ChannelID, kind, senderKind, user, name, body).Scan(&id)
	return id, err
}

// AddNote writes an internal note: seen by agents and supervisors, never
// sent.
func (s *Store) AddNote(ctx context.Context, acc InboxAccess, convID int64, body string) (ConvMessage, error) {
	var out ConvMessage
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		id, err := addLine(ctx, tx, c, messaging.KindNote, messaging.SenderUser, acc.UserID, acc.Name, body)
		if err != nil {
			return err
		}
		if err := emitConversationEvent(ctx, tx, c.ID, id, "conversation.updated", nil); err != nil {
			return err
		}
		out, err = scanConvMessage(tx.QueryRowContext(ctx, `SELECT `+convMessageCols+` FROM conversation_messages WHERE id = $1`, id))
		return err
	})
	return out, err
}

// SendInput is a reply an agent sends to the customer.
type SendInput struct {
	ConversationID int64
	Body           string
	// ClientMsgID makes a retried send idempotent; empty means none.
	ClientMsgID string
}

// SendConversationText sends an agent's reply through the conversation's
// channel. For a SIP trunk channel the text is queued in the SIP text queue
// in the same transaction as the message row, so either both exist or
// neither does, and its delivery result (sent when the trunk accepts it,
// failed when it refuses) reaches the message. Replying to an unassigned
// conversation claims it; replying to a resolved one reopens it.
//
// created is false when ClientMsgID matched an earlier send: that message is
// returned and nothing is sent again. A customer who opted out is refused
// (ErrOptedOut) before anything is queued; no one can override it.
func (s *Store) SendConversationText(ctx context.Context, acc InboxAccess, in SendInput) (msg ConvMessage, created bool, err error) {
	var sinkEvent *messaging.Event
	err = s.tx(ctx, func(tx *sql.Tx) error {
		sinkEvent = nil
		c, err := lockConversation(ctx, tx, acc, in.ConversationID)
		if err != nil {
			return err
		}
		if in.ClientMsgID != "" {
			m, err := scanConvMessage(tx.QueryRowContext(ctx, `SELECT `+convMessageCols+`
				FROM conversation_messages WHERE conversation_id = $1 AND client_msg_id = $2`, c.ID, in.ClientMsgID))
			if err == nil {
				msg, created = m, false
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if c.Status == messaging.StatusSpam {
			return ErrConversationState
		}
		var (
			provider, address, kind string
			chEnabled, adEnabled    bool
			trunk                   sql.NullInt64
		)
		if err := tx.QueryRowContext(ctx, `
			SELECT ch.provider, ch.kind, ch.enabled, ch.trunk_id, a.address, a.enabled
			FROM msg_channels ch JOIN msg_addresses a ON a.channel_id = ch.id WHERE ch.id = $1 AND a.id = $2`,
			c.ChannelID, c.AddressID).Scan(&provider, &kind, &chEnabled, &trunk, &address, &adEnabled); err != nil {
			return err
		}
		if !chEnabled || !adEnabled || provider != messaging.ProviderSIPTrunk || !trunk.Valid {
			return ErrChannelUnavailable
		}
		out, err := optedOut(ctx, tx, s, c.AddressID, c.PeerKey)
		if err != nil {
			return err
		}
		if out {
			return ErrOptedOut
		}
		var clientID any
		if in.ClientMsgID != "" {
			clientID = in.ClientMsgID
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, sender_user_id,
				sender_name, body, status, client_msg_id)
			VALUES ($1, $2, 'out', 'message', 'user', $3, $4, $5, 'sending', $6) RETURNING id`,
			c.ID, c.ChannelID, acc.UserID, acc.Name, in.Body, clientID).Scan(&id); err != nil {
			return err
		}
		for _, part := range messaging.SplitText(in.Body, messaging.MaxPartBytes) {
			text, raw := splitBody(messageDefaultCType, []byte(part))
			var sent MessageSent
			if err := s.sendMessageTx(ctx, tx, MessageInput{
				Sender: address, SenderKind: MsgSenderSystem, SenderName: "", Actor: acc.Actor,
				TargetKind: MsgKindExternal, Target: c.PeerKey, ContentType: messageDefaultCType, Body: []byte(part),
				TrunkID: trunk.Int64, ConversationMessageID: id,
			}, text, raw, &sent); err != nil {
				return err
			}
		}
		// Replying claims an unassigned conversation and reopens a resolved
		// or snoozed one; the first reply stamps the first response.
		claimed := !c.Assignee.Valid
		if _, err := tx.ExecContext(ctx, `
			UPDATE conversations SET last_message_id = $2, last_message_at = now(),
				assignee_user_id = coalesce(assignee_user_id, $3),
				first_response_at = coalesce(first_response_at, now()),
				status = CASE WHEN status IN ('resolved', 'snoozed') THEN 'open' ELSE status END,
				resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END,
				snoozed_until = NULL
			WHERE id = $1`, c.ID, id, acc.UserID); err != nil {
			return err
		}
		if claimed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, to_user_id, reason) VALUES ($1, $2, 'reply')`,
				c.ID, acc.UserID); err != nil {
				return err
			}
		}
		if err := emitConversationEvent(ctx, tx, c.ID, id, "message.sent", []int64{acc.UserID}); err != nil {
			return err
		}
		m, err := scanConvMessage(tx.QueryRowContext(ctx, `SELECT `+convMessageCols+` FROM conversation_messages WHERE id = $1`, id))
		if err != nil {
			return err
		}
		msg, created = m, true
		sinkEvent = &messaging.Event{Type: "message.sent", ConversationID: c.ID, MessageID: id, ChannelKind: kind,
			Direction: messaging.DirOut, Status: m.Status}
		return nil
	})
	if err == nil && sinkEvent != nil {
		s.emit(ctx, *sinkEvent)
	}
	return msg, created, err
}

// optedOut reports whether the customer opted out of the address.
func optedOut(ctx context.Context, tx *sql.Tx, s *Store, addressID int64, peerKey string) (bool, error) {
	h, err := s.peerHash(peerKey)
	if err != nil {
		return false, err
	}
	var out bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sms_opt_outs WHERE address_id = $1 AND peer_hash = $2 AND state = 'out')`,
		addressID, h).Scan(&out)
	return out, err
}

// WithMessagingEvents sets where the inbox's events go (the integrations
// pipeline, when it lands) and returns s. The default drops them.
func (s *Store) WithMessagingEvents(e messaging.EventSink) *Store {
	s.msgEvents = e
	return s
}

func (s *Store) emit(ctx context.Context, e messaging.Event) {
	if s.msgEvents != nil {
		s.msgEvents.Emit(ctx, e)
	}
}

// eligibleAgent reports whether the user can take conversations: enabled,
// and a role that has the inbox.
func eligibleAgent(ctx context.Context, tx *sql.Tx, userID int64) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `
		SELECT coalesce(NULLIF(display_name, ''), username) FROM users
		WHERE id = $1 AND enabled AND role IN ('user', 'operator', 'admin')`, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotAgent
	}
	return name, err
}

// ClaimConversation assigns an unassigned conversation to the caller. The
// assignment is a compare-and-set in PostgreSQL, so two agents (or two
// control pods) cannot both win: the loser gets ErrConversationState.
func (s *Store) ClaimConversation(ctx context.Context, acc InboxAccess, convID int64) (Conversation, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		if c.Assignee.Valid || c.Status == messaging.StatusResolved || c.Status == messaging.StatusSpam {
			return ErrConversationState
		}
		res, err := tx.ExecContext(ctx, `UPDATE conversations SET assignee_user_id = $2 WHERE id = $1 AND assignee_user_id IS NULL`, c.ID, acc.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrConversationState
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, to_user_id, reason) VALUES ($1, $2, 'claim')`, c.ID, acc.UserID); err != nil {
			return err
		}
		if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", acc.Name+" took the conversation"); err != nil {
			return err
		}
		// The supervisors saw it as unassigned, so they are told it moved.
		return emitPoolEvent(ctx, tx, c.ID, "conversation.assigned", acc.UserID)
	})
	if err != nil {
		return Conversation{}, err
	}
	return s.GetConversation(ctx, acc, convID)
}

// emitPoolEvent tells the supervisors and the new assignee that a
// conversation left the pool or moved.
func emitPoolEvent(ctx context.Context, tx *sql.Tx, convID int64, typ string, toUser int64) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(inboxEventLock)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_events (user_id, type, conversation_id)
		SELECT u.id, $2, $1 FROM users u WHERE u.enabled AND (u.role IN ('operator', 'admin') OR u.id = $3)`,
		convID, typ, toUser)
	return err
}

// AssignConversation hands a conversation to a user, or to nobody (the pool)
// when toUser is 0. A supervisor may assign any conversation; anyone else
// only one assigned to them.
func (s *Store) AssignConversation(ctx context.Context, acc InboxAccess, convID, toUser int64) (Conversation, error) {
	var ev *messaging.Event
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ev = nil
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		if !acc.All && (!c.Assignee.Valid || c.Assignee.Int64 != acc.UserID) {
			return ErrConversationState
		}
		if c.Status == messaging.StatusResolved || c.Status == messaging.StatusSpam {
			return ErrConversationState
		}
		name := "the pool"
		if toUser > 0 {
			if name, err = eligibleAgent(ctx, tx, toUser); err != nil {
				return err
			}
		} else if !acc.All {
			return ErrNotAgent
		}
		var to any
		if toUser > 0 {
			to = toUser
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET assignee_user_id = $2 WHERE id = $1`, c.ID, to); err != nil {
			return err
		}
		var from any
		if c.Assignee.Valid {
			from = c.Assignee.Int64
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conversation_assignments (conversation_id, from_user_id, to_user_id, reason) VALUES ($1, $2, $3, 'manual')`,
			c.ID, from, to); err != nil {
			return err
		}
		if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", acc.Name+" assigned the conversation to "+name); err != nil {
			return err
		}
		extra := []int64{acc.UserID}
		if c.Assignee.Valid {
			extra = append(extra, c.Assignee.Int64)
		}
		if err := emitConversationEvent(ctx, tx, c.ID, 0, "conversation.assigned", extra); err != nil {
			return err
		}
		ev = &messaging.Event{Type: "conversation.assigned", ConversationID: c.ID, AssigneeUserID: toUser}
		return nil
	})
	if err != nil {
		return Conversation{}, err
	}
	if ev != nil {
		s.emit(ctx, *ev)
	}
	// An agent who handed the conversation over no longer reaches it, but
	// still gets its new state back.
	return s.conversationByID(ctx, acc.UserID, true, convID)
}

// conversationByID reads a conversation the caller has just changed, with no
// access check beyond the change's own and no audit row.
func (s *Store) conversationByID(ctx context.Context, userID int64, all bool, id int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx, convSelect+` WHERE c.id = $3 AND `+visible, userID, all, id))
	return c, mapErr(err)
}

// SetConversationStatus changes a conversation's state. Snoozing needs a
// future time; resolving stamps resolved_at and reopening clears it.
func (s *Store) SetConversationStatus(ctx context.Context, acc InboxAccess, convID int64, status string, until *time.Time) (Conversation, error) {
	var ev *messaging.Event
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ev = nil
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		if c.Status == status && status != messaging.StatusSnoozed {
			return nil
		}
		var snooze any
		if status == messaging.StatusSnoozed {
			snooze = *until
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE conversations SET status = $2, snoozed_until = $3::timestamptz,
				resolved_at = CASE WHEN $2 = 'resolved' THEN now() ELSE NULL END
			WHERE id = $1`, c.ID, status, snooze); err != nil {
			return err
		}
		line := map[string]string{
			messaging.StatusResolved: " resolved the conversation",
			messaging.StatusOpen:     " reopened the conversation",
			messaging.StatusPending:  " is waiting on the customer",
			messaging.StatusSnoozed:  " snoozed the conversation",
			messaging.StatusSpam:     " marked the conversation as spam",
		}[status]
		if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", acc.Name+line); err != nil {
			return err
		}
		if err := emitConversationEvent(ctx, tx, c.ID, 0, "conversation.updated", []int64{acc.UserID}); err != nil {
			return err
		}
		if status == messaging.StatusResolved {
			ev = &messaging.Event{Type: "conversation.resolved", ConversationID: c.ID, Status: status}
		}
		return nil
	})
	if err != nil {
		return Conversation{}, err
	}
	if ev != nil {
		s.emit(ctx, *ev)
	}
	return s.GetConversation(ctx, acc, convID)
}

// SetConversationTags replaces a conversation's tags (already cleaned).
func (s *Store) SetConversationTags(ctx context.Context, acc InboxAccess, convID int64, tags []string) (Conversation, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET tags = $2::text[] WHERE id = $1`, c.ID, tags); err != nil {
			return err
		}
		return emitConversationEvent(ctx, tx, c.ID, 0, "conversation.updated", []int64{acc.UserID})
	})
	if err != nil {
		return Conversation{}, err
	}
	return s.GetConversation(ctx, acc, convID)
}

// MarkConversationRead records that the caller has read up to the
// conversation's last message.
func (s *Store) MarkConversationRead(ctx context.Context, acc InboxAccess, convID int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO conversation_reads (user_id, conversation_id, last_read_message_id)
			SELECT $1, c.id, coalesce(c.last_message_id, 0) FROM conversations c WHERE c.id = $2
			ON CONFLICT (user_id, conversation_id) DO UPDATE
				SET last_read_message_id = GREATEST(conversation_reads.last_read_message_id, EXCLUDED.last_read_message_id), at = now()`,
			acc.UserID, c.ID)
		return err
	})
}

// BulkResult is what a bulk action did.
type BulkResult struct {
	Done   int `json:"done"`
	Failed int `json:"failed"`
}

// BulkConversations applies one action to many conversations on a
// supervisor's behalf and audits it (counts, no content). Each conversation
// is its own transaction; one that does not fit counts as failed.
func (s *Store) BulkConversations(ctx context.Context, acc InboxAccess, ids []int64, action string, toUser int64) (BulkResult, error) {
	var res BulkResult
	for _, id := range ids {
		var err error
		switch action {
		case "resolve":
			_, err = s.SetConversationStatus(ctx, acc, id, messaging.StatusResolved, nil)
		case "reopen":
			_, err = s.SetConversationStatus(ctx, acc, id, messaging.StatusOpen, nil)
		case "assign":
			_, err = s.AssignConversation(ctx, acc, id, toUser)
		default:
			return res, fmt.Errorf("store: unknown bulk action %q", action)
		}
		if err != nil {
			res.Failed++
			continue
		}
		res.Done++
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		return insertAudit(ctx, tx, acc.Actor, fmt.Sprintf("bulk_%s done=%d failed=%d", action, res.Done, res.Failed), "conversation", "-")
	})
	return res, err
}

// InboxAgent is a user who can take conversations.
type InboxAgent struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

// ListInboxAgents lists the users conversations can be assigned to.
func (s *Store) ListInboxAgents(ctx context.Context) ([]InboxAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, display_name FROM users
		WHERE enabled AND role IN ('user', 'operator', 'admin') ORDER BY lower(coalesce(NULLIF(display_name, ''), username)), id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []InboxAgent
	for rows.Next() {
		var a InboxAgent
		if err := rows.Scan(&a.ID, &a.Username, &a.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// InboxStatus is what the navigation needs: whether the inbox has a channel
// and how many of the caller's conversations have unread customer text.
type InboxStatus struct {
	Enabled bool `json:"enabled"`
	Unread  int  `json:"unread"`
	// Unassigned counts the active conversations nobody has taken (a
	// supervisor's pool); it is 0 for anyone else.
	Unassigned int `json:"unassigned"`
}

// GetInboxStatus reads the caller's inbox status.
func (s *Store) GetInboxStatus(ctx context.Context, acc InboxAccess) (InboxStatus, error) {
	var st InboxStatus
	var err error
	if st.Enabled, err = s.HasMsgChannel(ctx); err != nil || !st.Enabled {
		return st, err
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM conversations c LEFT JOIN conversation_reads r ON r.conversation_id = c.id AND r.user_id = $1
		WHERE c.assignee_user_id = $1 AND c.status IN ('open', 'pending', 'snoozed')
		  AND EXISTS (SELECT 1 FROM conversation_messages x WHERE x.conversation_id = c.id AND x.direction = 'in'
			AND x.kind = 'message' AND x.id > coalesce(r.last_read_message_id, 0))`, acc.UserID).Scan(&st.Unread)
	if err != nil || !acc.All {
		return st, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversations
		WHERE assignee_user_id IS NULL AND status IN ('open', 'pending')`).Scan(&st.Unassigned)
	return st, err
}

// InboxStats are the counts a supervisor watches; they carry no content.
type InboxStats struct {
	Open       int `json:"open"`
	Pending    int `json:"pending"`
	Snoozed    int `json:"snoozed"`
	Unassigned int `json:"unassigned"`
	// ResolvedToday counts conversations resolved since midnight UTC.
	ResolvedToday int `json:"resolvedToday"`
	// OldestUnassignedSeconds is how long the longest-waiting conversation
	// in the pool has waited since its last customer text; 0 when none waits.
	OldestUnassignedSeconds int64 `json:"oldestUnassignedSeconds"`
}

// GetInboxStats returns the inbox counts.
func (s *Store) GetInboxStats(ctx context.Context) (InboxStats, error) {
	var st InboxStats
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status = 'open'),
		       count(*) FILTER (WHERE status = 'pending'),
		       count(*) FILTER (WHERE status = 'snoozed'),
		       count(*) FILTER (WHERE assignee_user_id IS NULL AND status IN ('open', 'pending')),
		       count(*) FILTER (WHERE status = 'resolved' AND resolved_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
		       coalesce(extract(epoch FROM now() - min(coalesce(last_inbound_at, created_at))
		           FILTER (WHERE assignee_user_id IS NULL AND status IN ('open', 'pending')))::bigint, 0)
		FROM conversations`).Scan(&st.Open, &st.Pending, &st.Snoozed, &st.Unassigned, &st.ResolvedToday, &st.OldestUnassignedSeconds)
	return st, err
}

// TrunkText is a text a SIP trunk delivered to one of our numbers.
type TrunkText struct {
	TrunkID int64
	// ToKey and FromKey are the lookup keys of the dialled number and the
	// sender (the same reduction the address books use).
	ToKey string
	// ToSuffix is the last nine digits of ToKey (empty for a shorter
	// number): an address typed with a + still matches a carrier that dials
	// it without.
	ToSuffix string
	FromKey  string
	// FromName is the display name the carrier sent, if any.
	FromName string
	Body     string
	// EventKey is unique per delivery (the idempotency key): the same text
	// delivered again, to this or another control pod, is ingested once.
	EventKey string
	// Skip only reports whether the number is a channel address, storing
	// nothing (a text that is not plain text cannot be a message).
	Skip bool
}

// Reply is a text Hello sends back on its own (the opt-out confirmation, the
// help text).
type Reply struct {
	TrunkID int64
	From    string
	To      string
	Body    string
}

// IngestResult is what an ingest did.
type IngestResult struct {
	// Handled is false when the dialled number is no channel address: the
	// caller carries on with its own delivery, unchanged.
	Handled   bool
	Duplicate bool
	// ConversationID and MessageID name what was stored.
	ConversationID int64
	MessageID      int64
	// Keyword is optout or optin when the text was an opt-out keyword.
	Keyword string
	Replies []Reply
}

// IngestTrunkText stores a text from a SIP trunk as a message of a
// conversation. It is idempotent and safe to run on any number of pods at
// once: the event key goes into a ledger first (a key seen before ends the
// ingest with nothing changed), the one open conversation per number and peer
// is made under a unique index, and the message is unique by its key. The
// whole ingest is one transaction: it either all commits or the caller sees
// the error and the carrier's retry delivers it again.
//
// A text that is a whole opt-out keyword records the opt-out, whatever the
// carrier does; START records the opt-in and HELP asks for the help text.
func (s *Store) IngestTrunkText(ctx context.Context, in TrunkText) (IngestResult, error) {
	var res IngestResult
	var ev *messaging.Event
	for attempt := 0; ; attempt++ {
		res, ev = IngestResult{}, nil
		err := s.tx(ctx, func(tx *sql.Tx) error {
			res, ev = IngestResult{}, nil
			return s.ingestTrunkText(ctx, tx, in, &res, &ev)
		})
		// Two pods may race to reopen or open the same conversation; the
		// unique index lets one win and the other tries again.
		if errors.Is(err, ErrConflict) && attempt < 2 {
			continue
		}
		if err == nil && ev != nil {
			s.emit(ctx, *ev)
		}
		return res, err
	}
}

func (s *Store) ingestTrunkText(ctx context.Context, tx *sql.Tx, in TrunkText, res *IngestResult, ev **messaging.Event) error {
	var (
		channelID, addressID  int64
		address, kind, label  string
		helpText, optOutText  string
		keywordsJSON          string
		reopenDays            int
		conv                  int64
		convStatus            string
		convAssignee          sql.NullInt64
		newConversation       bool
		peerName, peerNameSrc string
	)
	err := tx.QueryRowContext(ctx, `
		SELECT ch.id, ch.kind, a.id, a.address, a.label, to_json(a.optout_keywords)::text, a.help_text, a.optout_text
		FROM msg_channels ch JOIN msg_addresses a ON a.channel_id = ch.id
		WHERE ch.provider = 'sip_trunk' AND ch.trunk_id = $1 AND ch.enabled AND a.enabled
		  AND (a.address_key = $2 OR ($3 <> '' AND a.address_suffix = $3))
		ORDER BY (a.address_key = $2) DESC, a.id LIMIT 1`,
		in.TrunkID, in.ToKey, in.ToSuffix).Scan(&channelID, &kind, &addressID, &address, &label, &keywordsJSON, &helpText, &optOutText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not a channel address
	}
	if err != nil {
		return err
	}
	res.Handled = true
	if in.Skip {
		return nil
	}
	var words []string
	if err := json.Unmarshal([]byte(keywordsJSON), &words); err != nil {
		return err
	}
	// The ledger: a key seen before ends the ingest here, whichever pod or
	// retry brought it.
	r, err := tx.ExecContext(ctx, `INSERT INTO messaging_events (channel_id, event_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		channelID, in.EventKey)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		res.Duplicate = true
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT reopen_days FROM messaging_settings`).Scan(&reopenDays); err != nil {
		return err
	}
	if in.FromName != "" {
		peerName, peerNameSrc = in.FromName, "provider"
	}
	// The open conversation of this peer on this number, if any.
	err = tx.QueryRowContext(ctx, `
		SELECT id, status, assignee_user_id FROM conversations
		WHERE address_id = $1 AND peer_key = $2 AND status NOT IN ('resolved', 'spam') FOR UPDATE`,
		addressID, in.FromKey).Scan(&conv, &convStatus, &convAssignee)
	if errors.Is(err, sql.ErrNoRows) {
		// A resolved one within reopen_days is reopened; otherwise a new one starts.
		err = tx.QueryRowContext(ctx, `
			SELECT id, status, assignee_user_id FROM conversations
			WHERE address_id = $1 AND peer_key = $2 AND status = 'resolved'
			  AND last_message_at > now() - make_interval(days => $3)
			ORDER BY last_message_at DESC, id DESC LIMIT 1 FOR UPDATE`,
			addressID, in.FromKey, reopenDays).Scan(&conv, &convStatus, &convAssignee)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE conversations SET status = 'open', resolved_at = NULL WHERE id = $1`, conv); err != nil {
				return err
			}
			convStatus = messaging.StatusOpen
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO conversations (channel_id, address_id, peer_key, peer_name, peer_name_source)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (address_id, peer_key) WHERE status NOT IN ('resolved', 'spam') DO NOTHING
			RETURNING id, status`, channelID, addressID, in.FromKey, peerName, peerNameSrc).Scan(&conv, &convStatus)
		if errors.Is(err, sql.ErrNoRows) {
			// Another pod made it a moment ago: attach to it.
			err = tx.QueryRowContext(ctx, `
				SELECT id, status, assignee_user_id FROM conversations
				WHERE address_id = $1 AND peer_key = $2 AND status NOT IN ('resolved', 'spam') FOR UPDATE`,
				addressID, in.FromKey).Scan(&conv, &convStatus, &convAssignee)
		} else if err == nil {
			newConversation = true
		}
	}
	if err != nil {
		return err
	}
	if peerName != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET peer_name = $2, peer_name_source = $3 WHERE id = $1 AND peer_name = ''`,
			conv, peerName, peerNameSrc); err != nil {
			return err
		}
	}
	var msgID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, body, status, provider_message_id)
		VALUES ($1, $2, 'in', 'message', 'customer', $3, 'delivered', $4)
		ON CONFLICT (channel_id, provider_message_id) DO NOTHING RETURNING id`,
		conv, channelID, in.Body, in.EventKey).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		res.Duplicate = true
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET last_message_id = $2, last_message_at = now(), last_inbound_at = now(),
			status = CASE WHEN status IN ('pending', 'snoozed') THEN 'open' ELSE status END,
			snoozed_until = NULL
		WHERE id = $1`, conv, msgID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE msg_channels SET last_event_at = now() WHERE id = $1`, channelID); err != nil {
		return err
	}
	res.ConversationID, res.MessageID = conv, msgID
	row := convRow{ID: conv, ChannelID: channelID, AddressID: addressID, PeerKey: in.FromKey}

	switch messaging.ClassifyKeyword(in.Body, words) {
	case messaging.KeywordOptOut:
		h, err := s.peerHash(in.FromKey)
		if err != nil {
			return err
		}
		var was string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM sms_opt_outs WHERE address_id = $1 AND peer_hash = $2`,
			addressID, h).Scan(&was); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sms_opt_outs (address_id, peer_key, peer_hash, state, source) VALUES ($1, $2, $3, 'out', 'keyword')
			ON CONFLICT (address_id, peer_hash) DO UPDATE SET state = 'out', source = 'keyword', note = '', at = now(),
				peer_key = $2`, addressID, in.FromKey, h); err != nil {
			return err
		}
		if _, err := addLine(ctx, tx, row, messaging.KindSystem, messaging.SenderSystem, 0, "", "The customer opted out"); err != nil {
			return err
		}
		// The confirmation goes out once, not for every repeated STOP.
		if was != "out" {
			text := optOutText
			if text == "" {
				text = messaging.DefaultOptOutText
			}
			res.Replies = append(res.Replies, Reply{TrunkID: in.TrunkID, From: address, To: in.FromKey, Body: text})
		}
		res.Keyword = "optout"
		*ev = &messaging.Event{Type: "message.optout", ConversationID: conv, MessageID: msgID, ChannelKind: kind, Direction: messaging.DirIn}
	case messaging.KeywordOptIn:
		res.Keyword = "optin"
		h, err := s.peerHash(in.FromKey)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sms_opt_outs (address_id, peer_key, peer_hash, state, source) VALUES ($1, $2, $3, 'in', 'customer')
			ON CONFLICT (address_id, peer_hash) DO UPDATE SET state = 'in', source = 'customer', note = '', at = now(),
				peer_key = $2`, addressID, in.FromKey, h); err != nil {
			return err
		}
		if _, err := addLine(ctx, tx, row, messaging.KindSystem, messaging.SenderSystem, 0, "", "The customer opted back in"); err != nil {
			return err
		}
	case messaging.KeywordHelp:
		if helpText != "" {
			res.Replies = append(res.Replies, Reply{TrunkID: in.TrunkID, From: address, To: in.FromKey, Body: helpText})
		}
	}
	if *ev == nil {
		*ev = &messaging.Event{Type: "message.received", ConversationID: conv, MessageID: msgID, ChannelKind: kind, Direction: messaging.DirIn}
	}
	if newConversation {
		if err := emitConversationEvent(ctx, tx, conv, msgID, "conversation.new", nil); err != nil {
			return err
		}
	}
	return emitConversationEvent(ctx, tx, conv, msgID, "message.in", nil)
}

// InboxEvent is one entry of the live-update log: ids only.
type InboxEvent struct {
	ID             int64     `json:"id"`
	Type           string    `json:"type"`
	ConversationID int64     `json:"conversationId"`
	MessageID      int64     `json:"messageId"`
	At             time.Time `json:"at"`
}

// ListInboxEvents returns the user's events after id `after`, oldest first.
func (s *Store) ListInboxEvents(ctx context.Context, userID, after int64, limit int) ([]InboxEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, conversation_id, coalesce(message_id, 0), at FROM conversation_events
		WHERE user_id = $1 AND id > $2 ORDER BY id LIMIT $3`, userID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []InboxEvent
	for rows.Next() {
		var e InboxEvent
		if err := rows.Scan(&e.ID, &e.Type, &e.ConversationID, &e.MessageID, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestInboxEventID is the id a new stream starts after, so it replays
// nothing.
func (s *Store) LatestInboxEventID(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM conversation_events WHERE user_id = $1`, userID).Scan(&id)
	return id, err
}

// TouchInboxPresence records that the user's inbox stream is open, for the
// email fallback.
func (s *Store) TouchInboxPresence(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO inbox_presence (user_id, seen_at) VALUES ($1, now())
		ON CONFLICT (user_id) DO UPDATE SET seen_at = now()`, userID)
	return err
}

// InboxNotice is one email to send: an agent who has had no inbox open for
// two minutes has a customer text waiting. It names the channel and the
// conversation, never the text or the number.
type InboxNotice struct {
	To             string
	ChannelName    string
	ConversationID int64
	Unassigned     bool
}

// ClaimInboxNotices takes the inbox events older than two minutes that were
// not looked at yet and returns the emails to send: one per user and
// conversation per 15 minutes, only to a user with an address who has no
// stream open, and only while the setting is on. Every event it looked at is
// marked, so each is looked at once whichever replica runs it.
func (s *Store) ClaimInboxNotices(ctx context.Context, limit int) ([]InboxNotice, error) {
	var out []InboxNotice
	err := s.tx(ctx, func(tx *sql.Tx) error {
		out = nil
		set, err := getMessagingSettings(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT e.id, e.user_id, e.conversation_id, e.at > now() - interval '1 hour', u.email, ch.name,
			       (SELECT assignee_user_id IS NULL FROM conversations WHERE id = e.conversation_id),
			       coalesce((SELECT p.seen_at < now() - interval '2 minutes' FROM inbox_presence p WHERE p.user_id = e.user_id), TRUE)
			FROM conversation_events e
			JOIN users u ON u.id = e.user_id
			JOIN conversations c ON c.id = e.conversation_id
			JOIN msg_channels ch ON ch.id = c.channel_id
			WHERE NOT e.notified AND e.type IN ('message.in', 'conversation.new') AND e.at < now() - interval '2 minutes'
			ORDER BY e.id LIMIT $1 FOR UPDATE OF e SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		type cand struct {
			id, user, conv int64
			recent         bool
			email, channel string
			unassigned     bool
			away           bool
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.id, &c.user, &c.conv, &c.recent, &c.email, &c.channel, &c.unassigned, &c.away); err != nil {
				_ = rows.Close()
				return err
			}
			cands = append(cands, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range cands {
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_events SET notified = TRUE WHERE id = $1`, c.id); err != nil {
				return err
			}
			if !set.EmailNotify || !c.recent || !c.away || c.email == "" {
				continue
			}
			var sent int
			err := tx.QueryRowContext(ctx, `
				INSERT INTO conversation_notify (user_id, conversation_id, last_email_at) VALUES ($1, $2, now())
				ON CONFLICT (user_id, conversation_id) DO UPDATE SET last_email_at = now()
				WHERE conversation_notify.last_email_at < now() - interval '15 minutes'
				RETURNING 1`, c.user, c.conv).Scan(&sent)
			if errors.Is(err, sql.ErrNoRows) {
				continue // throttled
			}
			if err != nil {
				return err
			}
			out = append(out, InboxNotice{To: c.email, ChannelName: c.channel, ConversationID: c.conv, Unassigned: c.unassigned})
		}
		return nil
	})
	return out, err
}

// syncOneConversationMessage reflects the delivery result of a SIP text
// queue row on the inbox message it carries.
func (s *Store) syncOneConversationMessage(ctx context.Context, queueID int64) error {
	var cm sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT conversation_message_id FROM messages WHERE id = $1`, queueID).Scan(&cm); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if !cm.Valid {
		return nil
	}
	return s.syncConversationMessages(ctx, []int64{cm.Int64})
}

// syncStuckConversationMessages settles every inbox message still sending
// whose queue rows have all finished, and marks one whose queue rows are gone
// unknown ("may have been delivered") after five minutes.
func (s *Store) syncStuckConversationMessages(ctx context.Context) error {
	if err := s.syncConversationMessages(ctx, nil); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_messages cm SET status = 'unknown'
		WHERE cm.status IN ('queued', 'sending') AND cm.direction = 'out' AND cm.kind = 'message'
		  AND cm.created_at < now() - interval '5 minutes'
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_message_id = cm.id)`)
	return err
}

// syncConversationMessages updates the inbox messages ids names (every one
// still sending when ids is nil) from their queue rows: failed when any part
// failed, sent once every part was accepted by the trunk, still sending
// otherwise.
func (s *Store) syncConversationMessages(ctx context.Context, ids []int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			WITH r AS (
				SELECT m.conversation_message_id AS id,
				       CASE WHEN bool_or(m.status = 'failed') THEN 'failed'
				            WHEN bool_and(m.status = 'delivered') THEN 'sent'
				            ELSE 'sending' END AS st,
				       coalesce(left(max(m.detail) FILTER (WHERE m.status = 'failed'), 200), '') AS err
				FROM messages m
				WHERE m.conversation_message_id IN (
					SELECT id FROM conversation_messages WHERE status IN ('queued', 'sending') AND ($1::bigint[] IS NULL OR id = ANY($1::bigint[])))
				GROUP BY m.conversation_message_id)
			UPDATE conversation_messages cm SET status = r.st, error_code = r.err,
				sent_at = CASE WHEN r.st = 'sent' THEN now() ELSE cm.sent_at END
			FROM r WHERE cm.id = r.id AND r.st <> 'sending'
			RETURNING cm.id, cm.conversation_id`, ids)
		if err != nil {
			return err
		}
		type done struct{ msg, conv int64 }
		var settled []done
		for rows.Next() {
			var d done
			if err := rows.Scan(&d.msg, &d.conv); err != nil {
				_ = rows.Close()
				return err
			}
			settled = append(settled, d)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range settled {
			if err := emitConversationEvent(ctx, tx, d.conv, d.msg, "message.status", nil); err != nil {
				return err
			}
		}
		return nil
	})
}
