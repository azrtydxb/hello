package store

// Messaging channels, delivery item 2 (spec messaging-channels, "outbound
// sending on Twilio"): channels that reach a carrier's web API. This file
// holds the sealed carrier credentials, the carrier text queue the send and
// status workers claim from (M-9a), first contact (M-17) and the carrier
// failure bookkeeping.
//
// A carrier text is a conversation_messages row. It is inserted queued in
// the transaction that checks the opt-out, claimed (queued to sending, with
// an attempt id) before the carrier is called, and settled by the one
// attempt that claimed it. Nothing here retries a send.
//
// Credentials are opened only to build an adapter and are never logged,
// returned or audited. Message text and numbers never reach a log or an
// audit row.

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
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/secret"
)

// Errors of first contact.
var (
	// ErrFirstContactOff: the administrator has not allowed texting a number
	// that did not text first.
	ErrFirstContactOff = errors.New("store: first contact is off")
	// ErrRateLimited: a first-contact limit is used up.
	ErrRateLimited = errors.New("store: first-contact limit reached")
)

// ConversationExistsError: the customer already has an open conversation on
// the number; the sender replies there instead.
type ConversationExistsError struct{ ID int64 }

func (e *ConversationExistsError) Error() string { return "store: an open conversation exists" }

const (
	// outboundClaimLock serialises claims of carrier texts, so the per-number
	// throughput cap holds across replicas.
	outboundClaimLock = 0x68656c6c6f6f7574 // "helloout"
	// firstContactLock serialises first-contact creation, so the rate limits
	// hold across replicas.
	firstContactLock = 0x68656c6c6f666373 // "hellofcs"
	// failStreakDegraded is how many carrier failures in a row mark a channel
	// degraded.
	failStreakDegraded = 5
	// queuedTooLong is how long a carrier text may wait unclaimed before it
	// is failed (the channel is off, or its credentials are invalid).
	queuedTooLong = time.Hour
	// maxClaimScan bounds the candidates one claim looks at.
	maxClaimScan = 200
)

func initialStatus(carrier bool) string {
	if carrier {
		return messaging.DeliveryQueued
	}
	return messaging.DeliverySending
}

// checkCarrierText validates a text for a carrier: the customer's number is
// E.164 and the body fits what the carrier takes.
func checkCarrierText(prov, peerKey, body string) error {
	if !ValidE164(peerKey) {
		return fieldError("conversation", "the customer's number is not in international format (+ and digits), so a carrier cannot text it")
	}
	if utf8.RuneCountInString(body) > carrierMaxBody(prov) {
		return fieldError("body", fmt.Sprintf("must be at most %d characters for this carrier", carrierMaxBody(prov)))
	}
	return nil
}

// carrierMaxBody is the longest text, in characters, a carrier takes in one
// message (it segments the text itself below that).
func carrierMaxBody(string) int { return messaging.MaxCarrierBody }

// ValidE164 reports whether s is + and 8 to 15 digits: a number a carrier
// can text.
func ValidE164(s string) bool {
	if len(s) < 9 || len(s) > 16 || s[0] != '+' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// sealChannelCredentials seals a carrier account into the channel's row and
// resets the channel's health: new credentials start from a clean state.
func (s *Store) sealChannelCredentials(ctx context.Context, tx *sql.Tx, channelID int64, cfg provider.Config) error {
	if s.box == nil {
		return ErrNoSecretBox
	}
	raw, err := json.Marshal(cfg) //nolint:gosec // the token is sealed below and never stored or logged in the clear
	if err != nil {
		return err
	}
	sealed, err := s.box.Seal(string(raw), secret.MsgChannelAAD(channelID))
	if err != nil {
		return err
	}
	hint := cfg.AccountSID
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	}
	fp := hex.EncodeToString(s.box.MAC("messaging-credential", cfg.AuthToken))[:12]
	_, err = tx.ExecContext(ctx, `
		UPDATE msg_channels SET credentials_enc = $2, credential_fp = $3,
			config = config || jsonb_build_object('accountHint', $4::text, 'messagingService', $5::boolean),
			status = 'ok', status_detail = '', fail_streak = 0, updated_at = now()
		WHERE id = $1`, channelID, sealed, fp, hint, cfg.MessagingServiceSID != "")
	return err
}

// ChannelProviderConfig opens a carrier channel's credentials.
func (s *Store) ChannelProviderConfig(ctx context.Context, channelID int64) (provider.Config, error) {
	var cfg provider.Config
	if s.box == nil {
		return cfg, ErrNoSecretBox
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT credentials_enc FROM msg_channels WHERE id = $1`, channelID).Scan(&sealed); err != nil {
		return cfg, mapErr(err)
	}
	if sealed == nil {
		return cfg, ErrNotFound
	}
	plain, err := s.box.Open(sealed, secret.MsgChannelAAD(channelID))
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
		return provider.Config{}, err
	}
	return cfg, nil
}

// ChannelProvider returns the provider of a channel.
func (s *Store) ChannelProvider(ctx context.Context, channelID int64) (string, error) {
	var p string
	err := s.db.QueryRowContext(ctx, `SELECT provider FROM msg_channels WHERE id = $1`, channelID).Scan(&p)
	return p, mapErr(err)
}

func newAttemptID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ClaimOutbound claims up to limit queued carrier texts for sending: each
// goes from queued to sending with a new attempt id, once, whichever replica
// asks. A number is not asked for more than its sends_per_minute in any
// minute; the rest wait queued. A text to a customer who has opted out since
// it was queued is failed here and never reaches the carrier.
func (s *Store) ClaimOutbound(ctx context.Context, limit int) ([]messaging.OutboundJob, error) {
	var jobs []messaging.OutboundJob
	err := s.tx(ctx, func(tx *sql.Tx) error {
		jobs = nil
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(outboundClaimLock)); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT cm.id, cm.conversation_id, cm.channel_id, c.address_id, c.peer_key, ch.provider, a.address, cm.body, a.sends_per_minute
			FROM conversation_messages cm
			JOIN conversations c ON c.id = cm.conversation_id
			JOIN msg_channels ch ON ch.id = cm.channel_id
			JOIN msg_addresses a ON a.id = c.address_id
			WHERE cm.status = 'queued' AND cm.direction = 'out' AND cm.kind = 'message'
			  AND ch.provider <> 'sip_trunk' AND ch.enabled AND ch.status <> 'credentials_invalid' AND a.enabled
			ORDER BY cm.id LIMIT $1 FOR UPDATE OF cm SKIP LOCKED`, maxClaimScan)
		if err != nil {
			return err
		}
		type cand struct {
			messaging.OutboundJob
			perMinute int
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.MessageID, &c.ConversationID, &c.ChannelID, &c.AddressID, &c.To, &c.Provider, &c.From, &c.Body, &c.perMinute); err != nil {
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
		used := map[int64]int{}
		for _, c := range cands {
			if len(jobs) >= limit {
				break
			}
			n, seen := used[c.AddressID]
			if !seen {
				if err := tx.QueryRowContext(ctx, `
					SELECT count(*) FROM conversation_messages x JOIN conversations xc ON xc.id = x.conversation_id
					WHERE xc.address_id = $1 AND x.claimed_at > now() - interval '1 minute'`, c.AddressID).Scan(&n); err != nil {
					return err
				}
			}
			if n >= c.perMinute {
				used[c.AddressID] = n
				continue
			}
			if !ValidE164(c.To) {
				if err := s.failQueued(ctx, tx, c.MessageID, c.ConversationID, "invalid_number"); err != nil {
					return err
				}
				continue
			}
			out, err := optedOut(ctx, tx, s, c.AddressID, c.To)
			if err != nil {
				return err
			}
			if out {
				if err := s.failQueued(ctx, tx, c.MessageID, c.ConversationID, "opted_out"); err != nil {
					return err
				}
				continue
			}
			attempt, err := newAttemptID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE conversation_messages SET status = 'sending', attempt_id = $2, claimed_at = now() WHERE id = $1`,
				c.MessageID, attempt); err != nil {
				return err
			}
			used[c.AddressID] = n + 1
			j := c.OutboundJob
			j.AttemptID = attempt
			jobs = append(jobs, j)
		}
		return nil
	})
	return jobs, err
}

// failQueued fails a queued text that never reached the carrier.
func (s *Store) failQueued(ctx context.Context, tx *sql.Tx, msgID, convID int64, code string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE conversation_messages SET status = 'failed', error_code = $2 WHERE id = $1 AND status = 'queued'`,
		msgID, code); err != nil {
		return err
	}
	return emitConversationEvent(ctx, tx, convID, msgID, "message.status", nil)
}

// FinishOutbound records what a send attempt came to. A result for an
// attempt that no longer owns the row (another claim, a sweep) changes
// nothing. An unknown outcome leaves the row sending: the sweeper settles it
// as unknown, and nothing sends it again.
func (s *Store) FinishOutbound(ctx context.Context, job messaging.OutboundJob, res messaging.OutboundResult) error {
	var kind string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		kind = ""
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM msg_channels WHERE id = $1`, job.ChannelID).Scan(&kind); err != nil {
			return err
		}
		if err := s.recordChannelHealth(ctx, tx, job.ChannelID, res); err != nil {
			return err
		}
		switch res.Outcome {
		case messaging.OutcomeSent:
			st := messaging.DeliverySent
			switch res.ProviderStatus {
			case provider.StatusDelivered:
				st = messaging.DeliveryDelivered
			case provider.StatusFailed:
				st = messaging.DeliveryFailed
			}
			final := st != messaging.DeliverySent
			r, err := tx.ExecContext(ctx, `
				UPDATE conversation_messages SET status = $3, provider_message_id = $4, sent_at = now(),
					delivered_at = CASE WHEN $3 = 'delivered' THEN now() ELSE delivered_at END,
					next_poll_at = CASE WHEN $5 THEN NULL ELSE now() + interval '5 seconds' END, poll_count = 0
				WHERE id = $1 AND attempt_id = $2 AND status = 'sending'`,
				job.MessageID, job.AttemptID, st, res.ProviderID, final)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				return nil
			}
		case messaging.OutcomeFailed:
			r, err := tx.ExecContext(ctx, `
				UPDATE conversation_messages SET status = 'failed', error_code = $3
				WHERE id = $1 AND attempt_id = $2 AND status = 'sending'`, job.MessageID, job.AttemptID, res.ErrorCode)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				return nil
			}
			if res.OptOut {
				if err := s.recordProviderOptOut(ctx, tx, job.ConversationID); err != nil {
					return err
				}
			}
		default:
			return nil
		}
		return emitConversationEvent(ctx, tx, job.ConversationID, job.MessageID, "message.status", nil)
	})
	if err != nil {
		return err
	}
	if res.Outcome == messaging.OutcomeSent || res.Outcome == messaging.OutcomeFailed {
		st := res.Outcome
		s.emit(ctx, messaging.Event{Type: "message.status", ConversationID: job.ConversationID, MessageID: job.MessageID,
			ChannelKind: kind, Direction: messaging.DirOut, Status: st})
	}
	return nil
}

// recordChannelHealth updates a channel after a carrier call: an accepted
// text clears the failure streak, refused credentials stop the channel, and
// five failures in a row mark it degraded.
func (s *Store) recordChannelHealth(ctx context.Context, tx *sql.Tx, channelID int64, res messaging.OutboundResult) error {
	switch {
	case res.BadCredentials:
		_, err := tx.ExecContext(ctx, `UPDATE msg_channels SET status = 'credentials_invalid',
			status_detail = 'the carrier refused the account login; enter the SID and token again', fail_streak = 0 WHERE id = $1`, channelID)
		return err
	case res.Outcome == messaging.OutcomeSent:
		_, err := tx.ExecContext(ctx, `UPDATE msg_channels SET fail_streak = 0,
			status = CASE WHEN status = 'degraded' THEN 'ok' ELSE status END,
			status_detail = CASE WHEN status = 'degraded' THEN '' ELSE status_detail END,
			last_event_at = now() WHERE id = $1`, channelID)
		return err
	case res.ChannelFailure:
		_, err := tx.ExecContext(ctx, `UPDATE msg_channels SET fail_streak = fail_streak + 1,
			status = CASE WHEN status = 'ok' AND fail_streak + 1 >= $2 THEN 'degraded' ELSE status END,
			status_detail = CASE WHEN status = 'ok' AND fail_streak + 1 >= $2 THEN 'the carrier failed several sends in a row' ELSE status_detail END
			WHERE id = $1`, channelID, failStreakDegraded)
		return err
	}
	return nil
}

// recordProviderOptOut records an opt-out the carrier reported (Twilio's
// 21610) for the conversation's customer, as if they had texted STOP: the
// carrier blocks them, so Hello's own list must agree.
func (s *Store) recordProviderOptOut(ctx context.Context, tx *sql.Tx, convID int64) error {
	var (
		addressID int64
		peerKey   string
		channelID int64
	)
	if err := tx.QueryRowContext(ctx, `SELECT address_id, peer_key, channel_id FROM conversations WHERE id = $1`, convID).
		Scan(&addressID, &peerKey, &channelID); err != nil {
		return err
	}
	h, err := s.peerHash(peerKey)
	if err != nil {
		return err
	}
	var was sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state FROM sms_opt_outs WHERE address_id = $1 AND peer_hash = $2`, addressID, h).Scan(&was); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sms_opt_outs (address_id, peer_key, peer_hash, state, source) VALUES ($1, $2, $3, 'out', 'provider')
		ON CONFLICT (address_id, peer_hash) DO UPDATE SET state = 'out', source = 'provider', note = '', at = now(),
			peer_key = coalesce(sms_opt_outs.peer_key, $2)`, addressID, peerKey, h); err != nil {
		return err
	}
	if was.String == "out" {
		return nil
	}
	_, err = addLine(ctx, tx, convRow{ID: convID, ChannelID: channelID, AddressID: addressID, PeerKey: peerKey},
		messaging.KindSystem, messaging.SenderSystem, 0, "", "The carrier reports the customer opted out")
	return err
}

// ClaimPolls claims up to limit sent carrier texts whose delivery status is
// due to be read. The claim pushes the next read 30 seconds out, so a second
// replica does not read the same text meanwhile; the result sets the real
// schedule. A channel with refused credentials is not polled.
func (s *Store) ClaimPolls(ctx context.Context, limit int) ([]messaging.PollJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE conversation_messages cm SET next_poll_at = now() + interval '30 seconds'
		FROM msg_channels ch
		WHERE ch.id = cm.channel_id AND cm.id IN (
			SELECT m.id FROM conversation_messages m JOIN msg_channels c ON c.id = m.channel_id
			WHERE m.next_poll_at <= now() AND m.status = 'sent' AND m.provider_message_id IS NOT NULL
			  AND c.provider <> 'sip_trunk' AND c.status <> 'credentials_invalid'
			ORDER BY m.next_poll_at LIMIT $1 FOR UPDATE OF m SKIP LOCKED)
		RETURNING cm.id, cm.conversation_id, cm.channel_id, ch.provider, cm.provider_message_id, cm.poll_count, coalesce(cm.sent_at, cm.created_at)`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []messaging.PollJob
	for rows.Next() {
		var j messaging.PollJob
		if err := rows.Scan(&j.MessageID, &j.ConversationID, &j.ChannelID, &j.Provider, &j.ProviderID, &j.Polls, &j.SentAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FinishPoll records a status read. A final state settles the message (and
// records an opt-out the carrier reports); anything else schedules the next
// read, or ends the polling when the read says to stop.
func (s *Store) FinishPoll(ctx context.Context, job messaging.PollJob, res messaging.PollResult) error {
	var changed bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		changed = false
		if res.BadCredentials {
			_, err := tx.ExecContext(ctx, `UPDATE msg_channels SET status = 'credentials_invalid',
				status_detail = 'the carrier refused the account login; enter the SID and token again' WHERE id = $1`, job.ChannelID)
			return err
		}
		switch {
		case res.Read && res.State.Final():
			st, code := messaging.DeliveryDelivered, ""
			if res.State.Status == provider.StatusFailed {
				st, code = messaging.DeliveryFailed, res.State.ErrorCode
			}
			r, err := tx.ExecContext(ctx, `
				UPDATE conversation_messages SET status = $2, error_code = $3, next_poll_at = NULL, poll_count = poll_count + 1,
					delivered_at = CASE WHEN $2 = 'delivered' THEN now() ELSE delivered_at END
				WHERE id = $1 AND status = 'sent'`, job.MessageID, st, code)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				return nil
			}
			changed = true
			if code == provider.CodeOptedOut {
				if err := s.recordProviderOptOut(ctx, tx, job.ConversationID); err != nil {
					return err
				}
			}
			return emitConversationEvent(ctx, tx, job.ConversationID, job.MessageID, "message.status", nil)
		case res.Stop:
			_, err := tx.ExecContext(ctx, `UPDATE conversation_messages SET next_poll_at = NULL WHERE id = $1 AND status = 'sent'`, job.MessageID)
			return err
		default:
			next := messaging.PollDelay(job.Polls+1, time.Since(job.SentAt))
			_, err := tx.ExecContext(ctx, `
				UPDATE conversation_messages SET poll_count = poll_count + 1, next_poll_at = now() + make_interval(secs => $2)
				WHERE id = $1 AND status = 'sent'`, job.MessageID, next.Seconds())
			return err
		}
	})
	if err == nil && changed {
		s.emit(ctx, messaging.Event{Type: "message.status", ConversationID: job.ConversationID, MessageID: job.MessageID,
			Direction: messaging.DirOut, Status: "final"})
	}
	return err
}

// SettleCarrierTexts is the carrier half of the maintenance: a text still
// sending long after its claim (the process died, or the carrier's answer
// was lost) becomes unknown, "may have been delivered", and is never sent
// again; a text still queued after an hour (the channel is off or its
// credentials are invalid) fails without having been sent. It returns the
// two counts.
func (s *Store) SettleCarrierTexts(ctx context.Context) (unknown, failed int64, err error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE conversation_messages cm SET status = 'unknown'
		WHERE cm.status = 'sending' AND cm.direction = 'out' AND cm.kind = 'message' AND cm.claimed_at IS NOT NULL
		  AND cm.claimed_at < now() - interval '5 minutes'
		  AND cm.channel_id IN (SELECT id FROM msg_channels WHERE provider <> 'sip_trunk')`)
	if err != nil {
		return 0, 0, err
	}
	unknown, _ = res.RowsAffected()
	res, err = s.db.ExecContext(ctx, `
		UPDATE conversation_messages cm SET status = 'failed', error_code = 'not_sent'
		WHERE cm.status = 'queued' AND cm.direction = 'out' AND cm.kind = 'message'
		  AND cm.created_at < now() - make_interval(secs => $1)
		  AND cm.channel_id IN (SELECT id FROM msg_channels WHERE provider <> 'sip_trunk')`, queuedTooLong.Seconds())
	if err != nil {
		return unknown, 0, err
	}
	failed, _ = res.RowsAffected()
	return unknown, failed, nil
}

// StartInput is a first-contact message: a new conversation with a number
// that did not text first.
type StartInput struct {
	AddressID int64
	// PeerKey is the customer's number, reduced to its key.
	PeerKey string
	Body    string
	// ConsentNote is what the sender attested about the customer's consent.
	ConsentNote string
}

// StartConversation starts a conversation with a customer who has not texted
// the number, and queues its first text (M-17). It needs the administrator's
// first-contact setting on (ErrFirstContactOff), refuses an opted-out number
// (ErrOptedOut) and a customer who already has an open conversation on the
// number (*ConversationExistsError), and is rate limited per agent and per
// number (ErrRateLimited). One message, one recipient, one attestation: there
// is no list form. The attestation note is stored on the conversation; the
// audit row holds ids only.
func (s *Store) StartConversation(ctx context.Context, acc InboxAccess, in StartInput) (Conversation, error) {
	var convID int64
	var sinkEvent *messaging.Event
	err := s.tx(ctx, func(tx *sql.Tx) error {
		sinkEvent = nil
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(firstContactLock)); err != nil {
			return err
		}
		set, err := getMessagingSettings(ctx, tx)
		if err != nil {
			return err
		}
		if set.FirstContact != messaging.FirstContactAttested {
			return ErrFirstContactOff
		}
		var (
			prov, kind, chStatus string
			chEnabled, adEnabled bool
			channelID            int64
		)
		if err := tx.QueryRowContext(ctx, `
			SELECT ch.id, ch.provider, ch.kind, ch.enabled, ch.status, a.enabled
			FROM msg_addresses a JOIN msg_channels ch ON ch.id = a.channel_id WHERE a.id = $1`, in.AddressID).
			Scan(&channelID, &prov, &kind, &chEnabled, &chStatus, &adEnabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fieldError("addressId", "no such number")
			}
			return err
		}
		if !chEnabled || !adEnabled || prov == messaging.ProviderSIPTrunk || !provider.Known(prov) || chStatus == "credentials_invalid" {
			return ErrChannelUnavailable
		}
		if err := checkCarrierText(prov, in.PeerKey, in.Body); err != nil {
			return err
		}
		if out, err := optedOut(ctx, tx, s, in.AddressID, in.PeerKey); err != nil {
			return err
		} else if out {
			return ErrOptedOut
		}
		var existing int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM conversations WHERE address_id = $1 AND peer_key = $2 AND status NOT IN ('resolved', 'spam')`,
			in.AddressID, in.PeerKey).Scan(&existing)
		if err == nil {
			return &ConversationExistsError{ID: existing}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var perUser, perAddress int
		if err := tx.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM conversations WHERE origin = 'outbound' AND created_by_user_id = $1 AND created_at > now() - interval '1 hour'),
			       (SELECT count(*) FROM conversations WHERE origin = 'outbound' AND address_id = $2 AND created_at > now() - interval '24 hours')`,
			acc.UserID, in.AddressID).Scan(&perUser, &perAddress); err != nil {
			return err
		}
		if perUser >= set.FirstContactPerUserHour || perAddress >= set.FirstContactPerAddressDay {
			return ErrRateLimited
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO conversations (channel_id, address_id, peer_key, peer_kind, status, assignee_user_id, origin, created_by_user_id, consent_note)
			VALUES ($1, $2, $3, 'phone', 'open', $4, 'outbound', $4, $5) RETURNING id`,
			channelID, in.AddressID, in.PeerKey, acc.UserID, in.ConsentNote).Scan(&convID); err != nil {
			return err
		}
		var msgID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, sender_user_id, sender_name, body, status)
			VALUES ($1, $2, 'out', 'message', 'user', $3, $4, $5, 'queued') RETURNING id`,
			convID, channelID, acc.UserID, acc.Name, in.Body).Scan(&msgID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET last_message_id = $2, last_message_at = now(), first_response_at = now() WHERE id = $1`,
			convID, msgID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, to_user_id, reason) VALUES ($1, $2, 'first_contact')`,
			convID, acc.UserID); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, acc.Actor, "first_contact attested", "msg_conversation", strconv.FormatInt(convID, 10)); err != nil {
			return err
		}
		if err := emitConversationEvent(ctx, tx, convID, msgID, "conversation.new", []int64{acc.UserID}); err != nil {
			return err
		}
		sinkEvent = &messaging.Event{Type: "message.sent", ConversationID: convID, MessageID: msgID, ChannelKind: kind,
			Direction: messaging.DirOut, Status: messaging.DeliveryQueued}
		return nil
	})
	if err != nil {
		return Conversation{}, err
	}
	if sinkEvent != nil {
		s.emit(ctx, *sinkEvent)
	}
	return s.conversationByID(ctx, acc.UserID, acc.All, convID)
}

// compile-time check that the store is the workers' store.
var _ messaging.WorkerStore = (*Store)(nil)
