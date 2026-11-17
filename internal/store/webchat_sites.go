package store

// The chat sites of the LAN chat widget (spec messaging-channels M-28): a
// site is a channel of kind webchat with one address, plus the widget's
// settings. Created, changed and deleted by administrators, and read, by key,
// by the chat listener. The Turnstile secret is sealed and never returned.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/secret"
)

// Chat challenge kinds.
const (
	ChallengePoW       = "pow"
	ChallengeTurnstile = "turnstile"
	ChallengeNone      = "none"
)

// ChatSite is a chat site as the administrator and the chat listener see it.
type ChatSite struct {
	ID        int64  `json:"id"`
	ChannelID int64  `json:"channelId"`
	AddressID int64  `json:"addressId"`
	Name      string `json:"name"`
	// Enabled is the site's kill switch: the channel and its address are on.
	Enabled bool `json:"enabled"`
	// Key is the public site key the embed code names.
	Key            string   `json:"siteKey"`
	AllowedOrigins []string `json:"allowedOrigins"`
	Greeting       string   `json:"greeting"`
	Accent         string   `json:"accent"`
	Position       string   `json:"position"`
	Collect        string   `json:"collect"`
	// Hours is the schedule of the site's address (the one its routing
	// shows): the widget is offline outside it.
	Hours              messaging.Schedule `json:"hours"`
	OfflineMode        string             `json:"offlineMode"`
	PrivacyURL         string             `json:"privacyUrl"`
	Challenge          string             `json:"challenge"`
	PowBits            int                `json:"powBits"`
	TurnstileSiteKey   string             `json:"turnstileSiteKey"`
	HasTurnstileSecret bool               `json:"hasTurnstileSecret"`
	StripLinks         bool               `json:"stripLinks"`
	MaxOpenUnassigned  int                `json:"maxOpenUnassigned"`
	Transcripts        bool               `json:"transcripts"`
	// RetentionDays is the site's own retention for finished chats; nil
	// follows the messaging settings.
	RetentionDays *int `json:"retentionDays"`
	// Attachments lets visitors send images and PDFs (off by default; no
	// antivirus is bundled, spec M-29a.6).
	Attachments bool `json:"attachments"`
	// Logo is whether the site has a logo; LogoType is its sniffed type and
	// LogoVersion changes with each upload (the widget's cache key).
	Logo          bool      `json:"logo"`
	LogoType      string    `json:"-"`
	LogoVersion   int64     `json:"-"`
	Conversations int       `json:"conversations"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ChatSiteInput creates or replaces a site.
type ChatSiteInput struct {
	Name              string
	Enabled           bool
	AllowedOrigins    []string
	Greeting          string
	Accent            string
	Position          string
	Collect           string
	Hours             messaging.Schedule
	OfflineMode       string
	PrivacyURL        string
	Challenge         string
	PowBits           int
	TurnstileSiteKey  string
	TurnstileSecret   *string // nil keeps the stored secret; "" removes it
	StripLinks        bool
	MaxOpenUnassigned int
	Transcripts       bool
	RetentionDays     *int
	Attachments       bool
}

const chatSiteCols = `s.id, s.channel_id, s.address_id, c.name, c.enabled AND a.enabled, s.site_key,
	to_json(s.allowed_origins)::text, s.greeting, s.accent, s.position, s.collect, a.schedule::text, s.offline_mode,
	s.privacy_url, s.challenge, s.pow_bits, s.turnstile_site_key, s.turnstile_secret_enc IS NOT NULL, s.strip_links,
	s.max_open_unassigned, s.transcripts, s.retention_days, s.attachments, s.logo_type,
	coalesce(extract(epoch FROM s.logo_updated_at)::bigint, 0),
	(SELECT count(*) FROM conversations v WHERE v.channel_id = s.channel_id), s.created_at, s.updated_at`

const chatSiteFrom = ` FROM chat_sites s JOIN msg_channels c ON c.id = s.channel_id JOIN msg_addresses a ON a.id = s.address_id`

func scanChatSite(r interface{ Scan(...any) error }) (ChatSite, error) {
	var (
		c             ChatSite
		origins, hrs  string
		retentionDays sql.NullInt64
	)
	if err := r.Scan(&c.ID, &c.ChannelID, &c.AddressID, &c.Name, &c.Enabled, &c.Key, &origins, &c.Greeting, &c.Accent,
		&c.Position, &c.Collect, &hrs, &c.OfflineMode, &c.PrivacyURL, &c.Challenge, &c.PowBits, &c.TurnstileSiteKey,
		&c.HasTurnstileSecret, &c.StripLinks, &c.MaxOpenUnassigned, &c.Transcripts, &retentionDays,
		&c.Attachments, &c.LogoType, &c.LogoVersion, &c.Conversations, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(origins), &c.AllowedOrigins); err != nil {
		return c, err
	}
	if c.AllowedOrigins == nil {
		c.AllowedOrigins = []string{}
	}
	if err := json.Unmarshal([]byte(hrs), &c.Hours); err != nil {
		return c, err
	}
	if c.Hours.Days == nil {
		c.Hours.Days = []messaging.ScheduleDay{}
	}
	if retentionDays.Valid {
		n := int(retentionDays.Int64)
		c.RetentionDays = &n
	}
	c.Logo = c.LogoType != ""
	return c, nil
}

// ListChatSites returns every site.
func (s *Store) ListChatSites(ctx context.Context) ([]ChatSite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatSiteCols+chatSiteFrom+` ORDER BY lower(c.name), s.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ChatSite{}
	for rows.Next() {
		c, err := scanChatSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChatSite returns one site.
func (s *Store) GetChatSite(ctx context.Context, id int64) (ChatSite, error) {
	c, err := scanChatSite(s.db.QueryRowContext(ctx, `SELECT `+chatSiteCols+chatSiteFrom+` WHERE s.id = @id`, momo.Named("id", id)))
	return c, mapErr(err)
}

// ChatSiteByKey returns the site a public key names; ErrNotFound for an
// unknown key. A disabled site is returned with Enabled false.
func (s *Store) ChatSiteByKey(ctx context.Context, key string) (ChatSite, error) {
	c, err := scanChatSite(s.db.QueryRowContext(ctx, `SELECT `+chatSiteCols+chatSiteFrom+` WHERE s.site_key = @site_key`, momo.Named("site_key", key)))
	return c, mapErr(err)
}

// ChatTurnstileSecret returns a site's Cloudflare Turnstile secret key, for
// the server-side verification only.
func (s *Store) ChatTurnstileSecret(ctx context.Context, siteID int64) (string, error) {
	if s.box == nil {
		return "", ErrNoSecretBox
	}
	var enc []byte
	if err := s.db.QueryRowContext(ctx, `SELECT turnstile_secret_enc FROM chat_sites WHERE id = @id`, momo.Named("id", siteID)).Scan(&enc); err != nil {
		return "", mapErr(err)
	}
	if len(enc) == 0 {
		return "", ErrNotFound
	}
	return s.box.Open(enc, secret.ChatTurnstileAAD(siteID))
}

func newSiteKey() (string, error) {
	b := make([]byte, 12) // 96 random bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (in ChatSiteInput) hoursJSON() ([]byte, error) {
	if len(in.Hours.Days) == 0 {
		return []byte(`{}`), nil
	}
	return json.Marshal(in.Hours)
}

func originsArg(o []string) []string {
	if o == nil {
		return []string{}
	}
	return o
}

// checkChatTurnstile refuses a Turnstile site with no keys.
func checkChatTurnstile(in ChatSiteInput, hasSecret bool) error {
	if in.Challenge != ChallengeTurnstile {
		return nil
	}
	if in.TurnstileSiteKey == "" {
		return fieldError("turnstileSiteKey", "is required for the turnstile challenge")
	}
	if in.TurnstileSecret == nil && !hasSecret || in.TurnstileSecret != nil && *in.TurnstileSecret == "" {
		return fieldError("turnstileSecret", "is required for the turnstile challenge")
	}
	return nil
}

func (s *Store) sealTurnstile(ctx context.Context, tx *dbTx, siteID int64, secretKey *string) error {
	if secretKey == nil {
		return nil
	}
	if *secretKey == "" {
		_, err := tx.ExecContext(ctx, `UPDATE chat_sites SET turnstile_secret_enc = NULL WHERE id = @id`, momo.Named("id", siteID))
		return err
	}
	if s.box == nil {
		return ErrNoSecretBox
	}
	sealed, err := s.box.Seal(*secretKey, secret.ChatTurnstileAAD(siteID))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat_sites SET turnstile_secret_enc = @sealed WHERE id = @id`,
		momo.Named("id", siteID), momo.Named("sealed", sealed))
	return err
}

// CreateChatSite adds a site: its channel (kind webchat, no carrier), its
// one address (the site key) and its settings, in one transaction.
func (s *Store) CreateChatSite(ctx context.Context, actor string, in ChatSiteInput) (ChatSite, error) {
	hook, err := newHookID()
	if err != nil {
		return ChatSite{}, err
	}
	key, err := newSiteKey()
	if err != nil {
		return ChatSite{}, err
	}
	hours, err := in.hoursJSON()
	if err != nil {
		return ChatSite{}, err
	}
	if err := checkChatTurnstile(in, false); err != nil {
		return ChatSite{}, err
	}
	var id int64
	err = s.tx(ctx, func(tx *dbTx) error {
		var channelID, addressID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO msg_channels (kind, provider, name, enabled, hook_id)
			VALUES ('webchat', 'chat', @name, @enabled, @hook_id) RETURNING id`,
			momo.Named("name", in.Name), momo.Named("enabled", in.Enabled), momo.Named("hook_id", hook)).Scan(&channelID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO msg_addresses (channel_id, address, address_key, label, enabled, route_kind, schedule)
			VALUES (@channel_id, @address, @address, @label, @enabled, 'inbox', @schedule::jsonb) RETURNING id`,
			momo.Named("channel_id", channelID), momo.Named("address", key), momo.Named("label", in.Name),
			momo.Named("enabled", in.Enabled), momo.Named("schedule", string(hours))).Scan(&addressID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO chat_sites (channel_id, address_id, site_key, allowed_origins, greeting, accent, position, collect,
				offline_mode, privacy_url, challenge, pow_bits, turnstile_site_key, strip_links, max_open_unassigned,
				transcripts, retention_days, attachments)
			VALUES (@channel_id, @address_id, @site_key, @allowed_origins::text[], @greeting, @accent, @position, @collect,
				@offline_mode, @privacy_url, @challenge, @pow_bits, @turnstile_site_key, @strip_links, @max_open_unassigned,
				@transcripts, @retention_days, @attachments) RETURNING id`,
			momo.Named("channel_id", channelID), momo.Named("address_id", addressID), momo.Named("site_key", key),
			momo.Named("allowed_origins", originsArg(in.AllowedOrigins)), momo.Named("greeting", in.Greeting),
			momo.Named("accent", in.Accent), momo.Named("position", in.Position), momo.Named("collect", in.Collect),
			momo.Named("offline_mode", in.OfflineMode), momo.Named("privacy_url", in.PrivacyURL),
			momo.Named("challenge", in.Challenge), momo.Named("pow_bits", in.PowBits),
			momo.Named("turnstile_site_key", in.TurnstileSiteKey), momo.Named("strip_links", in.StripLinks),
			momo.Named("max_open_unassigned", in.MaxOpenUnassigned), momo.Named("transcripts", in.Transcripts),
			momo.Named("retention_days", in.RetentionDays), momo.Named("attachments", in.Attachments)).Scan(&id); err != nil {
			return err
		}
		if err := s.sealTurnstile(ctx, tx, id, in.TurnstileSecret); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "chat_site", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return ChatSite{}, err
	}
	return s.GetChatSite(ctx, id)
}

// UpdateChatSite replaces a site's settings; its key stays.
func (s *Store) UpdateChatSite(ctx context.Context, actor string, id int64, in ChatSiteInput) (ChatSite, error) {
	hours, err := in.hoursJSON()
	if err != nil {
		return ChatSite{}, err
	}
	err = s.tx(ctx, func(tx *dbTx) error {
		var channelID, addressID int64
		var hasSecret bool
		if err := tx.QueryRowContext(ctx, `SELECT channel_id, address_id, turnstile_secret_enc IS NOT NULL FROM chat_sites WHERE id = @id FOR UPDATE`,
			momo.Named("id", id)).Scan(&channelID, &addressID, &hasSecret); err != nil {
			return err
		}
		if err := checkChatTurnstile(in, hasSecret); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE msg_channels SET name = @name, enabled = @enabled, updated_at = now() WHERE id = @id`,
			momo.Named("id", channelID), momo.Named("name", in.Name), momo.Named("enabled", in.Enabled)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE msg_addresses SET label = @label, enabled = @enabled, schedule = @schedule::jsonb WHERE id = @id`,
			momo.Named("id", addressID), momo.Named("label", in.Name), momo.Named("enabled", in.Enabled),
			momo.Named("schedule", string(hours))); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_sites SET allowed_origins = @allowed_origins::text[], greeting = @greeting, accent = @accent,
				position = @position, collect = @collect, offline_mode = @offline_mode, privacy_url = @privacy_url,
				challenge = @challenge, pow_bits = @pow_bits, turnstile_site_key = @turnstile_site_key,
				strip_links = @strip_links, max_open_unassigned = @max_open_unassigned, transcripts = @transcripts,
				retention_days = @retention_days, attachments = @attachments, updated_at = now()
			WHERE id = @id`,
			momo.Named("id", id), momo.Named("allowed_origins", originsArg(in.AllowedOrigins)),
			momo.Named("greeting", in.Greeting), momo.Named("accent", in.Accent), momo.Named("position", in.Position),
			momo.Named("collect", in.Collect), momo.Named("offline_mode", in.OfflineMode),
			momo.Named("privacy_url", in.PrivacyURL), momo.Named("challenge", in.Challenge),
			momo.Named("pow_bits", in.PowBits), momo.Named("turnstile_site_key", in.TurnstileSiteKey),
			momo.Named("strip_links", in.StripLinks), momo.Named("max_open_unassigned", in.MaxOpenUnassigned),
			momo.Named("transcripts", in.Transcripts), momo.Named("retention_days", in.RetentionDays),
			momo.Named("attachments", in.Attachments)); err != nil {
			return err
		}
		if err := s.sealTurnstile(ctx, tx, id, in.TurnstileSecret); err != nil {
			return err
		}
		action := "update"
		if !in.Enabled {
			action = "disable"
		}
		return insertAudit(ctx, tx, actor, action, "chat_site", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return ChatSite{}, err
	}
	return s.GetChatSite(ctx, id)
}

// DeleteChatSite removes a site that has no conversations; one with history
// is refused (retention or an erase request removes it first).
func (s *Store) DeleteChatSite(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *dbTx) error {
		var channelID int64
		if err := tx.QueryRowContext(ctx, `SELECT channel_id FROM chat_sites WHERE id = @id FOR UPDATE`, momo.Named("id", id)).Scan(&channelID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE channel_id = @channel_id`, momo.Named("channel_id", channelID)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &InUseError{Msg: fmt.Sprintf("the site has %s; they are removed by retention or an erase request",
				plural(n, "a conversation", strconv.Itoa(n)+" conversations"))}
		}
		// The channel's delete cascades to the address and the site.
		if _, err := tx.ExecContext(ctx, `DELETE FROM msg_channels WHERE id = @id`, momo.Named("id", channelID)); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "chat_site", strconv.FormatInt(id, 10))
	})
}

// ChatBlock is a block on a visitor of a site.
type ChatBlock struct {
	ID        int64     `json:"id"`
	SiteID    int64     `json:"siteId"`
	VisitorID *int64    `json:"visitorId"`
	ByIP      bool      `json:"byIp"`
	Until     time.Time `json:"until"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListChatBlocks returns a site's blocks that still hold.
func (s *Store) ListChatBlocks(ctx context.Context, siteID int64) ([]ChatBlock, error) {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM chat_sites WHERE id = @id`, momo.Named("id", siteID)).Scan(&one); err != nil {
		return nil, mapErr(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, site_id, visitor_id, ip_hash IS NOT NULL, until, created_at FROM chat_blocks
		WHERE site_id = @site_id AND until > now() ORDER BY id DESC LIMIT 500`, momo.Named("site_id", siteID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ChatBlock{}
	for rows.Next() {
		var (
			b ChatBlock
			v sql.NullInt64
		)
		if err := rows.Scan(&b.ID, &b.SiteID, &v, &b.ByIP, &b.Until, &b.CreatedAt); err != nil {
			return nil, err
		}
		if v.Valid {
			b.VisitorID = &v.Int64
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteChatBlock lifts a block.
func (s *Store) DeleteChatBlock(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM chat_blocks WHERE id = @id`, momo.Named("id", id))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "unblock", "chat_block", strconv.FormatInt(id, 10))
	})
}
