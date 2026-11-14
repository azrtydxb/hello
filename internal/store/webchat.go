package store

// The visitor side of the LAN chat widget and what the inbox does with it
// (spec messaging-channels M-29 to M-29c). A visitor is anonymous: a random
// token authenticates every call and only its SHA-256 is kept. A visitor's
// conversation is an ordinary inbox conversation (peer kind visitor), so
// agents use the inbox they already have; what a visitor sees is carried by
// chat_events, which any pod can read, so a stream resumes anywhere.
//
// Message text, names and email addresses never reach a log or an audit
// row; the audit rows carry ids.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
)

// Errors of the chat store; the chat listener and the API map them.
var (
	// ErrNotChat: the conversation is not a chat.
	ErrNotChat = errors.New("store: not a chat conversation")
	// ErrChatNoEmail: the visitor left no email address, or the chat was
	// live, so there is nobody to email.
	ErrChatNoEmail = errors.New("store: the visitor left no email address")
	// ErrChatLimit: the visitor sent too many messages without an answer.
	ErrChatLimit = errors.New("store: too many messages without a reply")
	// ErrChatFull: the site's live queue is full; the widget goes offline.
	ErrChatFull = errors.New("store: the chat queue is full")
)

// chatEventLock serialises the writers of chat_events, so the ids of
// committed events are in commit order.
const chatEventLock = 0x68656c6c6f636874 // "hellocht"

// chatPeerKey is the peer key of a visitor's conversations.
func chatPeerKey(visitorID int64) string { return "visitor:" + strconv.FormatInt(visitorID, 10) }

// visitorOfPeer is the SQL that turns a conversation c's peer key into the
// visitor's id (NULL for anything but a visitor).
const visitorOfPeer = `CASE WHEN c.peer_kind = 'visitor' THEN substr(c.peer_key, 9)::bigint END`

func chatTokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// ChatIPHash is what is kept of a visitor's address: HMAC-SHA256 under a
// salt that changes at 00:00 UTC, so it cannot follow anyone beyond the day.
func (s *Store) ChatIPHash(ip string, now time.Time) ([]byte, error) {
	if s.box == nil {
		return nil, ErrNoSecretBox
	}
	return s.box.MAC("chat-ip:"+now.UTC().Format("2006-01-02"), ip), nil
}

// ChatVisitor is an authenticated visitor.
type ChatVisitor struct {
	ID      int64
	SiteID  int64
	Name    string
	Email   string
	Consent bool
	// Blocked: an agent blocked this visitor (or the day's address hash).
	Blocked bool
}

// ChatAuth finds the visitor a token belongs to on a site; ErrNotFound for a
// token nobody holds. ipHash is the day's hash of the caller's address (nil
// when unknown) and only feeds the block check.
func (s *Store) ChatAuth(ctx context.Context, siteID int64, token string, ipHash []byte) (ChatVisitor, error) {
	var v ChatVisitor
	err := s.db.QueryRowContext(ctx, `
		SELECT v.id, v.site_id, v.name, v.email, v.consent_to_store,
			EXISTS (SELECT 1 FROM chat_blocks b WHERE b.site_id = v.site_id AND b.until > now()
				AND (b.visitor_id = v.id OR (b.ip_hash IS NOT NULL AND b.ip_hash = $3::bytea)))
		FROM chat_visitors v WHERE v.site_id = $1 AND v.token_hash = $2`,
		siteID, chatTokenHash(token), ipHash).Scan(&v.ID, &v.SiteID, &v.Name, &v.Email, &v.Consent, &v.Blocked)
	if err != nil {
		return v, mapErr(err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE chat_visitors SET last_seen = now() WHERE id = $1 AND last_seen < now() - interval '1 minute'`, v.ID)
	return v, nil
}

// ChatIPBlocked reports whether the day's address hash is blocked on a site.
func (s *Store) ChatIPBlocked(ctx context.Context, siteID int64, ipHash []byte) (bool, error) {
	if len(ipHash) == 0 {
		return false, nil
	}
	var b bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chat_blocks WHERE site_id = $1 AND until > now() AND ip_hash = $2::bytea)`,
		siteID, ipHash).Scan(&b)
	return b, err
}

// ChatAvailability says whether a live chat can be answered now: the site is
// inside its opening hours, a supervisor has the inbox open (it counts the
// last two minutes), and the unassigned live chats are under the site's cap.
// reason is "hours", "no_agent" or "full" when not.
func (s *Store) ChatAvailability(ctx context.Context, site ChatSite, now time.Time) (online bool, reason string, err error) {
	if !site.Hours.Open(now) {
		return false, "hours", nil
	}
	var agent bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM inbox_presence p JOIN users u ON u.id = p.user_id
			WHERE p.seen_at > now() - interval '2 minutes' AND u.enabled AND u.role IN ('operator', 'admin'))`).Scan(&agent); err != nil {
		return false, "", err
	}
	if !agent {
		return false, "no_agent", nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM conversations WHERE channel_id = $1 AND assignee_user_id IS NULL
			AND status IN ('open', 'pending') AND NOT offline`, site.ChannelID).Scan(&n); err != nil {
		return false, "", err
	}
	if n >= site.MaxOpenUnassigned {
		return false, "full", nil
	}
	return true, "", nil
}

// ChatSessionInput creates or resumes a visitor. Name and Email are already
// validated; empty means the visitor gave none.
type ChatSessionInput struct {
	Site     ChatSite
	Token    string
	IPHash   []byte
	UAFamily string
	Page     string
	Name     string
	Email    string
	Consent  bool
}

// ChatSessionResult is what a session call returns.
type ChatSessionResult struct {
	// Token is the visitor's token; it is only ever returned here, for a new
	// visitor.
	Token          string
	VisitorID      int64
	Created        bool
	ConversationID int64
	// Closed: the visitor is blocked. Nothing was stored.
	Closed bool
}

func newVisitorToken() (string, error) {
	b := make([]byte, 32) // 256 random bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ChatSession creates a visitor (a new token) or resumes the one the token
// names, recording what the visitor chose to tell.
func (s *Store) ChatSession(ctx context.Context, in ChatSessionInput) (ChatSessionResult, error) {
	var res ChatSessionResult
	var v ChatVisitor
	found := false
	if in.Token != "" {
		got, err := s.ChatAuth(ctx, in.Site.ID, in.Token, in.IPHash)
		switch {
		case err == nil:
			v, found = got, true
		case errors.Is(err, ErrNotFound):
		default:
			return res, err
		}
	}
	if found && v.Blocked {
		res.Closed = true
		return res, nil
	}
	if !found {
		blocked, err := s.ChatIPBlocked(ctx, in.Site.ID, in.IPHash)
		if err != nil {
			return res, err
		}
		if blocked {
			res.Closed = true
			return res, nil
		}
		tok, err := newVisitorToken()
		if err != nil {
			return res, err
		}
		if err := s.db.QueryRowContext(ctx, `
			INSERT INTO chat_visitors (site_id, token_hash, name, email, consent_to_store, ip_hash, ua_family, page)
			VALUES ($1, $2, $3, $4, $5, $6::bytea, $7, $8) RETURNING id`,
			in.Site.ID, chatTokenHash(tok), in.Name, in.Email, in.Consent && (in.Name != "" || in.Email != ""), in.IPHash,
			in.UAFamily, in.Page).Scan(&v.ID); err != nil {
			return res, mapErr(err)
		}
		res.Token, res.Created = tok, true
	} else if in.Name != "" || in.Email != "" {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE chat_visitors SET name = CASE WHEN $2::text <> '' THEN $2::text ELSE name END,
				email = CASE WHEN $3::text <> '' THEN $3::text ELSE email END,
				consent_to_store = consent_to_store OR $4::boolean, ip_hash = coalesce($5::bytea, ip_hash), last_seen = now()
			WHERE id = $1`, v.ID, in.Name, in.Email, in.Consent, in.IPHash); err != nil {
			return res, err
		}
	}
	res.VisitorID = v.ID
	var conv int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM conversations WHERE address_id = $1 AND peer_key = $2 AND status NOT IN ('resolved', 'spam')`,
		in.Site.AddressID, chatPeerKey(v.ID)).Scan(&conv)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}
	res.ConversationID = conv
	if conv != 0 && in.Name != "" {
		_, _ = s.db.ExecContext(ctx, `UPDATE conversations SET peer_name = $2, peer_name_source = 'visitor' WHERE id = $1 AND peer_name = ''`, conv, in.Name)
	}
	return res, nil
}

// ChatSendInput is one message a visitor sends. Text is already cleaned.
type ChatSendInput struct {
	Site        ChatSite
	VisitorID   int64
	Text        string
	ClientMsgID string
	// Offline: no agent is available; the conversation is flagged and needs
	// the visitor's email address.
	Offline bool
	// NoNew: the site hides itself while offline, so only a visitor who
	// already has a conversation may continue it.
	NoNew bool
}

// ChatSendResult is what a send did.
type ChatSendResult struct {
	MessageID      int64
	ConversationID int64
	// Duplicate: ClientMsgID matched an earlier send; nothing changed.
	Duplicate bool
	// Closed: the conversation was marked spam by an agent; the text is
	// dropped without a sign.
	Closed bool
}

// ChatSend stores a visitor's message in their conversation, opening or
// reopening it, in one transaction. A retried client id is idempotent.
func (s *Store) ChatSend(ctx context.Context, in ChatSendInput) (ChatSendResult, error) {
	var res ChatSendResult
	var ev *messaging.Event
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, ev = ChatSendResult{}, nil
		return s.chatSend(ctx, tx, in, &res, &ev)
	})
	if err == nil && ev != nil {
		s.emit(ctx, *ev)
	}
	return res, err
}

func (s *Store) chatSend(ctx context.Context, tx *sql.Tx, in ChatSendInput, res *ChatSendResult, ev **messaging.Event) error {
	var name, email string
	// Locking the visitor serialises one visitor's messages, so the single
	// open conversation and the caps are exact.
	if err := tx.QueryRowContext(ctx, `SELECT name, email FROM chat_visitors WHERE id = $1 AND site_id = $2 FOR UPDATE`,
		in.VisitorID, in.Site.ID).Scan(&name, &email); err != nil {
		return err
	}
	var on bool
	if err := tx.QueryRowContext(ctx, `SELECT c.enabled AND a.enabled FROM msg_channels c, msg_addresses a WHERE c.id = $1 AND a.id = $2`,
		in.Site.ChannelID, in.Site.AddressID).Scan(&on); err != nil {
		return err
	}
	if !on {
		return ErrChannelUnavailable
	}
	peer := chatPeerKey(in.VisitorID)
	var spam bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM conversations WHERE address_id = $1 AND peer_key = $2 AND status = 'spam')`,
		in.Site.AddressID, peer).Scan(&spam); err != nil {
		return err
	}
	if spam {
		res.Closed = true
		return nil
	}
	var (
		conv       int64
		newConv    bool
		reopenDays int
	)
	err := tx.QueryRowContext(ctx, `SELECT id FROM conversations WHERE address_id = $1 AND peer_key = $2 AND status NOT IN ('resolved', 'spam') FOR UPDATE`,
		in.Site.AddressID, peer).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `SELECT reopen_days FROM messaging_settings`).Scan(&reopenDays); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM conversations WHERE address_id = $1 AND peer_key = $2 AND status = 'resolved'
				AND last_message_at > now() - make_interval(days => $3) ORDER BY last_message_at DESC, id DESC LIMIT 1 FOR UPDATE`,
			in.Site.AddressID, peer, reopenDays).Scan(&conv)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE conversations SET status = 'open', resolved_at = NULL WHERE id = $1`, conv); err != nil {
				return err
			}
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		if in.NoNew {
			res.Closed = true
			return nil
		}
		if in.Offline && email == "" {
			return fieldError("email", "is required when no agent is available")
		}
		if !in.Offline {
			var n int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FROM conversations WHERE channel_id = $1 AND assignee_user_id IS NULL
					AND status IN ('open', 'pending') AND NOT offline`, in.Site.ChannelID).Scan(&n); err != nil {
				return err
			}
			if n >= in.Site.MaxOpenUnassigned {
				return ErrChatFull
			}
		}
		src := ""
		if name != "" {
			src = "visitor"
		}
		err = tx.QueryRowContext(ctx, `
			INSERT INTO conversations (channel_id, address_id, peer_key, peer_kind, peer_name, peer_name_source, offline)
			VALUES ($1, $2, $3, 'visitor', $4, $5, $6) RETURNING id`,
			in.Site.ChannelID, in.Site.AddressID, peer, name, src, in.Offline).Scan(&conv)
		newConv = true
	}
	if err != nil {
		return err
	}
	res.ConversationID = conv
	var unanswered int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM conversation_messages m WHERE m.conversation_id = $1 AND m.direction = 'in' AND m.kind = 'message'
			AND m.id > coalesce((SELECT max(o.id) FROM conversation_messages o
				WHERE o.conversation_id = $1 AND o.direction = 'out' AND o.kind = 'message'), 0)`, conv).Scan(&unanswered); err != nil {
		return err
	}
	if unanswered >= messaging.MaxChatUnanswered {
		return ErrChatLimit
	}
	sender := name
	if sender == "" {
		sender = "Visitor"
	}
	var msgID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, sender_name, body, status, client_msg_id)
		VALUES ($1, $2, 'in', 'message', 'customer', $3, $4, 'delivered', $5)
		ON CONFLICT (conversation_id, client_msg_id) DO NOTHING RETURNING id`,
		conv, in.Site.ChannelID, sender, in.Text, in.ClientMsgID).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		res.Duplicate = true
		return tx.QueryRowContext(ctx, `SELECT id FROM conversation_messages WHERE conversation_id = $1 AND client_msg_id = $2`,
			conv, in.ClientMsgID).Scan(&res.MessageID)
	}
	if err != nil {
		return err
	}
	res.MessageID = msgID
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET last_message_id = $2, last_message_at = now(), last_inbound_at = now(),
			status = CASE WHEN status IN ('pending', 'snoozed') THEN 'open' ELSE status END, snoozed_until = NULL
		WHERE id = $1`, conv, msgID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE msg_channels SET last_event_at = now() WHERE id = $1`, in.Site.ChannelID); err != nil {
		return err
	}
	if newConv {
		if err := emitConversationEvent(ctx, tx, conv, msgID, "conversation.new", nil); err != nil {
			return err
		}
	}
	if err := emitConversationEvent(ctx, tx, conv, msgID, "message.in", nil); err != nil {
		return err
	}
	*ev = &messaging.Event{Type: "message.received", ConversationID: conv, MessageID: msgID, ChannelKind: messaging.KindWebchat, Direction: messaging.DirIn}
	return nil
}

// ChatRetentionDays is how long a finished chat of the site is kept: its own
// setting, or the messaging settings' chat retention.
func (s *Store) ChatRetentionDays(ctx context.Context, site ChatSite) (int, error) {
	if site.RetentionDays != nil {
		return *site.RetentionDays, nil
	}
	set, err := s.GetMessagingSettings(ctx)
	return set.RetentionChatDays, err
}

// ChatLine is one line of a chat as the visitor sees it.
type ChatLine struct {
	ID int64 `json:"id"`
	// From is "visitor" or "agent"; an agent is never named.
	From string    `json:"from"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// ChatHistory returns the visitor's lines after id `after`, oldest first, the
// newest `limit` of them: only messages that were delivered or sent (a reply
// still being emailed, or one that failed, is not shown), never notes or the
// inbox's own system lines.
func (s *Store) ChatHistory(ctx context.Context, visitorID, after int64, limit int) ([]ChatLine, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.direction, m.body, m.created_at FROM conversation_messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE c.peer_kind = 'visitor' AND c.peer_key = $1 AND m.kind = 'message' AND m.status IN ('sent', 'delivered', 'read') AND m.id > $2
		ORDER BY m.id DESC LIMIT $3`, chatPeerKey(visitorID), after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ChatLine
	for rows.Next() {
		var (
			l   ChatLine
			dir string
		)
		if err := rows.Scan(&l.ID, &dir, &l.Text, &l.At); err != nil {
			return nil, err
		}
		l.From = "agent"
		if dir == messaging.DirIn {
			l.From = "visitor"
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if out == nil {
		out = []ChatLine{}
	}
	return out, nil
}

// ChatEvent is one entry of a visitor's stream.
type ChatEvent struct {
	ID   int64
	Type string // message, read, typing, closed
	// Line is set for a message.
	Line *ChatLine
}

// ChatEvents returns the visitor's events after id `after`, oldest first.
func (s *Store) ChatEvents(ctx context.Context, visitorID, after int64, limit int) ([]ChatEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.type, coalesce(m.id, 0), coalesce(m.direction, ''), coalesce(m.body, ''), coalesce(m.created_at, e.at)
		FROM chat_events e LEFT JOIN conversation_messages m ON m.id = e.message_id AND m.kind = 'message'
		WHERE e.visitor_id = $1 AND e.id > $2 ORDER BY e.id LIMIT $3`, visitorID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ChatEvent{}
	for rows.Next() {
		var (
			e   ChatEvent
			l   ChatLine
			dir string
		)
		if err := rows.Scan(&e.ID, &e.Type, &l.ID, &dir, &l.Text, &l.At); err != nil {
			return nil, err
		}
		if e.Type == "message" {
			if l.ID == 0 {
				continue // the message was erased
			}
			l.From = "agent"
			if dir == messaging.DirIn {
				l.From = "visitor"
			}
			e.Line = &l
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ChatLatestEventID is the newest event id of a visitor (0 for none).
func (s *Store) ChatLatestEventID(ctx context.Context, visitorID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM chat_events WHERE visitor_id = $1`, visitorID).Scan(&id)
	return id, err
}

// chatEvent tells the visitor of conversation convID something happened.
// It does nothing for a conversation that is not a chat.
func chatEvent(ctx context.Context, tx *sql.Tx, convID, msgID int64, typ string) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(chatEventLock)); err != nil {
		return err
	}
	var msg any
	if msgID > 0 {
		msg = msgID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO chat_events (visitor_id, type, message_id)
		SELECT v.id, $2, $3::bigint FROM conversations c
		JOIN chat_visitors v ON v.id = `+visitorOfPeer+`
		WHERE c.id = $1`, convID, typ, msg)
	return err
}

// openChatConversation returns the id of the visitor's open conversation, 0
// for none.
func openChatConversation(ctx context.Context, tx *sql.Tx, siteAddress, visitorID int64) (int64, error) {
	var conv int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM conversations WHERE address_id = $1 AND peer_key = $2 AND status NOT IN ('resolved', 'spam') FOR UPDATE`,
		siteAddress, chatPeerKey(visitorID)).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return conv, err
}

// ChatTyping tells the agents the visitor is typing. It is nothing when
// there is no open conversation.
func (s *Store) ChatTyping(ctx context.Context, site ChatSite, visitorID int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		conv, err := openChatConversation(ctx, tx, site.AddressID, visitorID)
		if err != nil || conv == 0 {
			return err
		}
		return emitConversationEvent(ctx, tx, conv, 0, "typing", nil)
	})
}

// ChatEnd closes the visitor's open conversation: it is resolved with a line
// the agents see, and the visitor's other tabs are told.
func (s *Store) ChatEnd(ctx context.Context, site ChatSite, visitorID int64) error {
	var ev *messaging.Event
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ev = nil
		conv, err := openChatConversation(ctx, tx, site.AddressID, visitorID)
		if err != nil || conv == 0 {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET status = 'resolved', resolved_at = now(), snoozed_until = NULL WHERE id = $1`, conv); err != nil {
			return err
		}
		row := convRow{ID: conv, ChannelID: site.ChannelID, AddressID: site.AddressID, PeerKey: chatPeerKey(visitorID)}
		if _, err := addLine(ctx, tx, row, messaging.KindSystem, messaging.SenderSystem, 0, "", "The visitor ended the chat"); err != nil {
			return err
		}
		if err := emitConversationEvent(ctx, tx, conv, 0, "conversation.updated", nil); err != nil {
			return err
		}
		if err := chatEvent(ctx, tx, conv, 0, "closed"); err != nil {
			return err
		}
		ev = &messaging.Event{Type: "conversation.resolved", ConversationID: conv, ChannelKind: messaging.KindWebchat, Status: messaging.StatusResolved}
		return nil
	})
	if err == nil && ev != nil {
		s.emit(ctx, *ev)
	}
	return err
}

// ChatTranscript returns the address the visitor gave and everything of
// their chats the visitor can see.
func (s *Store) ChatTranscript(ctx context.Context, visitorID int64) (email string, lines []ChatLine, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT email FROM chat_visitors WHERE id = $1`, visitorID).Scan(&email); err != nil {
		return "", nil, mapErr(err)
	}
	lines, err = s.ChatHistory(ctx, visitorID, 0, 500)
	return email, lines, err
}

// ChatInfo is what an agent sees of a chat visitor.
type ChatInfo struct {
	SiteID int64 `json:"siteId"`
	// Offline: the chat started while no agent was available.
	Offline bool `json:"offline"`
	// Email is the address the visitor gave, "" if none.
	Email   string `json:"email"`
	Page    string `json:"page"`
	Browser string `json:"browser"`
	Blocked bool   `json:"blocked"`
}

// chatInfo loads the chat facts of a conversation; nil when it is not a chat.
func (s *Store) chatInfo(ctx context.Context, convID int64) (*ChatInfo, error) {
	var ci ChatInfo
	err := s.db.QueryRowContext(ctx, `
		SELECT cs.id, c.offline, coalesce(v.email, ''), coalesce(v.page, ''), coalesce(v.ua_family, ''),
			EXISTS (SELECT 1 FROM chat_blocks b WHERE b.visitor_id = v.id AND b.until > now())
		FROM conversations c JOIN chat_sites cs ON cs.channel_id = c.channel_id
		LEFT JOIN chat_visitors v ON v.id = `+visitorOfPeer+`
		WHERE c.id = $1`, convID).Scan(&ci.SiteID, &ci.Offline, &ci.Email, &ci.Page, &ci.Browser, &ci.Blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// insertAgentReply writes an agent's reply into a conversation and does what
// a reply does: claims an unassigned conversation, reopens a resolved or
// snoozed one and stamps the first response. status is the message's state.
func insertAgentReply(ctx context.Context, tx *sql.Tx, c convRow, acc InboxAccess, body, clientMsgID, status string) (int64, error) {
	var clientID any
	if clientMsgID != "" {
		clientID = clientMsgID
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, sender_user_id,
			sender_name, body, status, client_msg_id, sent_at)
		VALUES ($1, $2, 'out', 'message', 'user', $3, $4, $5, $6::text, $7, CASE WHEN $6::text = 'sent' THEN now() END) RETURNING id`,
		c.ID, c.ChannelID, acc.UserID, acc.Name, body, status, clientID).Scan(&id); err != nil {
		return 0, err
	}
	claimed := !c.Assignee.Valid
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET last_message_id = $2, last_message_at = now(),
			assignee_user_id = coalesce(assignee_user_id, $3),
			first_response_at = coalesce(first_response_at, now()),
			status = CASE WHEN status IN ('resolved', 'snoozed') THEN 'open' ELSE status END,
			resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END,
			snoozed_until = NULL
		WHERE id = $1`, c.ID, id, acc.UserID); err != nil {
		return 0, err
	}
	if claimed {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, to_user_id, reason) VALUES ($1, $2, 'reply')`,
			c.ID, acc.UserID); err != nil {
			return 0, err
		}
	}
	if err := emitConversationEvent(ctx, tx, c.ID, id, "message.sent", []int64{acc.UserID}); err != nil {
		return 0, err
	}
	return id, nil
}

// chatAgentReply sends an agent's reply into a live chat: there is no
// carrier, so the message is stored sent and the visitor's stream carries it.
func (s *Store) chatAgentReply(ctx context.Context, tx *sql.Tx, c convRow, acc InboxAccess, in SendInput, on bool) (ConvMessage, error) {
	if !on {
		return ConvMessage{}, ErrChannelUnavailable
	}
	id, err := insertAgentReply(ctx, tx, c, acc, in.Body, in.ClientMsgID, messaging.DeliverySent)
	if err != nil {
		return ConvMessage{}, err
	}
	if err := chatEvent(ctx, tx, c.ID, id, "message"); err != nil {
		return ConvMessage{}, err
	}
	return scanConvMessage(tx.QueryRowContext(ctx, `SELECT `+convMessageCols+` FROM conversation_messages WHERE id = $1`, id))
}

// ChatAgentTyping tells the visitor an agent is typing (at most one event
// every three seconds per visitor). A conversation that is not a chat is
// ErrNotChat.
func (s *Store) ChatAgentTyping(ctx context.Context, acc InboxAccess, convID int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		var chat bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chat_sites WHERE channel_id = $1)`, c.ChannelID).Scan(&chat); err != nil {
			return err
		}
		if !chat {
			return ErrNotChat
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(chatEventLock)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO chat_events (visitor_id, type)
			SELECT v.id, 'typing' FROM conversations c JOIN chat_visitors v ON v.id = `+visitorOfPeer+`
			WHERE c.id = $1 AND NOT EXISTS (SELECT 1 FROM chat_events e WHERE e.visitor_id = v.id AND e.type = 'typing'
				AND e.at > now() - interval '3 seconds')`, c.ID)
		return err
	})
}

// BlockChatVisitor blocks the visitor of a chat for ttl: by visitor, and for
// the rest of the UTC day by the hash of their address (which stops matching
// when the salt changes). The conversation is resolved. The audit row has ids
// only.
func (s *Store) BlockChatVisitor(ctx context.Context, acc InboxAccess, convID int64, ttl time.Duration) (ChatBlock, error) {
	var out ChatBlock
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		var (
			siteID, visitorID int64
			ipHash            []byte
			seen              time.Time
		)
		err = tx.QueryRowContext(ctx, `
			SELECT cs.id, v.id, v.ip_hash, v.last_seen FROM conversations c
			JOIN chat_sites cs ON cs.channel_id = c.channel_id
			JOIN chat_visitors v ON v.id = `+visitorOfPeer+`
			WHERE c.id = $1`, c.ID).Scan(&siteID, &visitorID, &ipHash, &seen)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotChat
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		until := now.Add(ttl)
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO chat_blocks (site_id, visitor_id, until, by_user_id) VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
			siteID, visitorID, until, acc.UserID).Scan(&out.ID, &out.CreatedAt); err != nil {
			return err
		}
		out.SiteID, out.VisitorID, out.Until = siteID, &visitorID, until
		day := now.Truncate(24 * time.Hour)
		if len(ipHash) > 0 && seen.After(day) {
			ipUntil := day.Add(24 * time.Hour)
			if until.Before(ipUntil) {
				ipUntil = until
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO chat_blocks (site_id, ip_hash, until, by_user_id) VALUES ($1, $2, $3, $4)`,
				siteID, ipHash, ipUntil, acc.UserID); err != nil {
				return err
			}
			out.ByIP = true
		}
		if c.Status != messaging.StatusResolved {
			if _, err := tx.ExecContext(ctx, `UPDATE conversations SET status = 'resolved', resolved_at = now(), snoozed_until = NULL WHERE id = $1`, c.ID); err != nil {
				return err
			}
			if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", acc.Name+" blocked the visitor"); err != nil {
				return err
			}
			if err := emitConversationEvent(ctx, tx, c.ID, 0, "conversation.updated", []int64{acc.UserID}); err != nil {
				return err
			}
			if err := chatEvent(ctx, tx, c.ID, 0, "closed"); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, acc.Actor, "block", "chat_visitor", strconv.FormatInt(visitorID, 10))
	})
	return out, err
}

// ChatEmailReply is an agent's answer to an offline chat, ready to email.
type ChatEmailReply struct {
	Message ConvMessage
	// To is the address the visitor gave; SiteName the channel's name.
	To       string
	SiteName string
}

// PrepareChatEmailReply records an agent's reply to an offline chat (state
// sending) and returns the address to email it to. The visitor sees it in the
// widget once FinishChatEmailReply marks it sent. A retried client id returns
// the earlier message with created false and sends nothing.
func (s *Store) PrepareChatEmailReply(ctx context.Context, acc InboxAccess, in SendInput) (rep ChatEmailReply, created bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, in.ConversationID)
		if err != nil {
			return err
		}
		var (
			offline  bool
			email    string
			siteName string
			chEnable bool
		)
		err = tx.QueryRowContext(ctx, `
			SELECT cv.offline, coalesce(v.email, ''), ch.name, ch.enabled
			FROM conversations cv JOIN chat_sites cs ON cs.channel_id = cv.channel_id
			JOIN msg_channels ch ON ch.id = cv.channel_id
			LEFT JOIN chat_visitors v ON v.id = CASE WHEN cv.peer_kind = 'visitor' THEN substr(cv.peer_key, 9)::bigint END
			WHERE cv.id = $1`, c.ID).Scan(&offline, &email, &siteName, &chEnable)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotChat
		}
		if err != nil {
			return err
		}
		if in.ClientMsgID != "" {
			m, err := scanConvMessage(tx.QueryRowContext(ctx, `SELECT `+convMessageCols+`
				FROM conversation_messages WHERE conversation_id = $1 AND client_msg_id = $2`, c.ID, in.ClientMsgID))
			if err == nil {
				rep = ChatEmailReply{Message: m, To: email, SiteName: siteName}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if c.Status == messaging.StatusSpam {
			return ErrConversationState
		}
		if !chEnable {
			return ErrChannelUnavailable
		}
		if !offline || email == "" {
			return ErrChatNoEmail
		}
		id, err := insertAgentReply(ctx, tx, c, acc, in.Body, in.ClientMsgID, messaging.DeliverySending)
		if err != nil {
			return err
		}
		m, err := scanConvMessage(tx.QueryRowContext(ctx, `SELECT `+convMessageCols+` FROM conversation_messages WHERE id = $1`, id))
		if err != nil {
			return err
		}
		rep = ChatEmailReply{Message: m, To: email, SiteName: siteName}
		created = true
		return nil
	})
	return rep, created, err
}

// FinishChatEmailReply records whether the email went out: sent (and the
// visitor's widget shows the reply) or failed.
func (s *Store) FinishChatEmailReply(ctx context.Context, msgID int64, sent bool) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var conv int64
		status, code := messaging.DeliverySent, ""
		if !sent {
			status, code = messaging.DeliveryFailed, "email_failed"
		}
		err := tx.QueryRowContext(ctx, `
			UPDATE conversation_messages SET status = $2::text, error_code = $3::text, sent_at = CASE WHEN $2::text = 'sent' THEN now() ELSE sent_at END
			WHERE id = $1 AND status = 'sending' RETURNING conversation_id`, msgID, status, code).Scan(&conv)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := emitConversationEvent(ctx, tx, conv, msgID, "message.status", nil); err != nil {
			return err
		}
		if sent {
			return chatEvent(ctx, tx, conv, msgID, "message")
		}
		return nil
	})
}

// maintainChat removes what the chat keeps only for a while: old stream
// events, lapsed blocks and visitors with no conversation left.
func (s *Store) maintainChat(ctx context.Context) (int64, error) {
	var n int64
	for _, q := range []string{
		`DELETE FROM chat_events WHERE at < now() - interval '7 days'`,
		`DELETE FROM chat_blocks WHERE until < now() - interval '1 day'`,
		`DELETE FROM chat_visitors v WHERE v.last_seen < now() - interval '30 days'
			AND NOT EXISTS (SELECT 1 FROM conversations c WHERE c.peer_kind = 'visitor' AND c.peer_key = 'visitor:' || v.id::text)`,
	} {
		res, err := s.db.ExecContext(ctx, q)
		if err != nil {
			return n, fmt.Errorf("chat maintenance: %w", err)
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}
