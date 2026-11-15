package store

// Privacy tooling of the messaging inbox (spec messaging-channels M-20,
// M-21): the provider-side deletion queue, the redaction of one message, the
// export of everything Hello holds of one customer (a data subject access
// request) and the supervisor-view audit report.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/media"
)

// newRedactionBatch names one erase or redaction: what for, and random.
func newRedactionBatch(what string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return what + "-" + hex.EncodeToString(b)
}

// queueRedactions queues the carrier messages q selects (channel id,
// provider, provider message id; args bind q's parameters) for deletion
// under batch. A message queued before and not yet done joins this batch
// and starts its tries again; one already done stays done.
func queueRedactions(ctx context.Context, tx *sql.Tx, batch, q string, args ...any) error {
	//nolint:gosec // G202: q is a fixed query of this package with bound parameters.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO msg_provider_redactions (channel_id, provider, provider_message_id, batch)
		SELECT x.channel_id, x.provider, x.provider_message_id, $`+strconv.Itoa(len(args)+1)+` FROM (`+q+`) AS x(channel_id, provider, provider_message_id)
		ON CONFLICT (channel_id, provider_message_id) DO UPDATE SET batch = EXCLUDED.batch, state = 'pending', attempts = 0,
			next_attempt_at = now(), claimed_until = NULL, last_error = '', finished_at = NULL
		WHERE msg_provider_redactions.state <> 'done'`, append(args, batch)...)
	return err
}

// ClaimRedactions leases up to limit due deletions for a minute, of one
// batch or (batch "") of any, and counts the try.
func (s *Store) ClaimRedactions(ctx context.Context, batch string, limit int) ([]messaging.RedactJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE msg_provider_redactions SET claimed_until = now() + interval '1 minute', attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM msg_provider_redactions
			WHERE state = 'pending' AND next_attempt_at <= now() AND (claimed_until IS NULL OR claimed_until < now())
			  AND ($1 = '' OR batch = $1)
			ORDER BY next_attempt_at, id LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id, channel_id, provider, provider_message_id, attempts`, batch, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []messaging.RedactJob
	for rows.Next() {
		var j messaging.RedactJob
		if err := rows.Scan(&j.ID, &j.ChannelID, &j.Provider, &j.ProviderID, &j.Attempts); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FinishRedaction records a try.
func (s *Store) FinishRedaction(ctx context.Context, job messaging.RedactJob, res messaging.RedactResult) error {
	errText := res.Error
	if len(errText) > 200 {
		errText = errText[:200]
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE msg_provider_redactions SET state = $2, last_error = $3, claimed_until = NULL,
			next_attempt_at = now() + make_interval(secs => $4),
			finished_at = CASE WHEN $2 = 'pending' THEN NULL ELSE now() END
		WHERE id = $1`, job.ID, res.State, errText, res.RetryIn.Seconds())
	return err
}

// MaintainRedactions gives up on deletions past their last try and drops
// finished rows after 30 days.
func (s *Store) MaintainRedactions(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE msg_provider_redactions SET state = 'failed', finished_at = now(), last_error = 'retries exhausted'
		WHERE state = 'pending' AND attempts >= $1 AND (claimed_until IS NULL OR claimed_until < now())`, messaging.MaxRedactAttempts); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM msg_provider_redactions WHERE finished_at < now() - interval '30 days'`)
	return err
}

// RedactionCount is the state of the deletions of one provider.
type RedactionCount struct {
	Provider    string `json:"provider"`
	Pending     int    `json:"pending"`
	Done        int    `json:"done"`
	Failed      int    `json:"failed"`
	Unsupported int    `json:"unsupported"`
}

// RedactionCounts counts deletions by provider and state, of one batch or
// ("") of every row kept (the last 30 days and everything pending).
func (s *Store) RedactionCounts(ctx context.Context, batch string) ([]RedactionCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider, count(*) FILTER (WHERE state = 'pending'), count(*) FILTER (WHERE state = 'done'),
		       count(*) FILTER (WHERE state = 'failed'), count(*) FILTER (WHERE state = 'unsupported')
		FROM msg_provider_redactions WHERE $1 = '' OR batch = $1 GROUP BY provider ORDER BY provider`, batch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RedactionCount{}
	for rows.Next() {
		var c RedactionCount
		if err := rows.Scan(&c.Provider, &c.Pending, &c.Done, &c.Failed, &c.Unsupported); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ProviderReport fills the per-provider part of an erase or redaction
// report from its batch and records it in the audit log (counts only).
// providers names every provider the customer's conversations were on, so
// one with nothing to delete (a SIP trunk) is reported too.
func (s *Store) ProviderReport(ctx context.Context, actor, batch string, providers []string) ([]EraseProvider, error) {
	counts, err := s.RedactionCounts(ctx, batch)
	if err != nil {
		return nil, err
	}
	by := map[string]RedactionCount{}
	for _, c := range counts {
		by[c.Provider] = c
		found := false
		for _, p := range providers {
			found = found || p == c.Provider
		}
		if !found {
			providers = append(providers, c.Provider)
		}
	}
	out := []EraseProvider{}
	for _, p := range providers {
		c := by[p]
		e := EraseProvider{Provider: p, Messages: c.Pending + c.Done + c.Failed + c.Unsupported, Done: c.Done, Pending: c.Pending,
			Failed: c.Failed + c.Unsupported}
		e.Redacted = e.Messages > 0 && e.Done == e.Messages
		switch {
		case p == messaging.ProviderSIPTrunk:
			e.Detail = "the SIP trunk carrier keeps its own logs; Hello cannot delete them"
		case e.Messages == 0:
			e.Detail = "no message was sent through this provider; it keeps its own logs under its own terms"
		case c.Unsupported > 0:
			var on bool
			if err := s.db.QueryRowContext(ctx, `
				SELECT coalesce(bool_or(last_error LIKE '%profile_redaction_on%'), false) FROM msg_provider_redactions
				WHERE batch = $1 AND provider = $2 AND state = 'unsupported'`, batch, p).Scan(&on); err != nil {
				return nil, err
			}
			e.Detail = "the provider has no API to delete a single message: turn on its own redaction (Telnyx: the messaging profile's redaction setting); it keeps its own logs"
			if on {
				e.Detail = "the provider has no API to delete a single message; its own redaction is on for the messaging profile"
			}
		case e.Redacted:
			e.Detail = "deleted at the provider; it keeps its own logs under its own terms"
		case e.Pending > 0:
			e.Detail = "some deletions are still being retried in the background (for up to a day)"
		default:
			e.Detail = "the provider refused some deletions; see the Messaging settings for the counts"
		}
		out = append(out, e)
	}
	if len(counts) > 0 {
		err = s.tx(ctx, func(tx *sql.Tx) error {
			for _, e := range out {
				if e.Messages == 0 {
					continue
				}
				if err := insertAudit(ctx, tx, actor, fmt.Sprintf("provider_redact provider=%s done=%d pending=%d failed=%d",
					e.Provider, e.Done, e.Pending, e.Failed), "msg_peer", batch); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return out, err
}

// RedactReport is what a redaction of one message did.
type RedactReport struct {
	Message ConvMessage `json:"message"`
	// Providers is the provider-side part: empty when the message never
	// went through a carrier API.
	Providers []EraseProvider `json:"providers"`
	Batch     string          `json:"-"`
	// MediaObjects counts the message's attachment files removed from the
	// media store and checked gone; MediaObjectsPending those the store
	// could not remove now, which the media sweeper removes later.
	MediaObjects        int `json:"mediaObjects"`
	MediaObjectsPending int `json:"mediaObjectsPending"`
	// Media are the attachments whose files the caller removes, as after
	// an erase (spec M-20: the object deletions follow the transaction).
	Media []media.Garbage `json:"-"`
}

// ErrNotRedactable is a line that cannot be redacted (a system line).
var ErrNotRedactable = errors.New("store: this line cannot be redacted")

// RedactConversationMessage removes the text of one message or note: the
// body is emptied and marked redacted, the copies in the SIP text queue go,
// the attachments are detached from the message for the caller to remove
// (rep.Media, as an erase does; the media sweeper removes what the caller
// cannot), and a message sent through a carrier API is queued for deletion
// there (Batch). It is audited without content. Redacting a line twice
// changes nothing.
func (s *Store) RedactConversationMessage(ctx context.Context, actor string, convID, msgID int64) (RedactReport, error) {
	var rep RedactReport
	rep.Providers = []EraseProvider{}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var (
			kind, prov string
			providerID sql.NullString
			redacted   sql.NullTime
		)
		err := tx.QueryRowContext(ctx, `
			SELECT m.kind, ch.provider, m.provider_message_id, m.redacted_at
			FROM conversation_messages m JOIN msg_channels ch ON ch.id = m.channel_id
			WHERE m.id = $1 AND m.conversation_id = $2 FOR UPDATE OF m`, msgID, convID).Scan(&kind, &prov, &providerID, &redacted)
		if err != nil {
			return err
		}
		if kind == messaging.KindSystem {
			return ErrNotRedactable
		}
		if redacted.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_messages SET body = '', redacted_at = now() WHERE id = $1`, msgID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_message_id = $1`, msgID); err != nil {
			return err
		}
		if rep.Media, err = detachMessageMedia(ctx, tx, msgID); err != nil {
			return err
		}
		if providerID.Valid && prov != messaging.ProviderSIPTrunk {
			rep.Batch = newRedactionBatch("redact")
			if err := queueRedactions(ctx, tx, rep.Batch, `
				SELECT channel_id, $2::text, provider_message_id FROM conversation_messages WHERE id = $1`, msgID, prov); err != nil {
				return err
			}
		}
		if err := emitConversationEvent(ctx, tx, convID, msgID, "conversation.updated", nil); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "redact", "conversation_message", strconv.FormatInt(msgID, 10))
	})
	if err != nil {
		return rep, mapErr(err)
	}
	rep.Message, err = scanConvMessage(s.db.QueryRowContext(ctx, `SELECT `+convMessageCols+` FROM conversation_messages WHERE id = $1`, msgID))
	if err != nil {
		return rep, err
	}
	ms := []ConvMessage{rep.Message}
	if err := loadMedia(ctx, s.db, ms); err != nil {
		return rep, err
	}
	rep.Message = ms[0]
	return rep, nil
}

// detachMessageMedia takes a redacted message's attachments off it: each
// becomes an orphan (no message, no conversation, nothing left to fetch)
// that the caller removes now or the media sweeper later, and any public
// link to it stops working at once.
func detachMessageMedia(ctx context.Context, tx *sql.Tx, msgID int64) ([]media.Garbage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, object_key, TRUE FROM message_media WHERE message_id = $1 ORDER BY id FOR UPDATE`, msgID)
	if err != nil {
		return nil, err
	}
	gs := []media.Garbage{}
	for rows.Next() {
		var g media.Garbage
		if err := rows.Scan(&g.ID, &g.Key, &g.Drop); err != nil {
			_ = rows.Close()
			return nil, err
		}
		gs = append(gs, g)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return gs, nil
	}
	ids := garbageIDs(gs)
	if _, err := tx.ExecContext(ctx, `
		UPDATE message_media SET message_id = NULL, conversation_id = NULL, attached_at = coalesce(attached_at, now()), source_ref = '{}'
		WHERE id = ANY($1::bigint[])`, ids); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM media_links WHERE media_id = ANY($1::bigint[])`, ids)
	return gs, err
}

// ---------------------------------------------------------------------------
// Export (DSAR).

// ExportConversation is one conversation of a data subject access export.
type ExportConversation struct {
	ID           int64      `json:"id"`
	Channel      string     `json:"channel"`
	ChannelKind  string     `json:"channelKind"`
	Address      string     `json:"address"`
	Peer         string     `json:"peer"`
	PeerName     string     `json:"peerName"`
	Status       string     `json:"status"`
	Origin       string     `json:"origin"`
	Tags         []string   `json:"tags"`
	AssigneeName string     `json:"assigneeName"`
	CreatedAt    time.Time  `json:"createdAt"`
	ResolvedAt   *time.Time `json:"resolvedAt"`
	// Messages are the conversation's lines, oldest first.
	Messages []ExportMessage `json:"-"`
}

// ExportMessage is one line of an exported conversation.
type ExportMessage struct {
	ID          int64      `json:"id"`
	Direction   string     `json:"direction"`
	Kind        string     `json:"kind"`
	SenderKind  string     `json:"senderKind"`
	SenderName  string     `json:"senderName"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	ErrorCode   string     `json:"errorCode"`
	CreatedAt   time.Time  `json:"createdAt"`
	SentAt      *time.Time `json:"sentAt"`
	DeliveredAt *time.Time `json:"deliveredAt"`
	ReadAt      *time.Time `json:"readAt"`
	RedactedAt  *time.Time `json:"redactedAt"`
	// Media are the message's attachments (spec M-18), oldest first.
	Media []ExportMedia `json:"media"`
}

// ExportMedia is one attachment of an export: its metadata and, set by the
// caller that writes the archive, where the archive holds its file.
type ExportMedia struct {
	MediaFile
	// File is the file's path in the archive; empty when Hello holds no
	// file (still downloading, refused, expired or removed meanwhile).
	File string `json:"file"`
}

// PeerExport is everything Hello holds of one customer in the inbox.
type PeerExport struct {
	Peer          string               `json:"peer"`
	ExportedAt    time.Time            `json:"exportedAt"`
	Conversations []ExportConversation `json:"conversations"`
	// OptOuts are the customer's opt-out records, per number of ours.
	OptOuts []ExportOptOut `json:"optOuts"`
	// Uploads are a chat visitor's files not sent with a message yet; the
	// archive lists them in uploads.json.
	Uploads []ExportMedia `json:"-"`
}

// ExportOptOut is one opt-out record of the customer.
type ExportOptOut struct {
	Address string    `json:"address"`
	State   string    `json:"state"`
	Source  string    `json:"source"`
	At      time.Time `json:"at"`
}

// MaxExportMessages bounds one export, so a request cannot read the whole
// table into memory. A variable for tests.
var MaxExportMessages = 50000

// ErrExportTooLarge is an export over MaxExportMessages.
var ErrExportTooLarge = errors.New("store: the export is too large")

// ExportPeer reads everything of one customer (their number, reduced to its
// key, or a chat visitor) across every channel: conversations with their
// messages, and the opt-out records. Internal notes are agents' notes about
// the customer and are part of it only with notes. The export is audited
// with counts only.
func (s *Store) ExportPeer(ctx context.Context, actor, peerKey string, notes bool) (PeerExport, error) {
	out := PeerExport{Peer: peerKey, ExportedAt: time.Now().UTC(), Conversations: []ExportConversation{}, OptOuts: []ExportOptOut{},
		Uploads: []ExportMedia{}}
	var n int
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE c.peer_key = $1`, peerKey).Scan(&n); err != nil {
		return out, err
	}
	if n > MaxExportMessages {
		return out, ErrExportTooLarge
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, ch.name, ch.kind, a.address, c.peer_key, c.peer_name, c.status, c.origin, to_json(c.tags)::text,
		       coalesce(NULLIF(u.display_name, ''), u.username, ''), c.created_at, c.resolved_at
		FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id JOIN msg_addresses a ON a.id = c.address_id
		LEFT JOIN users u ON u.id = c.assignee_user_id
		WHERE c.peer_key = $1 ORDER BY c.created_at, c.id`, peerKey)
	if err != nil {
		return out, err
	}
	index := map[int64]int{}
	for rows.Next() {
		var (
			c        ExportConversation
			tags     string
			resolved sql.NullTime
		)
		if err := rows.Scan(&c.ID, &c.Channel, &c.ChannelKind, &c.Address, &c.Peer, &c.PeerName, &c.Status, &c.Origin, &tags,
			&c.AssigneeName, &c.CreatedAt, &resolved); err != nil {
			_ = rows.Close()
			return out, err
		}
		if err := json.Unmarshal([]byte(tags), &c.Tags); err != nil || c.Tags == nil {
			c.Tags = []string{}
		}
		c.ResolvedAt = nullTime(resolved)
		c.Messages = []ExportMessage{}
		index[c.ID] = len(out.Conversations)
		out.Conversations = append(out.Conversations, c)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	kinds := "('message', 'system')"
	if notes {
		kinds = "('message', 'system', 'note')"
	}
	//nolint:gosec // G202: kinds is one of two fixed lists.
	rows, err = s.db.QueryContext(ctx, `
		SELECT m.conversation_id, m.id, m.direction, m.kind, m.sender_kind, m.sender_name, m.body, m.status, m.error_code,
		       m.created_at, m.sent_at, m.delivered_at, m.read_at, m.redacted_at
		FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE c.peer_key = $1 AND m.kind IN `+kinds+` ORDER BY m.conversation_id, m.id`, peerKey)
	if err != nil {
		return out, err
	}
	count := 0
	// at finds an exported message by id: its conversation and place.
	at := map[int64][2]int{}
	for rows.Next() {
		var (
			conv                        int64
			m                           ExportMessage
			sent, deliv, read, redacted sql.NullTime
		)
		if err := rows.Scan(&conv, &m.ID, &m.Direction, &m.Kind, &m.SenderKind, &m.SenderName, &m.Body, &m.Status, &m.ErrorCode,
			&m.CreatedAt, &sent, &deliv, &read, &redacted); err != nil {
			_ = rows.Close()
			return out, err
		}
		m.SentAt, m.DeliveredAt, m.ReadAt, m.RedactedAt = nullTime(sent), nullTime(deliv), nullTime(read), nullTime(redacted)
		m.Media = []ExportMedia{}
		if i, ok := index[conv]; ok {
			at[m.ID] = [2]int{i, len(out.Conversations[i].Messages)}
			out.Conversations[i].Messages = append(out.Conversations[i].Messages, m)
			count++
		}
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	// The attachments of the exported lines, and a chat visitor's own
	// uploads not sent yet: everything an erase would remove.
	//nolint:gosec // G202: kinds is one of two fixed lists.
	files, err := s.exportMedia(ctx, `
		SELECT `+convMediaCols+`, coalesce(mm.conversation_id, 0), mm.object_key, mm.sha256 FROM message_media mm
		JOIN conversation_messages m ON m.id = mm.message_id JOIN conversations c ON c.id = m.conversation_id
		WHERE c.peer_key = $1 AND m.kind IN `+kinds+` ORDER BY mm.id`, peerKey)
	if err != nil {
		return out, err
	}
	nMedia := 0
	for _, f := range files {
		if f.MessageID == nil {
			continue
		}
		if p, ok := at[*f.MessageID]; ok {
			msg := &out.Conversations[p[0]].Messages[p[1]]
			msg.Media = append(msg.Media, f)
			nMedia++
		}
	}
	if out.Uploads, err = s.exportMedia(ctx, `
		SELECT `+convMediaCols+`, coalesce(mm.conversation_id, 0), mm.object_key, mm.sha256 FROM message_media mm
		JOIN chat_visitors v ON v.id = mm.uploader_visitor_id
		WHERE 'visitor:' || v.id::text = $1 AND mm.message_id IS NULL ORDER BY mm.id`, peerKey); err != nil {
		return out, err
	}
	nMedia += len(out.Uploads)
	rows, err = s.db.QueryContext(ctx, `
		SELECT a.address, o.state, o.source, o.at FROM sms_opt_outs o JOIN msg_addresses a ON a.id = o.address_id
		WHERE o.peer_key = $1 ORDER BY o.at`, peerKey)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var o ExportOptOut
		if err := rows.Scan(&o.Address, &o.State, &o.Source, &o.At); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.OptOuts = append(out.OptOuts, o)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		return insertAudit(ctx, tx, actor, fmt.Sprintf("export conversations=%d messages=%d media=%d notes=%t", len(out.Conversations), count,
			nMedia, notes), "msg_peer", "-")
	})
	return out, err
}

// exportMedia reads the attachments q selects (convMediaCols, then the
// conversation, the object key and the hash; $1 is the peer key).
func (s *Store) exportMedia(ctx context.Context, q, peerKey string) ([]ExportMedia, error) {
	rows, err := s.db.QueryContext(ctx, q, peerKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ExportMedia{}
	for rows.Next() {
		var f MediaFile
		m, err := scanConvMedia(rows, &f.ConversationID, &f.Key, &f.SHA256)
		if err != nil {
			return nil, err
		}
		f.ConvMedia = m
		out = append(out, ExportMedia{MediaFile: f})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// The supervisor-view audit report.

// SupervisorView is one audited look at a conversation by someone it was
// not assigned to (one row per person and conversation per hour).
type SupervisorView struct {
	ID             int64     `json:"id"`
	At             time.Time `json:"at"`
	Actor          string    `json:"actor"`
	UserID         *int64    `json:"userId"`
	UserName       string    `json:"userName"`
	ConversationID int64     `json:"conversationId"`
	// Via names the OAuth client or tool when the view did not come from the
	// console.
	Via string `json:"via"`
}

// SupervisorViewTotal sums one person's views in the period.
type SupervisorViewTotal struct {
	Actor         string `json:"actor"`
	UserName      string `json:"userName"`
	Views         int    `json:"views"`
	Conversations int    `json:"conversations"`
}

// SupervisorViewFilter selects the report: a period, optionally one person
// (an actor, user:<name>), a page before id Before.
type SupervisorViewFilter struct {
	From, To time.Time
	Actor    string
	Before   int64
	Limit    int
}

// SupervisorViewReport is a page of views and the totals of the period.
type SupervisorViewReport struct {
	Items      []SupervisorView      `json:"items"`
	NextBefore int64                 `json:"nextBefore"`
	Totals     []SupervisorViewTotal `json:"totals"`
}

// SupervisorViews reads the supervisor-view audit rows of a period, newest
// first, and who looked how often. It carries ids and names, never content.
func (s *Store) SupervisorViews(ctx context.Context, f SupervisorViewFilter) (SupervisorViewReport, error) {
	out := SupervisorViewReport{Items: []SupervisorView{}, Totals: []SupervisorViewTotal{}}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.at, e.actor, u.id, coalesce(NULLIF(u.display_name, ''), u.username, ''), e.resource_id, coalesce(e.via, '')
		FROM audit_events e LEFT JOIN users u ON 'user:' || u.username = e.actor
		WHERE e.action = 'supervisor_view' AND e.resource = 'conversation' AND e.at >= $1 AND e.at < $2
		  AND ($3 = '' OR e.actor = $3) AND ($4 = 0 OR e.id < $4)
		ORDER BY e.id DESC LIMIT $5`, f.From, f.To, f.Actor, f.Before, f.Limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var (
			v    SupervisorView
			uid  sql.NullInt64
			conv string
		)
		if err := rows.Scan(&v.ID, &v.At, &v.Actor, &uid, &v.UserName, &conv, &v.Via); err != nil {
			_ = rows.Close()
			return out, err
		}
		if uid.Valid {
			v.UserID = &uid.Int64
		}
		v.ConversationID, _ = strconv.ParseInt(conv, 10, 64)
		out.Items = append(out.Items, v)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.NextBefore = out.Items[len(out.Items)-1].ID
	}
	rows, err = s.db.QueryContext(ctx, `
		SELECT e.actor, coalesce(max(NULLIF(u.display_name, '')), max(u.username), ''), count(*), count(DISTINCT e.resource_id)
		FROM audit_events e LEFT JOIN users u ON 'user:' || u.username = e.actor
		WHERE e.action = 'supervisor_view' AND e.resource = 'conversation' AND e.at >= $1 AND e.at < $2
		  AND ($3 = '' OR e.actor = $3)
		GROUP BY e.actor ORDER BY count(*) DESC, e.actor`, f.From, f.To, f.Actor)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t SupervisorViewTotal
		if err := rows.Scan(&t.Actor, &t.UserName, &t.Views, &t.Conversations); err != nil {
			return out, err
		}
		out.Totals = append(out.Totals, t)
	}
	return out, rows.Err()
}
