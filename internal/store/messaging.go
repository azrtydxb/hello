package store

// Messaging channels, phase 1 (spec messaging-channels, "inbox core"): the
// channels and numbers an administrator sets up, the opt-out list, the
// settings and the maintenance (retention, erase) that keep customer data
// bounded. The conversations themselves are in messaging_inbox.go.
//
// Message text and customer numbers never reach a log or an audit row; the
// audit rows carry ids and counts.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
)

// Errors of the messaging store; the API maps them to a status.
var (
	// ErrOptedOut: the customer opted out of this number's texts.
	ErrOptedOut = errors.New("store: the customer opted out")
	// ErrChannelUnavailable: the conversation's channel is disabled or
	// cannot send.
	ErrChannelUnavailable = errors.New("store: the channel cannot send")
	// ErrNotAgent: the user cannot take conversations (disabled, or a role
	// without the inbox).
	ErrNotAgent = errors.New("store: user cannot take conversations")
	// ErrConversationState: the change does not fit the conversation's
	// state (assigning a resolved one, claiming an assigned one).
	ErrConversationState = errors.New("store: conversation state does not allow this")
)

// optOutPurpose keys the opt-out hash: it is derived from HELLO_SECRET_KEY
// and unrelated to the sealing key.
const optOutPurpose = "messaging-optout"

// MsgChannel is a place customer texts come from and go to.
type MsgChannel struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	// TrunkID and TrunkName name the SIP trunk of a sip_trunk channel.
	TrunkID        *int64     `json:"trunkId"`
	TrunkName      string     `json:"trunkName"`
	HasCredentials bool       `json:"hasCredentials"`
	Status         string     `json:"status"`
	StatusDetail   string     `json:"statusDetail"`
	LastEventAt    *time.Time `json:"lastEventAt"`
	Addresses      int        `json:"addresses"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// MsgChannelInput creates or replaces a channel.
type MsgChannelInput struct {
	Kind     string
	Provider string
	Name     string
	Enabled  bool
	TrunkID  *int64
}

const msgChannelCols = `c.id, c.kind, c.provider, c.name, c.enabled, c.trunk_id, coalesce(t.name, ''),
	c.credentials_enc IS NOT NULL, c.status, c.status_detail, c.last_event_at,
	(SELECT count(*) FROM msg_addresses a WHERE a.channel_id = c.id), c.created_at, c.updated_at`

const msgChannelFrom = ` FROM msg_channels c LEFT JOIN trunks t ON t.id = c.trunk_id`

func scanMsgChannel(r interface{ Scan(...any) error }) (MsgChannel, error) {
	var (
		c     MsgChannel
		trunk sql.NullInt64
		last  sql.NullTime
	)
	err := r.Scan(&c.ID, &c.Kind, &c.Provider, &c.Name, &c.Enabled, &trunk, &c.TrunkName,
		&c.HasCredentials, &c.Status, &c.StatusDetail, &last, &c.Addresses, &c.CreatedAt, &c.UpdatedAt)
	if trunk.Valid {
		c.TrunkID = &trunk.Int64
	}
	c.LastEventAt = nullTime(last)
	return c, err
}

// ListMsgChannels returns every channel.
func (s *Store) ListMsgChannels(ctx context.Context) ([]MsgChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgChannelCols+msgChannelFrom+` ORDER BY lower(c.name), c.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []MsgChannel
	for rows.Next() {
		c, err := scanMsgChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetMsgChannel returns one channel.
func (s *Store) GetMsgChannel(ctx context.Context, id int64) (MsgChannel, error) {
	c, err := scanMsgChannel(s.db.QueryRowContext(ctx, `SELECT `+msgChannelCols+msgChannelFrom+` WHERE c.id = $1`, id))
	return c, mapErr(err)
}

// HasMsgChannel reports whether any enabled channel has an enabled number:
// the inbox is shown only then.
func (s *Store) HasMsgChannel(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM msg_channels c
		JOIN msg_addresses a ON a.channel_id = c.id WHERE c.enabled AND a.enabled)`).Scan(&ok)
	return ok, err
}

func newHookID() (string, error) {
	b := make([]byte, 16) // 128 random bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// checkChannelTrunk refuses a channel whose trunk does not exist.
func checkChannelTrunk(ctx context.Context, tx *sql.Tx, in MsgChannelInput) error {
	if in.Provider != messaging.ProviderSIPTrunk {
		return nil
	}
	if in.TrunkID == nil {
		return fieldError("trunkId", "a SIP trunk channel needs a trunk")
	}
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM trunks WHERE id = $1)`, *in.TrunkID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fieldError("trunkId", "no such trunk")
	}
	return nil
}

// CreateMsgChannel adds a channel.
func (s *Store) CreateMsgChannel(ctx context.Context, actor string, in MsgChannelInput) (MsgChannel, error) {
	hook, err := newHookID()
	if err != nil {
		return MsgChannel{}, err
	}
	var id int64
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := checkChannelTrunk(ctx, tx, in); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO msg_channels (kind, provider, name, enabled, hook_id, trunk_id)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			in.Kind, in.Provider, in.Name, in.Enabled, hook, in.TrunkID).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "msg_channel", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgChannel{}, err
	}
	return s.GetMsgChannel(ctx, id)
}

// UpdateMsgChannel replaces a channel's settings; its kind and provider
// stay.
func (s *Store) UpdateMsgChannel(ctx context.Context, actor string, id int64, in MsgChannelInput) (MsgChannel, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var provider string
		if err := tx.QueryRowContext(ctx, `SELECT provider FROM msg_channels WHERE id = $1 FOR UPDATE`, id).Scan(&provider); err != nil {
			return err
		}
		in.Provider = provider
		if err := checkChannelTrunk(ctx, tx, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE msg_channels SET name = $2, enabled = $3, trunk_id = $4, updated_at = now()
			WHERE id = $1`, id, in.Name, in.Enabled, in.TrunkID); err != nil {
			return err
		}
		action := "update"
		if !in.Enabled {
			action = "disable"
		}
		return insertAudit(ctx, tx, actor, action, "msg_channel", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgChannel{}, err
	}
	return s.GetMsgChannel(ctx, id)
}

// DeleteMsgChannel removes a channel with no conversations; one that has
// history is refused (erase or let retention remove it first).
func (s *Store) DeleteMsgChannel(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE channel_id = $1`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &InUseError{Msg: fmt.Sprintf("the channel has %s; they are removed by retention or an erase request",
				plural(n, "a conversation", strconv.Itoa(n)+" conversations"))}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM msg_channels WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "msg_channel", strconv.FormatInt(id, 10))
	})
}

// MsgAddress is one number on a channel.
type MsgAddress struct {
	ID            int64     `json:"id"`
	ChannelID     int64     `json:"channelId"`
	ChannelName   string    `json:"channelName"`
	Address       string    `json:"address"`
	Label         string    `json:"label"`
	RouteKind     string    `json:"routeKind"`
	Enabled       bool      `json:"enabled"`
	OptOutWords   []string  `json:"optOutKeywords"`
	HelpText      string    `json:"helpText"`
	OptOutText    string    `json:"optOutText"`
	Conversations int       `json:"conversations"`
	CreatedAt     time.Time `json:"createdAt"`
	// Key is the lookup key inbound texts are matched by.
	Key string `json:"-"`
}

// MsgAddressInput creates or replaces a number.
type MsgAddressInput struct {
	ChannelID   int64
	Address     string
	Key         string
	Suffix      string
	Label       string
	Enabled     bool
	OptOutWords []string
	HelpText    string
	OptOutText  string
}

const msgAddressCols = `a.id, a.channel_id, c.name, a.address, a.address_key, a.label, a.route_kind, a.enabled,
	to_json(a.optout_keywords)::text, a.help_text, a.optout_text,
	(SELECT count(*) FROM conversations v WHERE v.address_id = a.id), a.created_at`

const msgAddressFrom = ` FROM msg_addresses a JOIN msg_channels c ON c.id = a.channel_id`

func scanMsgAddress(r interface{ Scan(...any) error }) (MsgAddress, error) {
	var (
		a     MsgAddress
		words string
	)
	if err := r.Scan(&a.ID, &a.ChannelID, &a.ChannelName, &a.Address, &a.Key, &a.Label, &a.RouteKind, &a.Enabled,
		&words, &a.HelpText, &a.OptOutText, &a.Conversations, &a.CreatedAt); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(words), &a.OptOutWords); err != nil {
		return a, err
	}
	return a, nil
}

// ListMsgAddresses returns the numbers of one channel, or of every channel
// when channelID is 0.
func (s *Store) ListMsgAddresses(ctx context.Context, channelID int64) ([]MsgAddress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgAddressCols+msgAddressFrom+`
		WHERE $1 = 0 OR a.channel_id = $1 ORDER BY lower(c.name), a.address, a.id`, channelID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []MsgAddress
	for rows.Next() {
		a, err := scanMsgAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetMsgAddress returns one number.
func (s *Store) GetMsgAddress(ctx context.Context, id int64) (MsgAddress, error) {
	a, err := scanMsgAddress(s.db.QueryRowContext(ctx, `SELECT `+msgAddressCols+msgAddressFrom+` WHERE a.id = $1`, id))
	return a, mapErr(err)
}

// CreateMsgAddress adds a number to a channel.
func (s *Store) CreateMsgAddress(ctx context.Context, actor string, in MsgAddressInput) (MsgAddress, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var ok bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM msg_channels WHERE id = $1)`, in.ChannelID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fieldError("channelId", "no such channel")
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO msg_addresses (channel_id, address, address_key, address_suffix, label, enabled, optout_keywords, help_text, optout_text)
			VALUES ($1, $2, $3, $9, $4, $5, $6::text[], $7, $8) RETURNING id`,
			in.ChannelID, in.Address, in.Key, in.Label, in.Enabled, keywordsArg(in.OptOutWords), in.HelpText, in.OptOutText, in.Suffix).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "msg_address", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgAddress{}, err
	}
	return s.GetMsgAddress(ctx, id)
}

// keywordsArg is the stored keyword list: lower case, or the default.
func keywordsArg(words []string) []string {
	if len(words) == 0 {
		return append([]string(nil), messaging.DefaultOptOutKeywords...)
	}
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToLower(strings.TrimSpace(w))
	}
	return out
}

// UpdateMsgAddress replaces a number's settings; its channel and number
// stay.
func (s *Store) UpdateMsgAddress(ctx context.Context, actor string, id int64, in MsgAddressInput) (MsgAddress, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE msg_addresses SET label = $2, enabled = $3, optout_keywords = $4::text[], help_text = $5, optout_text = $6
			WHERE id = $1`, id, in.Label, in.Enabled, keywordsArg(in.OptOutWords), in.HelpText, in.OptOutText)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "msg_address", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgAddress{}, err
	}
	return s.GetMsgAddress(ctx, id)
}

// DeleteMsgAddress removes a number with no conversations.
func (s *Store) DeleteMsgAddress(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE address_id = $1`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &InUseError{Msg: fmt.Sprintf("the number has %s; they are removed by retention or an erase request",
				plural(n, "a conversation", strconv.Itoa(n)+" conversations"))}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM msg_addresses WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "msg_address", strconv.FormatInt(id, 10))
	})
}

// MessagingSettings are the administrator-set inbox limits.
type MessagingSettings struct {
	RetentionSMSDays  int  `json:"retentionSmsDays"`
	RetentionChatDays int  `json:"retentionChatDays"`
	ReopenDays        int  `json:"reopenDays"`
	EmailNotify       bool `json:"emailNotify"`
}

// GetMessagingSettings returns the settings.
func (s *Store) GetMessagingSettings(ctx context.Context) (MessagingSettings, error) {
	return getMessagingSettings(ctx, s.db)
}

func getMessagingSettings(ctx context.Context, q rowQuerier) (MessagingSettings, error) {
	var m MessagingSettings
	err := q.QueryRowContext(ctx, `SELECT retention_sms_days, retention_chat_days, reopen_days, email_notify
		FROM messaging_settings`).Scan(&m.RetentionSMSDays, &m.RetentionChatDays, &m.ReopenDays, &m.EmailNotify)
	return m, err
}

// PutMessagingSettings replaces the settings.
func (s *Store) PutMessagingSettings(ctx context.Context, actor string, m MessagingSettings) (MessagingSettings, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE messaging_settings SET retention_sms_days = $1, retention_chat_days = $2, reopen_days = $3, email_notify = $4`,
			m.RetentionSMSDays, m.RetentionChatDays, m.ReopenDays, m.EmailNotify); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "messaging_settings", "1")
	})
	if err != nil {
		return MessagingSettings{}, err
	}
	return s.GetMessagingSettings(ctx)
}

// peerHash is what the opt-out list stores of a number.
func (s *Store) peerHash(peerKey string) ([]byte, error) {
	if s.box == nil {
		return nil, ErrNoSecretBox
	}
	return s.box.MAC(optOutPurpose, peerKey), nil
}

// MsgOptOut is one number's opt-out state on one of our numbers.
type MsgOptOut struct {
	ID        int64     `json:"id"`
	AddressID int64     `json:"addressId"`
	Address   string    `json:"address"`
	Channel   string    `json:"channel"`
	Peer      string    `json:"peer"`
	State     string    `json:"state"`
	Source    string    `json:"source"`
	Note      string    `json:"note"`
	At        time.Time `json:"at"`
}

// OptOutFilter selects opt-outs; Q matches the end of the number.
type OptOutFilter struct {
	AddressID int64
	Q         string
	// All includes numbers that opted back in.
	All bool
}

// ListOptOuts returns the opted-out numbers, newest first, at most limit.
func (s *Store) ListOptOuts(ctx context.Context, f OptOutFilter, limit int) ([]MsgOptOut, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.address_id, a.address, c.name, coalesce(o.peer_key, ''), o.state, o.source, o.note, o.at
		FROM sms_opt_outs o JOIN msg_addresses a ON a.id = o.address_id JOIN msg_channels c ON c.id = a.channel_id
		WHERE ($1 = 0 OR o.address_id = $1) AND ($2 OR o.state = 'out')
		  AND ($3 = '' OR o.peer_key LIKE '%' || $3)
		ORDER BY o.at DESC, o.id DESC LIMIT $4`, f.AddressID, f.All, escapeLike(f.Q), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []MsgOptOut
	for rows.Next() {
		var o MsgOptOut
		if err := rows.Scan(&o.ID, &o.AddressID, &o.Address, &o.Channel, &o.Peer, &o.State, &o.Source, &o.Note, &o.At); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// escapeLike makes s safe inside a LIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// RecordOptOut records that a number opted out of an address by an
// administrator's hand (a customer who asked by phone, say). It audits who
// did it, never the number.
func (s *Store) RecordOptOut(ctx context.Context, actor string, addressID int64, peerKey, note string) (MsgOptOut, error) {
	h, err := s.peerHash(peerKey)
	if err != nil {
		return MsgOptOut{}, err
	}
	var id int64
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var ok bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM msg_addresses WHERE id = $1)`, addressID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fieldError("addressId", "no such number")
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO sms_opt_outs (address_id, peer_key, peer_hash, state, source, note)
			VALUES ($1, $2, $3, 'out', 'admin', $4)
			ON CONFLICT (address_id, peer_hash) DO UPDATE SET state = 'out', source = 'admin', note = $4, at = now(),
				peer_key = coalesce(sms_opt_outs.peer_key, $2)
			RETURNING id`, addressID, peerKey, h, note).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "optout", "msg_optout", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgOptOut{}, err
	}
	return s.getOptOut(ctx, id)
}

func (s *Store) getOptOut(ctx context.Context, id int64) (MsgOptOut, error) {
	var o MsgOptOut
	err := s.db.QueryRowContext(ctx, `
		SELECT o.id, o.address_id, a.address, c.name, coalesce(o.peer_key, ''), o.state, o.source, o.note, o.at
		FROM sms_opt_outs o JOIN msg_addresses a ON a.id = o.address_id JOIN msg_channels c ON c.id = a.channel_id
		WHERE o.id = $1`, id).Scan(&o.ID, &o.AddressID, &o.Address, &o.Channel, &o.Peer, &o.State, &o.Source, &o.Note, &o.At)
	return o, mapErr(err)
}

// Reconsent records a customer's verbal re-consent, which clears the
// opt-out. It is audited, with the note an administrator wrote.
func (s *Store) Reconsent(ctx context.Context, actor string, id int64, note string) (MsgOptOut, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sms_opt_outs SET state = 'in', source = 'admin', note = $2, at = now() WHERE id = $1`, id, note)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "reconsent", "msg_optout", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgOptOut{}, err
	}
	return s.getOptOut(ctx, id)
}

// EraseReport is what an erase removed; it holds counts only.
type EraseReport struct {
	Conversations int `json:"conversations"`
	Messages      int `json:"messages"`
	// OptOutsKept counts the opt-outs reduced to their hash.
	OptOutsKept int `json:"optOutsKept"`
	// Providers lists, per provider, whether provider-side deletion was
	// tried. The carrier keeps its own logs under its own terms.
	Providers []EraseProvider `json:"providers"`
}

// EraseProvider is one provider's part of an erase.
type EraseProvider struct {
	Provider string `json:"provider"`
	Redacted bool   `json:"redacted"`
	Detail   string `json:"detail"`
}

// ErasePeer deletes every conversation, message and note of a customer
// (their number, reduced to its key) on every channel, and keeps only the
// opt-out hash, so a deleted customer is not texted again. The audit row
// holds counts only. Backups keep the rows until they expire.
func (s *Store) ErasePeer(ctx context.Context, actor, peerKey string) (EraseReport, error) {
	var rep EraseReport
	rep.Providers = []EraseProvider{}
	h, err := s.peerHash(peerKey)
	if err != nil {
		return rep, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		providers, err := queryStrings(ctx, tx, `
			SELECT DISTINCT ch.provider FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id
			WHERE c.peer_key = $1 ORDER BY 1`, peerKey)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM conversation_messages WHERE conversation_id IN (SELECT id FROM conversations WHERE peer_key = $1)`,
			peerKey).Scan(&rep.Messages); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE peer_key = $1`, peerKey)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		rep.Conversations = int(n)
		res, err = tx.ExecContext(ctx, `UPDATE sms_opt_outs SET peer_key = NULL, note = '' WHERE peer_hash = $1`, h)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		rep.OptOutsKept = int(n)
		for _, p := range providers {
			// The SIP trunk carrier holds no copy Hello can delete.
			rep.Providers = append(rep.Providers, EraseProvider{Provider: p, Redacted: false,
				Detail: "the carrier keeps its own logs; Hello cannot delete them"})
		}
		return insertAudit(ctx, tx, actor, fmt.Sprintf("erase conversations=%d messages=%d", rep.Conversations, rep.Messages), "msg_peer", "-")
	})
	return rep, err
}

// MessagingMaintenance is what one maintenance run did, counts only.
type MessagingMaintenance struct {
	Woken         int64
	Conversations int64
	Events        int64
}

const purgeBatch = 500

// MaintainMessaging wakes snoozed conversations, purges finished
// conversations past their retention (open ones never go by age) in batches
// and drops old events and ledger rows. Every step is idempotent, so any
// replica may run it.
func (s *Store) MaintainMessaging(ctx context.Context) (MessagingMaintenance, error) {
	var out MessagingMaintenance
	set, err := s.GetMessagingSettings(ctx)
	if err != nil {
		return out, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			UPDATE conversations SET status = 'open', snoozed_until = NULL
			WHERE status = 'snoozed' AND snoozed_until <= now() RETURNING id`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.Woken = int64(len(ids))
		for _, id := range ids {
			if err := emitConversationEvent(ctx, tx, id, 0, "conversation.updated", nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	for {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM conversations WHERE id IN (
				SELECT c.id FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id
				WHERE c.status IN ('resolved', 'spam')
				  AND coalesce(c.resolved_at, c.last_message_at) < now() - make_interval(days => CASE WHEN ch.kind = 'webchat' THEN $1::int ELSE $2::int END)
				ORDER BY c.id LIMIT $3::int)`, set.RetentionChatDays, set.RetentionSMSDays, purgeBatch)
		if err != nil {
			return out, err
		}
		n, _ := res.RowsAffected()
		out.Conversations += n
		if n < purgeBatch {
			break
		}
	}
	for _, q := range []string{
		`DELETE FROM conversation_events WHERE at < now() - interval '7 days'`,
		`DELETE FROM messaging_events WHERE received_at < now() - interval '7 days'`,
		`DELETE FROM conversation_assignments WHERE at < now() - interval '365 days'`,
		`DELETE FROM conversation_notify WHERE last_email_at < now() - interval '1 day'`,
		`DELETE FROM inbox_presence WHERE seen_at < now() - interval '1 day'`,
	} {
		res, err := s.db.ExecContext(ctx, q)
		if err != nil {
			return out, err
		}
		n, _ := res.RowsAffected()
		out.Events += n
	}
	if out.Conversations > 0 {
		err = s.tx(ctx, func(tx *sql.Tx) error {
			return insertAudit(ctx, tx, "system", fmt.Sprintf("purge conversations=%d", out.Conversations), "messaging", "-")
		})
	}
	return out, err
}
