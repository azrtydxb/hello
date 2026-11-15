package store

// Messaging media (spec messaging-channels M-18): the attachments of inbox
// messages. The files live in MinIO (internal/messaging/media); this file
// keeps their rows: who may read one (the people who can see its
// conversation), the uploads waiting for their message, the inbound
// downloads the media worker claims, the public links of a later phase, and
// what retention and an erase leave for the sweeper to remove.
//
// Nothing here logs a file name, a type a customer sent or a carrier's URL.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/media"
	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// ErrMediaUnsupported is an attachment on a channel that cannot carry one:
// a SIP trunk never does, and a carrier channel needs the public listener's
// media links, which are not on in the LAN-only build (spec M-18).
var ErrMediaUnsupported = errors.New("store: the channel cannot carry attachments")

// ErrMediaNotReady is an attachment that has no file to serve: still
// downloading, refused or expired.
var ErrMediaNotReady = errors.New("store: the attachment has no file")

// ConvMedia is one attachment of a message as the inbox shows it.
type ConvMedia struct {
	ID          int64  `json:"id"`
	MessageID   *int64 `json:"messageId"`
	Direction   string `json:"direction"`
	Status      string `json:"status"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Filename    string `json:"filename"`
	// Reason says why a file was refused or expired (a short token).
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

// MediaFile is an attachment with where its file is kept: what serving it,
// an export or an erase needs. The key is never shown.
type MediaFile struct {
	ConvMedia
	ConversationID int64  `json:"conversationId"`
	Key            string `json:"-"`
	SHA256         string `json:"sha256"`
}

// Stored is what media.Serve needs of it.
func (m MediaFile) Stored() media.Stored {
	return media.Stored{Key: m.Key, ContentType: m.ContentType, Filename: m.Filename}
}

const convMediaCols = `mm.id, mm.message_id, mm.direction, mm.status, mm.content_type, mm.size, mm.filename, mm.reason, mm.created_at`

func scanConvMedia(r interface{ Scan(...any) error }, extra ...any) (ConvMedia, error) {
	var (
		m   ConvMedia
		msg sql.NullInt64
	)
	dest := append([]any{&m.ID, &msg, &m.Direction, &m.Status, &m.ContentType, &m.Size, &m.Filename, &m.Reason, &m.CreatedAt}, extra...)
	if err := r.Scan(dest...); err != nil {
		return m, err
	}
	if msg.Valid {
		m.MessageID = &msg.Int64
	}
	return m, nil
}

// loadMedia fills the attachments of msgs.
func loadMedia(ctx context.Context, q dbQuery, msgs []ConvMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]int64, len(msgs))
	at := make(map[int64]int, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
		at[m.ID] = i
		if msgs[i].Media == nil {
			msgs[i].Media = []ConvMedia{}
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT `+convMediaCols+` FROM message_media mm
		WHERE mm.message_id = ANY($1::bigint[]) ORDER BY mm.id`, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		m, err := scanConvMedia(rows)
		if err != nil {
			return err
		}
		if i, ok := at[*m.MessageID]; ok {
			msgs[i].Media = append(msgs[i].Media, m)
		}
	}
	return rows.Err()
}

// MediaUploadInput is a file an agent or a visitor uploaded, already
// inspected.
type MediaUploadInput struct {
	media.Inspected
	Filename string
	Declared string
}

// MediaUpload is an upload's row: the API stores the file under Key and then
// finishes or aborts it.
type MediaUpload struct {
	ID        int64
	Key       string
	ChannelID int64
}

// beginUpload inserts the pending row of an upload and names its key.
func beginUpload(ctx context.Context, tx *sql.Tx, channelID int64, conv, user, visitor any, dir string, in MediaUploadInput) (MediaUpload, error) {
	var (
		up MediaUpload
		at time.Time
	)
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO message_media (conversation_id, channel_id, direction, status, content_type, declared_type, size, sha256,
			filename, uploader_user_id, uploader_visitor_id)
		VALUES ($1, $2, $3, 'pending', $4, $5, $6, $7, $8, $9, $10) RETURNING id, created_at`,
		conv, channelID, dir, in.ContentType, clip(in.Declared, 100), in.Size, in.SHA256,
		media.CleanFilename(in.Filename, in.ContentType), user, visitor).Scan(&up.ID, &at); err != nil {
		return up, err
	}
	up.ChannelID = channelID
	up.Key = media.UploadKey(channelID, at, up.ID, in.SHA256)
	_, err := tx.ExecContext(ctx, `UPDATE message_media SET object_key = $2 WHERE id = $1`, up.ID, up.Key)
	return up, err
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// BeginInboxUpload records an agent's upload to a conversation they can see.
// Only a website chat carries an agent's attachments in this build; any
// other channel is ErrMediaUnsupported.
func (s *Store) BeginInboxUpload(ctx context.Context, acc InboxAccess, convID int64, in MediaUploadInput) (MediaUpload, error) {
	var up MediaUpload
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var (
			channelID int64
			prov      string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT c.channel_id, ch.provider FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id
			WHERE c.id = $3 AND `+visible, acc.UserID, acc.All, convID).Scan(&channelID, &prov)
		if err != nil {
			return mapErr(err)
		}
		if prov != messaging.ProviderChat {
			return ErrMediaUnsupported
		}
		up, err = beginUpload(ctx, tx, channelID, convID, acc.UserID, nil, messaging.DirOut, in)
		return err
	})
	return up, err
}

// FinishMediaUpload marks an upload's file stored and returns it.
func (s *Store) FinishMediaUpload(ctx context.Context, id int64) (ConvMedia, error) {
	m, err := scanConvMedia(s.db.QueryRowContext(ctx, `
		UPDATE message_media mm SET status = 'ready' WHERE id = $1 AND status = 'pending' AND attached_at IS NULL
		RETURNING `+convMediaCols, id))
	return m, mapErr(err)
}

// AbortMediaUpload drops an upload whose file could not be stored. The
// caller removes whatever part of the object may exist.
func (s *Store) AbortMediaUpload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM message_media WHERE id = $1 AND attached_at IS NULL`, id)
	return err
}

// cleanMediaIDs refuses more than media.MaxPerMessage attachments or the same
// one twice.
func cleanMediaIDs(ids []int64) error {
	if len(ids) > media.MaxPerMessage {
		return fieldError("mediaIds", fmt.Sprintf("at most %d attachments per message", media.MaxPerMessage))
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return fieldError("mediaIds", "each attachment once")
		}
		seen[id] = true
	}
	return nil
}

// attachUploads joins an agent's ready uploads to the message msgID they
// were sent with. Each must be the caller's, made for this conversation, and
// not sent before.
func attachUploads(ctx context.Context, tx *sql.Tx, convID, msgID, userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE message_media SET message_id = $1, attached_at = now()
		WHERE id = ANY($2::bigint[]) AND conversation_id = $3 AND uploader_user_id = $4
		  AND message_id IS NULL AND attached_at IS NULL AND status = 'ready' AND direction = 'out'`,
		msgID, ids, convID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); int(n) != len(ids) {
		return fieldError("mediaIds", "an attachment is not one of your uploads to this conversation, or was sent already")
	}
	return nil
}

// InboxMedia returns an attachment the caller may read: one of a
// conversation they can see, or their own upload not sent yet.
func (s *Store) InboxMedia(ctx context.Context, acc InboxAccess, id int64) (MediaFile, error) {
	var f MediaFile
	m, err := scanConvMedia(s.db.QueryRowContext(ctx, `
		SELECT `+convMediaCols+`, c.id, mm.object_key, mm.sha256 FROM message_media mm JOIN conversations c ON c.id = mm.conversation_id
		WHERE mm.id = $3 AND (mm.attached_at IS NOT NULL OR mm.uploader_user_id = $1) AND `+visible,
		acc.UserID, acc.All, id), &f.ConversationID, &f.Key, &f.SHA256)
	if err != nil {
		return f, mapErr(err)
	}
	f.ConvMedia = m
	if m.Status != media.StatusReady || f.Key == "" {
		return f, ErrMediaNotReady
	}
	return f, nil
}

// ChatBeginUpload records a visitor's attachment to their chat. It joins
// their next message.
func (s *Store) ChatBeginUpload(ctx context.Context, site ChatSite, visitorID int64, in MediaUploadInput) (MediaUpload, error) {
	var up MediaUpload
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		up, err = beginUpload(ctx, tx, site.ChannelID, nil, nil, visitorID, messaging.DirIn, in)
		return err
	})
	return up, err
}

// attachChatUploads joins a visitor's ready uploads to their message.
func attachChatUploads(ctx context.Context, tx *sql.Tx, convID, msgID, visitorID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE message_media SET message_id = $1, conversation_id = $2, attached_at = now()
		WHERE id = ANY($3::bigint[]) AND uploader_visitor_id = $4
		  AND message_id IS NULL AND attached_at IS NULL AND status = 'ready'`,
		msgID, convID, ids, visitorID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); int(n) != len(ids) {
		return fieldError("mediaIds", "an attachment is not this visitor's or was sent already")
	}
	return nil
}

// ChatMedia returns an attachment a visitor may read: one of a message their
// chat shows (theirs, or an agent's reply that was sent).
func (s *Store) ChatMedia(ctx context.Context, visitorID, id int64) (MediaFile, error) {
	var f MediaFile
	m, err := scanConvMedia(s.db.QueryRowContext(ctx, `
		SELECT `+convMediaCols+`, c.id, mm.object_key, mm.sha256 FROM message_media mm
		JOIN conversation_messages x ON x.id = mm.message_id
		JOIN conversations c ON c.id = x.conversation_id
		WHERE mm.id = $1 AND c.peer_kind = 'visitor' AND c.peer_key = $2 AND x.kind = 'message'
		  AND x.status IN ('sent', 'delivered', 'read')`, id, chatPeerKey(visitorID)), &f.ConversationID, &f.Key, &f.SHA256)
	if err != nil {
		return f, mapErr(err)
	}
	f.ConvMedia = m
	if m.Status != media.StatusReady || f.Key == "" {
		return f, ErrMediaNotReady
	}
	return f, nil
}

// ConversationMedia lists every attachment of a conversation with where its
// file is kept, oldest first, for an export (spec M-20, DSAR) or an erase.
// It checks no access: the caller is an administrator's route.
func (s *Store) ConversationMedia(ctx context.Context, convID int64) ([]MediaFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+convMediaCols+`, mm.conversation_id, mm.object_key, mm.sha256 FROM message_media mm
		WHERE mm.conversation_id = $1 ORDER BY mm.id`, convID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []MediaFile{}
	for rows.Next() {
		var f MediaFile
		m, err := scanConvMedia(rows, &f.ConversationID, &f.Key, &f.SHA256)
		if err != nil {
			return nil, err
		}
		f.ConvMedia = m
		out = append(out, f)
	}
	return out, rows.Err()
}

// insertInboundMedia records the attachments of an inbound carrier message
// as pending, in the ingest's transaction (spec M-25 step 3): the media
// worker downloads them, and the message is visible meanwhile.
func insertInboundMedia(ctx context.Context, tx *sql.Tx, convID, msgID, channelID int64, refs []provider.MediaRef) error {
	for _, ref := range refs {
		src, err := json.Marshal(ref)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_media (message_id, conversation_id, channel_id, direction, status, declared_type, size, source_ref, attached_at)
			VALUES ($1, $2, $3, 'in', 'pending', $4, $5, $6::jsonb, now())`,
			msgID, convID, channelID, clip(ref.ContentType, 100), max(ref.Size, 0), string(src)); err != nil {
			return err
		}
	}
	return nil
}

// AddInboundMedia records the attachments of an inbound carrier message (its
// MMS media) for the media worker to download. It is the ingest's step for
// media; the public listener's webhook and a reconcile pull call it.
func (s *Store) AddInboundMedia(ctx context.Context, messageID int64, refs []provider.MediaRef) error {
	if len(refs) > media.MaxPerMessage {
		refs = refs[:media.MaxPerMessage]
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var convID, channelID int64
		if err := tx.QueryRowContext(ctx, `SELECT conversation_id, channel_id FROM conversation_messages WHERE id = $1 AND direction = 'in'`,
			messageID).Scan(&convID, &channelID); err != nil {
			return mapErr(err)
		}
		return insertInboundMedia(ctx, tx, convID, messageID, channelID, refs)
	})
}

// mediaLease is how long a claimed download is another worker's; a worker
// that dies leaves the row to be claimed again after it.
const mediaLease = 5 * time.Minute

// ClaimMediaFetch hands up to limit inbound attachments due for a download
// to this worker (FOR UPDATE SKIP LOCKED: each to one worker).
func (s *Store) ClaimMediaFetch(ctx context.Context, limit int) ([]media.FetchJob, error) {
	var out []media.FetchJob
	err := s.tx(ctx, func(tx *sql.Tx) error {
		out = nil
		rows, err := tx.QueryContext(ctx, `
			UPDATE message_media mm SET attempts = mm.attempts + 1, next_attempt_at = now() + make_interval(secs => $2)
			FROM msg_channels ch
			WHERE ch.id = mm.channel_id AND mm.id IN (
				SELECT id FROM message_media WHERE status = 'pending' AND direction = 'in' AND attached_at IS NOT NULL
				  AND message_id IS NOT NULL AND next_attempt_at <= now()
				ORDER BY next_attempt_at, id LIMIT $1 FOR UPDATE SKIP LOCKED)
			RETURNING mm.id, mm.message_id, mm.channel_id, ch.provider, mm.source_ref::text, mm.created_at, mm.attempts - 1`,
			limit, mediaLease.Seconds())
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				j   media.FetchJob
				src string
			)
			if err := rows.Scan(&j.ID, &j.MessageID, &j.ChannelID, &j.Provider, &src, &j.CreatedAt, &j.Attempts); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(src), &j.Ref); err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// FinishMediaFetch records a download's outcome and tells the people who see
// the conversation that the message changed.
func (s *Store) FinishMediaFetch(ctx context.Context, job media.FetchJob, res media.FetchResult) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var convID sql.NullInt64
		var err error
		switch res.Status {
		case media.StatusReady:
			err = tx.QueryRowContext(ctx, `
				UPDATE message_media SET status = 'ready', object_key = $2, content_type = $3, size = $4, sha256 = $5,
					declared_type = $6, filename = $7, reason = '', source_ref = '{}'
				WHERE id = $1 AND status = 'pending' RETURNING conversation_id`,
				job.ID, res.Key, res.ContentType, res.Size, res.SHA256, clip(res.Declared, 100),
				"attachment"+media.Extension(res.ContentType)).Scan(&convID)
		case media.StatusRejected, media.StatusExpired:
			// The carrier's URL is dropped with the attempt: nothing is
			// fetched from it again.
			err = tx.QueryRowContext(ctx, `
				UPDATE message_media SET status = $2, reason = $3, declared_type = coalesce(NULLIF($4, ''), declared_type), source_ref = '{}'
				WHERE id = $1 AND status = 'pending' RETURNING conversation_id`,
				job.ID, res.Status, clip(res.Reason, 64), clip(res.Declared, 100)).Scan(&convID)
		default:
			_, err = tx.ExecContext(ctx, `UPDATE message_media SET next_attempt_at = now() + make_interval(secs => $2)
				WHERE id = $1 AND status = 'pending'`, job.ID, res.Retry.Seconds())
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil // erased or purged meanwhile; the sweeper has the object
		}
		if err != nil || !convID.Valid {
			return err
		}
		return emitConversationEvent(ctx, tx, convID.Int64, job.MessageID, "message.status", nil)
	})
}

// MediaGarbage lists up to limit attachments whose file goes: orphans (their
// message was purged or erased), uploads unsent after media.UploadTTL or
// never stored, and files past the media retention (spec M-19).
func (s *Store) MediaGarbage(ctx context.Context, limit int) ([]media.Garbage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, object_key, TRUE FROM message_media WHERE message_id IS NULL AND attached_at IS NOT NULL
		UNION ALL
		SELECT id, object_key, TRUE FROM message_media WHERE message_id IS NULL AND attached_at IS NULL
			AND created_at < now() - make_interval(secs => $2)
		UNION ALL
		SELECT id, object_key, FALSE FROM message_media WHERE status = 'ready' AND message_id IS NOT NULL
			AND created_at < now() - make_interval(days => (SELECT retention_media_days FROM messaging_settings))
		LIMIT $1`, limit, media.UploadTTL.Seconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []media.Garbage
	for rows.Next() {
		var g media.Garbage
		if err := rows.Scan(&g.ID, &g.Key, &g.Drop); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DropMedia settles an attachment whose file was removed: an orphan or an
// unsent upload loses its row; a file past retention leaves its row expired.
func (s *Store) DropMedia(ctx context.Context, g media.Garbage) error {
	if g.Drop {
		_, err := s.db.ExecContext(ctx, `DELETE FROM message_media WHERE id = $1`, g.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE message_media SET status = 'expired', object_key = '', reason = $2 WHERE id = $1`,
		g.ID, media.ReasonRetention)
	return err
}

// MintMediaLink makes a public link to an outbound attachment that is ready
// (spec M-18, "Outbound"): it works for media.LinkTTL and media.LinkMaxUses
// fetches. The token is returned once; only its SHA-256 is kept.
func (s *Store) MintMediaLink(ctx context.Context, mediaID int64) (string, time.Time, error) {
	tok, hash, err := media.NewLinkToken()
	if err != nil {
		return "", time.Time{}, err
	}
	var exp time.Time
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO media_links (token_hash, media_id, expires_at, max_uses)
		SELECT $1, id, now() + make_interval(secs => $3), $4 FROM message_media
		WHERE id = $2 AND direction = 'out' AND status = 'ready' AND object_key <> ''
		RETURNING expires_at`, hash, mediaID, media.LinkTTL.Seconds(), media.LinkMaxUses).Scan(&exp)
	if err != nil {
		return "", time.Time{}, mapErr(err)
	}
	return tok, exp, nil
}

// RedeemMediaLink counts one use of a link and returns its attachment. A
// link that is unknown, expired or used up is ErrNotFound, and so is one
// whose file is gone.
func (s *Store) RedeemMediaLink(ctx context.Context, tokenHash []byte) (media.Stored, error) {
	var f media.Stored
	err := s.db.QueryRowContext(ctx, `
		WITH used AS (
			UPDATE media_links SET uses = uses + 1
			WHERE token_hash = $1 AND expires_at > now() AND uses < max_uses RETURNING media_id)
		SELECT mm.object_key, mm.content_type, mm.filename FROM used JOIN message_media mm ON mm.id = used.media_id
		WHERE mm.status = 'ready' AND mm.object_key <> ''`, tokenHash).Scan(&f.Key, &f.ContentType, &f.Filename)
	return f, mapErr(err)
}

// peerMedia lists the attachments of a peer's conversations and of their
// chat uploads, for an erase.
func peerMedia(ctx context.Context, tx *sql.Tx, peerKey string) ([]media.Garbage, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, object_key, TRUE FROM message_media
		WHERE conversation_id IN (SELECT id FROM conversations WHERE peer_key = $1)
		   OR uploader_visitor_id IN (SELECT id FROM chat_visitors WHERE 'visitor:' || id::text = $1)
		FOR UPDATE`, peerKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []media.Garbage{}
	for rows.Next() {
		var g media.Garbage
		if err := rows.Scan(&g.ID, &g.Key, &g.Drop); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetChatSiteLogo records that a site's logo (stored at media.LogoKey) has
// the type ctype.
func (s *Store) SetChatSiteLogo(ctx context.Context, actor string, id int64, ctype string) (ChatSite, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE chat_sites SET logo_type = $2, logo_updated_at = now(), updated_at = now() WHERE id = $1`, id, ctype)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "logo", "chat_site", fmt.Sprint(id))
	})
	if err != nil {
		return ChatSite{}, err
	}
	return s.GetChatSite(ctx, id)
}

// ClearChatSiteLogo removes a site's logo; the caller removes the object.
func (s *Store) ClearChatSiteLogo(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE chat_sites SET logo_type = '', logo_updated_at = NULL, updated_at = now() WHERE id = $1 AND logo_type <> ''`, id)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "logo_remove", "chat_site", fmt.Sprint(id))
	})
}
