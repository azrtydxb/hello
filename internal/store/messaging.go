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

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/media"
	"github.com/azrtydxb/hello/internal/messaging/provider"
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
	TrunkID        *int64 `json:"trunkId"`
	TrunkName      string `json:"trunkName"`
	HasCredentials bool   `json:"hasCredentials"`
	// AccountHint is the tail of a carrier account's SID, enough to tell
	// accounts apart; never the token.
	AccountHint      string     `json:"accountHint"`
	MessagingService bool       `json:"messagingService"`
	Status           string     `json:"status"`
	StatusDetail     string     `json:"statusDetail"`
	LastEventAt      *time.Time `json:"lastEventAt"`
	Addresses        int        `json:"addresses"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// MsgChannelInput creates or replaces a channel.
type MsgChannelInput struct {
	Kind     string
	Provider string
	Name     string
	Enabled  bool
	TrunkID  *int64
	// Credentials seal a carrier account; nil keeps the stored ones. A
	// carrier channel needs them on creation.
	Credentials *provider.Config
}

const msgChannelCols = `c.id, c.kind, c.provider, c.name, c.enabled, c.trunk_id, coalesce(t.name, ''),
	c.credentials_enc IS NOT NULL, coalesce(c.config->>'accountHint', ''), coalesce(c.config->>'messagingService', '') = 'true', c.status, c.status_detail, c.last_event_at,
	(SELECT count(*) FROM msg_addresses a WHERE a.channel_id = c.id), c.created_at, c.updated_at`

const msgChannelFrom = ` FROM msg_channels c LEFT JOIN trunks t ON t.id = c.trunk_id`

func scanMsgChannel(r interface{ Scan(...any) error }) (MsgChannel, error) {
	var (
		c     MsgChannel
		trunk sql.NullInt64
		last  sql.NullTime
	)
	err := r.Scan(&c.ID, &c.Kind, &c.Provider, &c.Name, &c.Enabled, &trunk, &c.TrunkName,
		&c.HasCredentials, &c.AccountHint, &c.MessagingService, &c.Status, &c.StatusDetail, &last, &c.Addresses, &c.CreatedAt, &c.UpdatedAt)
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
	c, err := scanMsgChannel(s.db.QueryRowContext(ctx, `SELECT `+msgChannelCols+msgChannelFrom+` WHERE c.id = @id`, momo.Named("id", id)))
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
func checkChannelTrunk(ctx context.Context, tx *dbTx, in MsgChannelInput) error {
	if in.Provider != messaging.ProviderSIPTrunk {
		if in.TrunkID != nil {
			return fieldError("trunkId", "a carrier channel does not use a SIP trunk")
		}
		return nil
	}
	if in.TrunkID == nil {
		return fieldError("trunkId", "a SIP trunk channel needs a trunk")
	}
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM trunks WHERE id = @id)`, momo.Named("id", *in.TrunkID)).Scan(&ok); err != nil {
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
	err = s.tx(ctx, func(tx *dbTx) error {
		if err := checkChannelTrunk(ctx, tx, in); err != nil {
			return err
		}
		if in.Provider != messaging.ProviderSIPTrunk && in.Credentials == nil {
			return fieldError("accountSid", "a carrier channel needs its account credentials")
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO msg_channels (kind, provider, name, enabled, hook_id, trunk_id)
			VALUES (@kind, @provider, @name, @enabled, @hook_id, @trunk_id) RETURNING id`,
			momo.Named("kind", in.Kind), momo.Named("provider", in.Provider), momo.Named("name", in.Name),
			momo.Named("enabled", in.Enabled), momo.Named("hook_id", hook), momo.Named("trunk_id", in.TrunkID)).Scan(&id); err != nil {
			return err
		}
		if in.Credentials != nil {
			if err := s.sealChannelCredentials(ctx, tx, id, *in.Credentials); err != nil {
				return err
			}
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
	err := s.tx(ctx, func(tx *dbTx) error {
		var provider string
		if err := tx.QueryRowContext(ctx, `SELECT provider FROM msg_channels WHERE id = @id FOR UPDATE`, momo.Named("id", id)).Scan(&provider); err != nil {
			return err
		}
		in.Provider = provider
		if err := checkChannelTrunk(ctx, tx, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE msg_channels SET name = @name, enabled = @enabled, trunk_id = @trunk_id, updated_at = now()
			WHERE id = @id`, momo.Named("id", id), momo.Named("name", in.Name), momo.Named("enabled", in.Enabled),
			momo.Named("trunk_id", in.TrunkID)); err != nil {
			return err
		}
		if in.Credentials != nil {
			if err := s.sealChannelCredentials(ctx, tx, id, *in.Credentials); err != nil {
				return err
			}
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
	return s.tx(ctx, func(tx *dbTx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE channel_id = @id`, momo.Named("id", id)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &InUseError{Msg: fmt.Sprintf("the channel has %s; they are removed by retention or an erase request",
				plural(n, "a conversation", strconv.Itoa(n)+" conversations"))}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM msg_channels WHERE id = @id`, momo.Named("id", id))
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
	ID          int64    `json:"id"`
	ChannelID   int64    `json:"channelId"`
	ChannelName string   `json:"channelName"`
	Address     string   `json:"address"`
	Label       string   `json:"label"`
	RouteKind   string   `json:"routeKind"`
	Enabled     bool     `json:"enabled"`
	OptOutWords []string `json:"optOutKeywords"`
	HelpText    string   `json:"helpText"`
	OptOutText  string   `json:"optOutText"`
	// SendsPerMinute caps the carrier texts the number starts per minute.
	SendsPerMinute int       `json:"sendsPerMinute"`
	Conversations  int       `json:"conversations"`
	CreatedAt      time.Time `json:"createdAt"`
	// DeskPhoneDelivery delivers conversations assigned to a user who turned
	// it on to the phones of their extension (spec M-26).
	DeskPhoneDelivery bool `json:"deskPhoneDelivery"`
	// OwnerExtensionID is the extension whose phone may reply into an
	// existing conversation on this number; nil when none.
	OwnerExtensionID *int64 `json:"ownerExtensionId"`
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
	// SendsPerMinute is the throughput cap (0 keeps the stored or default).
	SendsPerMinute int
	// DeskPhoneDelivery and OwnerExtensionID: see MsgAddress.
	DeskPhoneDelivery bool
	OwnerExtensionID  *int64
}

const msgAddressCols = `a.id, a.channel_id, c.name, a.address, a.address_key, a.label, a.route_kind, a.enabled,
	to_json(a.optout_keywords)::text, a.help_text, a.optout_text, a.sends_per_minute,
	(SELECT count(*) FROM conversations v WHERE v.address_id = a.id), a.created_at,
	a.desk_phone_delivery, a.owner_extension_id`

const msgAddressFrom = ` FROM msg_addresses a JOIN msg_channels c ON c.id = a.channel_id`

func scanMsgAddress(r interface{ Scan(...any) error }) (MsgAddress, error) {
	var (
		a     MsgAddress
		words string
		owner sql.NullInt64
	)
	if err := r.Scan(&a.ID, &a.ChannelID, &a.ChannelName, &a.Address, &a.Key, &a.Label, &a.RouteKind, &a.Enabled,
		&words, &a.HelpText, &a.OptOutText, &a.SendsPerMinute, &a.Conversations, &a.CreatedAt, &a.DeskPhoneDelivery, &owner); err != nil {
		return a, err
	}
	if owner.Valid {
		a.OwnerExtensionID = &owner.Int64
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
		WHERE @channel_id::bigint = 0 OR a.channel_id = @channel_id::bigint ORDER BY lower(c.name), a.address, a.id`, momo.Named("channel_id", channelID))
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
	a, err := scanMsgAddress(s.db.QueryRowContext(ctx, `SELECT `+msgAddressCols+msgAddressFrom+` WHERE a.id = @id`, momo.Named("id", id)))
	return a, mapErr(err)
}

// CreateMsgAddress adds a number to a channel.
func (s *Store) CreateMsgAddress(ctx context.Context, actor string, in MsgAddressInput) (MsgAddress, error) {
	var id int64
	err := s.tx(ctx, func(tx *dbTx) error {
		var ok bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM msg_channels WHERE id = @id)`, momo.Named("id", in.ChannelID)).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fieldError("channelId", "no such channel")
		}
		var chat bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM msg_channels WHERE id = @id AND provider = 'chat')`, momo.Named("id", in.ChannelID)).Scan(&chat); err != nil {
			return err
		}
		if chat {
			return fieldError("channelId", "a chat site has one address, managed with the site")
		}
		if err := checkOwnerExtension(ctx, tx, in.OwnerExtensionID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO msg_addresses (channel_id, address, address_key, address_suffix, label, enabled, optout_keywords, help_text, optout_text, sends_per_minute,
				desk_phone_delivery, owner_extension_id)
			VALUES (@channel_id, @address, @address_key, @address_suffix, @label, @enabled, @optout_keywords::text[], @help_text, @optout_text,
				coalesce(NULLIF(@sends_per_minute::int, 0), 60), @desk_phone_delivery, @owner_extension_id) RETURNING id`,
			momo.Named("channel_id", in.ChannelID), momo.Named("address", in.Address), momo.Named("address_key", in.Key),
			momo.Named("address_suffix", in.Suffix), momo.Named("label", in.Label), momo.Named("enabled", in.Enabled),
			momo.Named("optout_keywords", keywordsArg(in.OptOutWords)), momo.Named("help_text", in.HelpText),
			momo.Named("optout_text", in.OptOutText), momo.Named("sends_per_minute", in.SendsPerMinute),
			momo.Named("desk_phone_delivery", in.DeskPhoneDelivery), momo.Named("owner_extension_id", ownerArg(in.OwnerExtensionID))).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "msg_address", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgAddress{}, err
	}
	return s.GetMsgAddress(ctx, id)
}

// ownerArg is the owner extension column's value.
func ownerArg(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

// checkOwnerExtension refuses an owner that is not a user extension: a door
// station or a missing extension cannot text.
func checkOwnerExtension(ctx context.Context, tx *dbTx, id *int64) error {
	if id == nil {
		return nil
	}
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM extensions WHERE id = @id`, momo.Named("id", *id)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && kind == ExtensionDoor) {
		return fieldError("ownerExtensionId", "no such extension that can text")
	}
	return err
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
	err := s.tx(ctx, func(tx *dbTx) error {
		if err := checkOwnerExtension(ctx, tx, in.OwnerExtensionID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE msg_addresses SET label = @label, enabled = @enabled, optout_keywords = @optout_keywords::text[], help_text = @help_text,
				optout_text = @optout_text, sends_per_minute = coalesce(NULLIF(@sends_per_minute::int, 0), sends_per_minute),
				desk_phone_delivery = @desk_phone_delivery, owner_extension_id = @owner_extension_id
			WHERE id = @id`, momo.Named("id", id), momo.Named("label", in.Label), momo.Named("enabled", in.Enabled),
			momo.Named("optout_keywords", keywordsArg(in.OptOutWords)), momo.Named("help_text", in.HelpText),
			momo.Named("optout_text", in.OptOutText), momo.Named("sends_per_minute", in.SendsPerMinute),
			momo.Named("desk_phone_delivery", in.DeskPhoneDelivery), momo.Named("owner_extension_id", ownerArg(in.OwnerExtensionID)))
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
	return s.tx(ctx, func(tx *dbTx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE address_id = @id`, momo.Named("id", id)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &InUseError{Msg: fmt.Sprintf("the number has %s; they are removed by retention or an erase request",
				plural(n, "a conversation", strconv.Itoa(n)+" conversations"))}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM msg_addresses WHERE id = @id`, momo.Named("id", id))
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
	RetentionSMSDays  int `json:"retentionSmsDays"`
	RetentionChatDays int `json:"retentionChatDays"`
	// RetentionMediaDays is how long an attachment's file is kept (spec
	// M-19); the message stays and shows it expired.
	RetentionMediaDays int  `json:"retentionMediaDays"`
	ReopenDays         int  `json:"reopenDays"`
	EmailNotify        bool `json:"emailNotify"`
	// FirstContact is off (default) or attested (M-17): whether an agent may
	// start a conversation with a number that has not texted first.
	FirstContact string `json:"firstContact"`
	// The first-contact rate limits: per agent per hour, per number per day.
	FirstContactPerUserHour   int `json:"firstContactPerUserHour"`
	FirstContactPerAddressDay int `json:"firstContactPerAddressDay"`
}

// GetMessagingSettings returns the settings.
func (s *Store) GetMessagingSettings(ctx context.Context) (MessagingSettings, error) {
	return getMessagingSettings(ctx, s.db)
}

func getMessagingSettings(ctx context.Context, q rowQuerier) (MessagingSettings, error) {
	var m MessagingSettings
	err := q.QueryRowContext(ctx, `SELECT retention_sms_days, retention_chat_days, reopen_days, email_notify,
		first_contact, first_contact_user_hour, first_contact_address_day, retention_media_days
		FROM messaging_settings`).Scan(&m.RetentionSMSDays, &m.RetentionChatDays, &m.ReopenDays, &m.EmailNotify,
		&m.FirstContact, &m.FirstContactPerUserHour, &m.FirstContactPerAddressDay, &m.RetentionMediaDays)
	return m, err
}

// PutMessagingSettings replaces the settings.
func (s *Store) PutMessagingSettings(ctx context.Context, actor string, m MessagingSettings) (MessagingSettings, error) {
	err := s.tx(ctx, func(tx *dbTx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE messaging_settings SET retention_sms_days = @retention_sms_days, retention_chat_days = @retention_chat_days,
				reopen_days = @reopen_days, email_notify = @email_notify, first_contact = @first_contact,
				first_contact_user_hour = @first_contact_user_hour, first_contact_address_day = @first_contact_address_day,
				retention_media_days = @retention_media_days`,
			momo.Named("retention_sms_days", m.RetentionSMSDays), momo.Named("retention_chat_days", m.RetentionChatDays),
			momo.Named("reopen_days", m.ReopenDays), momo.Named("email_notify", m.EmailNotify), momo.Named("first_contact", m.FirstContact),
			momo.Named("first_contact_user_hour", m.FirstContactPerUserHour), momo.Named("first_contact_address_day", m.FirstContactPerAddressDay),
			momo.Named("retention_media_days", m.RetentionMediaDays)); err != nil {
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
		WHERE (@address_id::bigint = 0 OR o.address_id = @address_id::bigint) AND (@all OR o.state = 'out')
		  AND (@q::text = '' OR o.peer_key LIKE '%' || @q::text)
		ORDER BY o.at DESC, o.id DESC LIMIT @limit`, momo.Named("address_id", f.AddressID), momo.Named("all", f.All),
		momo.Named("q", escapeLike(f.Q)), momo.Named("limit", limit))
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
	err = s.tx(ctx, func(tx *dbTx) error {
		var ok bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM msg_addresses WHERE id = @id)`, momo.Named("id", addressID)).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fieldError("addressId", "no such number")
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO sms_opt_outs (address_id, peer_key, peer_hash, state, source, note)
			VALUES (@address_id, @peer_key, @peer_hash, 'out', 'admin', @note)
			ON CONFLICT (address_id, peer_hash) DO UPDATE SET state = 'out', source = 'admin', note = @note, at = now(),
				peer_key = coalesce(sms_opt_outs.peer_key, @peer_key)
			RETURNING id`, momo.Named("address_id", addressID), momo.Named("peer_key", peerKey), momo.Named("peer_hash", h),
			momo.Named("note", note)).Scan(&id); err != nil {
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
		WHERE o.id = @id`, momo.Named("id", id)).Scan(&o.ID, &o.AddressID, &o.Address, &o.Channel, &o.Peer, &o.State, &o.Source, &o.Note, &o.At)
	return o, mapErr(err)
}

// Reconsent records a customer's verbal re-consent, which clears the
// opt-out. It is audited, with the note an administrator wrote.
func (s *Store) Reconsent(ctx context.Context, actor string, id int64, note string) (MsgOptOut, error) {
	err := s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sms_opt_outs SET state = 'in', source = 'admin', note = @note, at = now() WHERE id = @id`,
			momo.Named("id", id), momo.Named("note", note))
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
	// SIPMessages counts the copies in the SIP text queue and its history
	// (the parts sent to desk phones and through SIP trunks) that went too.
	SIPMessages int `json:"sipMessages"`
	// OptOutsKept counts the opt-outs reduced to their hash.
	OptOutsKept int `json:"optOutsKept"`
	// MediaObjects counts the attachment files removed from the media store
	// and checked gone; MediaObjectsPending those the store could not remove
	// now, which the media sweeper removes later.
	MediaObjects        int `json:"mediaObjects"`
	MediaObjectsPending int `json:"mediaObjectsPending"`
	// Media are the attachments whose files the caller removes (spec M-20:
	// the object deletions follow the transaction).
	Media []media.Garbage `json:"-"`
	// Providers lists, per provider, what the provider-side deletion did.
	// The carrier keeps its own logs under its own terms.
	Providers []EraseProvider `json:"providers"`
	// Batch names the provider deletions this erase queued.
	Batch string `json:"-"`
}

// EraseProvider is one provider's part of an erase or a redaction.
type EraseProvider struct {
	Provider string `json:"provider"`
	// Redacted is true when every message Hello sent through the provider
	// is gone from it (deleted, blanked, or no longer there).
	Redacted bool `json:"redacted"`
	// Messages counts the provider's messages queued for deletion; Done,
	// Pending and Failed split them. Pending ones are retried in the
	// background for a day.
	Messages int    `json:"messages"`
	Done     int    `json:"done"`
	Pending  int    `json:"pending"`
	Failed   int    `json:"failed"`
	Detail   string `json:"detail"`
}

// ErasePeer deletes every conversation, message and note of a customer
// (their number, reduced to its key) on every channel, and the copies of
// their texts in the SIP text queue, and keeps only the opt-out hash, so a
// deleted customer is not texted again. The messages Hello sent through a
// carrier API are queued for deletion at the carrier in the same
// transaction (rep.Batch); the caller runs them. The audit row holds counts
// only. Backups keep the rows until they expire.
func (s *Store) ErasePeer(ctx context.Context, actor, peerKey string) (EraseReport, error) {
	var rep EraseReport
	rep.Providers = []EraseProvider{}
	h, err := s.peerHash(peerKey)
	if err != nil {
		return rep, err
	}
	rep.Batch = newRedactionBatch("erase")
	var providers []string
	err = s.tx(ctx, func(tx *dbTx) error {
		var err error
		providers, err = queryStrings(ctx, tx, erasePeerProviders, momo.Named("peer_key", peerKey))
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM conversation_messages WHERE conversation_id IN (SELECT id FROM conversations WHERE peer_key = @peer_key)`,
			momo.Named("peer_key", peerKey)).Scan(&rep.Messages); err != nil {
			return err
		}
		// The attachments' rows stay, orphaned, until their files are gone:
		// the caller removes them now, or the media sweeper later.
		if rep.Media, err = peerMedia(ctx, tx, peerKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE message_media SET attached_at = coalesce(attached_at, now())
			WHERE id = ANY(@ids::bigint[])`, momo.Named("ids", garbageIDs(rep.Media))); err != nil {
			return err
		}
		// A public link to an erased file stops working now, not when the
		// file is gone.
		if _, err := tx.ExecContext(ctx, `DELETE FROM media_links WHERE media_id = ANY(@ids::bigint[])`, momo.Named("ids", garbageIDs(rep.Media))); err != nil {
			return err
		}
		if err := queueRedactions(ctx, tx, rep.Batch, `
			SELECT m.channel_id, ch.provider, m.provider_message_id FROM conversation_messages m
			JOIN conversations c ON c.id = m.conversation_id JOIN msg_channels ch ON ch.id = m.channel_id
			WHERE c.peer_key = $1 AND m.provider_message_id IS NOT NULL AND ch.provider <> 'sip_trunk'`, peerKey); err != nil {
			return err
		}
		// The SIP text queue holds a copy of what went to desk phones and
		// through SIP trunks, and the trunk texts of the number itself.
		res, err := tx.ExecContext(ctx, `
			DELETE FROM messages WHERE conversation_message_id IN (
				SELECT m.id FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id WHERE c.peer_key = @peer_key)
			   OR (sender_kind = 'external' AND sender = @peer_key) OR (recipient_kind = 'external' AND recipient = @peer_key)`,
			momo.Named("peer_key", peerKey))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		rep.SIPMessages = int(n)
		res, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE peer_key = @peer_key`, momo.Named("peer_key", peerKey))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		rep.Conversations = int(n)
		// A chat visitor's own record goes with their conversations.
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_visitors WHERE 'visitor:' || id::text = @peer_key`, momo.Named("peer_key", peerKey)); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `UPDATE sms_opt_outs SET peer_key = NULL, note = '' WHERE peer_hash = @peer_hash`, momo.Named("peer_hash", h))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		rep.OptOutsKept = int(n)
		return insertAudit(ctx, tx, actor, fmt.Sprintf("erase conversations=%d messages=%d sip_messages=%d", rep.Conversations, rep.Messages,
			rep.SIPMessages), "msg_peer", rep.Batch)
	})
	if err != nil {
		return rep, err
	}
	for _, p := range providers {
		rep.Providers = append(rep.Providers, EraseProvider{Provider: p})
	}
	return rep, nil
}

// erasePeerProviders is the providers of a customer's conversations, read
// through queryStrings (TestMessagingStatementsOffline builds it); it binds
// @peer_key.
const erasePeerProviders = `
			SELECT DISTINCT ch.provider FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id
			WHERE c.peer_key = @peer_key ORDER BY 1`

func garbageIDs(gs []media.Garbage) []int64 {

	out := make([]int64, len(gs))
	for i, g := range gs {
		out[i] = g.ID
	}
	return out
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
	err = s.tx(ctx, func(tx *dbTx) error {
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
				  AND coalesce(c.resolved_at, c.last_message_at) < now() - make_interval(days => CASE WHEN ch.kind = 'webchat'
					THEN coalesce((SELECT cs.retention_days FROM chat_sites cs WHERE cs.channel_id = ch.id), @chat_days::int) ELSE @sms_days::int END)
				ORDER BY c.id LIMIT @limit::int)`, momo.Named("chat_days", set.RetentionChatDays), momo.Named("sms_days", set.RetentionSMSDays),
			momo.Named("limit", purgeBatch))

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
		`DELETE FROM media_links WHERE expires_at < now() - interval '1 day'`,
	} {
		res, err := s.db.ExecContext(ctx, q)
		if err != nil {
			return out, err
		}
		n, _ := res.RowsAffected()
		out.Events += n
	}
	chat, err := s.maintainChat(ctx)
	if err != nil {
		return out, err
	}
	out.Events += chat
	if out.Conversations > 0 {
		err = s.tx(ctx, func(tx *dbTx) error {
			return insertAudit(ctx, tx, "system", fmt.Sprintf("purge conversations=%d", out.Conversations), "messaging", "-")
		})
	}
	return out, err
}
