package store

// The inbox AI (spec messaging-channels M-30, M-30a): the runs the ingest
// queues for each customer text, what the worker reads and writes, the FAQ
// an auto-reply answers from, the settings and kill switches, and what the
// inbox shows of it.
//
// Message text is customer data: nothing here logs it, and the audit rows
// carry ids only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/msgai"
)

// ---------------------------------------------------------------------------
// What the inbox shows.

// ConvAI is what the inbox AI made of a conversation.
type ConvAI struct {
	// Mode is the number's AI mode: off, triage, draft or auto.
	Mode string `json:"mode"`
	// Triage is the last triage applied; nil when none.
	Triage *ConvTriage `json:"triage"`
	// Applied is what the triage changed, which RevertConversationAI undoes.
	Applied ConvAIApplied `json:"applied"`
	// Spam is the triage's spam flag, for supervisors; it deletes nothing.
	Spam bool `json:"spam"`
	// Handoff is true while the AI asked for a person and nobody replied
	// since; HandoffReason names why.
	Handoff       bool   `json:"handoff"`
	HandoffReason string `json:"handoffReason"`
	// Draft is the suggested reply to the latest customer text, nil when
	// there is none; set when one conversation is read.
	Draft *ConvDraft `json:"draft"`
	// Pending is true while a run for the conversation is queued or running;
	// set when one conversation is read.
	Pending bool `json:"pending"`
}

// ConvTriage is a triage as the inbox shows it.
type ConvTriage struct {
	msgai.Triage
	// MessageID is the customer text it read.
	MessageID int64 `json:"messageId"`
	// Reverted is true once an agent undid what it changed.
	Reverted bool `json:"reverted"`
}

// ConvAIApplied is what a triage changed.
type ConvAIApplied struct {
	// Tags are the tags the AI added.
	Tags []string `json:"tags"`
	// Priority is the priority before the AI raised it; "" when it did not.
	Priority string `json:"priority"`
}

// ConvDraft is the reply the AI suggests for the latest customer text.
type ConvDraft struct {
	RunID     int64     `json:"runId"`
	MessageID int64     `json:"messageId"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

func (a *ConvAI) decode(triage, applied string) error {
	if triage != "" {
		var t ConvTriage
		if err := json.Unmarshal([]byte(triage), &t); err != nil {
			return err
		}
		if t.SuggestedTags == nil {
			t.SuggestedTags = []string{}
		}
		a.Triage = &t
	}
	if applied != "" {
		if err := json.Unmarshal([]byte(applied), &a.Applied); err != nil {
			return err
		}
	}
	if a.Applied.Tags == nil {
		a.Applied.Tags = []string{}
	}
	return nil
}

// convDraft returns the conversation's draft that is still current: for the
// latest customer text, with no reply after it. It also reports whether a
// run is pending.
func (s *Store) convDraft(ctx context.Context, convID int64) (*ConvDraft, bool, error) {
	var pending bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM ai_message_runs WHERE conversation_id = $1 AND state IN ('queued', 'running'))`,
		convID).Scan(&pending); err != nil {
		return nil, false, err
	}
	var (
		d   ConvDraft
		raw string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT r.id, r.message_id, r.result::text, coalesce(r.finished_at, r.created_at) FROM ai_message_runs r
		WHERE r.conversation_id = $1 AND r.kind = 'draft' AND r.state = 'done' AND r.result IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM conversation_messages x WHERE x.conversation_id = r.conversation_id AND x.id > r.message_id
		                  AND x.kind = 'message' AND (x.direction = 'in' OR x.sender_kind IN ('user', 'ai')))
		ORDER BY r.id DESC LIMIT 1`, convID).Scan(&d.RunID, &d.MessageID, &raw, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, pending, nil
	}
	if err != nil {
		return nil, pending, err
	}
	var dr msgai.Draft
	if err := json.Unmarshal([]byte(raw), &dr); err != nil || dr.Reply == "" {
		return nil, pending, nil
	}
	d.Text = dr.Reply
	return &d, pending, nil
}

// RevertConversationAI undoes what the AI triage changed on a conversation
// the caller reaches: the tags it added (those still there), the priority it
// raised (while still high), and the spam flag. The triage stays visible,
// marked reverted.
func (s *Store) RevertConversationAI(ctx context.Context, acc InboxAccess, convID int64) (Conversation, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := lockConversation(ctx, tx, acc, convID)
		if err != nil {
			return err
		}
		var (
			tagsJSON, priority, appliedJSON string
			spam                            bool
		)
		if err := tx.QueryRowContext(ctx, `SELECT to_json(tags)::text, priority, ai_applied::text, ai_spam FROM conversations WHERE id = $1`,
			c.ID).Scan(&tagsJSON, &priority, &appliedJSON, &spam); err != nil {
			return err
		}
		var tags []string
		var applied ConvAIApplied
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(appliedJSON), &applied); err != nil {
			return err
		}
		if len(applied.Tags) == 0 && applied.Priority == "" && !spam {
			return nil // nothing to undo
		}
		kept := []string{}
		for _, t := range tags {
			if !slices.Contains(applied.Tags, t) {
				kept = append(kept, t)
			}
		}
		if applied.Priority != "" && priority == "high" {
			priority = applied.Priority
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE conversations SET tags = $2, priority = $3, ai_spam = FALSE, ai_applied = '{}',
				ai_triage = CASE WHEN ai_triage IS NULL THEN NULL ELSE jsonb_set(ai_triage, '{reverted}', 'true') END
			WHERE id = $1`, c.ID, kept, priority); err != nil {
			return err
		}
		if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", acc.Name+" undid the AI triage's suggestions"); err != nil {
			return err
		}
		return emitConversationEvent(ctx, tx, c.ID, 0, "conversation.updated", nil)
	})
	if err != nil {
		return Conversation{}, err
	}
	return s.conversationByID(ctx, acc.UserID, acc.All, convID)
}

// ---------------------------------------------------------------------------
// Queueing.

// queueAITriage queues the triage of a customer text in the ingest
// transaction, when the number's AI is on and no kill switch is set. The
// worker does the rest; a model that fails or is slow never holds up the
// text (spec M-30).
func queueAITriage(ctx context.Context, tx *sql.Tx, convID, addressID, msgID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ai_message_runs (message_id, conversation_id, kind)
		SELECT $1::bigint, $2::bigint, 'triage' FROM msg_addresses a JOIN msg_channels ch ON ch.id = a.channel_id, messaging_settings s
		WHERE a.id = $3 AND a.ai_mode <> 'off' AND ch.ai_enabled AND s.ai_enabled
		ON CONFLICT (message_id, kind) DO NOTHING`, msgID, convID, addressID)
	return err
}

func queueAIRun(ctx context.Context, tx *sql.Tx, convID, msgID int64, kind string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ai_message_runs (message_id, conversation_id, kind) VALUES ($1, $2, $3)
		ON CONFLICT (message_id, kind) DO NOTHING`, msgID, convID, kind)
	return err
}

// ---------------------------------------------------------------------------
// The worker's persistence.

// MessageAI returns the store as the inbox AI worker's persistence.
func (s *Store) MessageAI() msgai.Store { return aiStore{s} }

type aiStore struct{ s *Store }

const (
	// aiClaimTimeout is when a running run whose pod died is claimed again.
	aiClaimTimeout = 2 * time.Minute
	// aiMaxAttempts is how often a run is claimed before it is given up.
	aiMaxAttempts = 3
)

// ClaimAIRun takes the oldest queued run, or one whose pod died.
func (as aiStore) ClaimAIRun(ctx context.Context) (msgai.Job, bool, error) {
	var j msgai.Job
	secs := aiClaimTimeout.Seconds()
	if _, err := as.s.db.ExecContext(ctx, `
		UPDATE ai_message_runs SET state = 'failed', error = 'abandoned', finished_at = now()
		WHERE id IN (SELECT id FROM ai_message_runs WHERE state = 'running' AND claimed_at < now() - make_interval(secs => $1)
		             AND attempts >= $2 FOR UPDATE SKIP LOCKED)`, secs, aiMaxAttempts); err != nil {
		return j, false, err
	}
	err := as.s.db.QueryRowContext(ctx, `
		UPDATE ai_message_runs SET state = 'running', claimed_at = now(), attempts = attempts + 1
		WHERE id = (SELECT id FROM ai_message_runs
		            WHERE state = 'queued' OR (state = 'running' AND claimed_at < now() - make_interval(secs => $1))
		            ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, message_id, conversation_id, kind`, secs).Scan(&j.RunID, &j.MessageID, &j.ConversationID, &j.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// aiFresh is the condition "no newer customer text and no reply by a person
// or the AI follows message $2 in conversation $1".
const aiFresh = `NOT EXISTS (SELECT 1 FROM conversation_messages fx WHERE fx.conversation_id = $1 AND fx.id > $2
	AND fx.kind = 'message' AND (fx.direction = 'in' OR fx.sender_kind IN ('user', 'ai')))`

// AIRunContext reads what a run needs.
func (as aiStore) AIRunContext(ctx context.Context, job msgai.Job) (msgai.Context, error) {
	return as.s.loadAIContext(ctx, as.s.db, job)
}

func jsonList(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *Store) loadAIContext(ctx context.Context, q querier, job msgai.Job) (msgai.Context, error) {
	c := msgai.Context{Job: job}
	var (
		addressID                    int64
		peerKey, peerName, peerSrc   string
		lastIn                       sql.NullTime
		autoIntents, handoff, optout string
		intents, sensitive           string
		minConf                      float64
		assigned                     bool
	)
	err := q.QueryRowContext(ctx, `
		SELECT c.address_id, c.peer_key, c.peer_name, c.peer_name_source, c.ai_spam, c.assignee_user_id IS NOT NULL, c.last_inbound_at,
		       ch.kind, ch.ai_enabled AND ch.enabled AND a.enabled,
		       a.ai_mode, a.ai_redact, a.ai_disclosure, a.ai_disclosure_text, a.ai_holding_text, a.ai_max_replies, a.ai_hourly_cap,
		       round(a.ai_min_confidence::numeric, 2)::float8, to_json(a.ai_auto_intents)::text, to_json(a.ai_handoff_words)::text, to_json(a.optout_keywords)::text,
		       s.ai_enabled, s.ai_auto_enabled, s.ai_business_name, to_json(s.ai_intents)::text, to_json(s.ai_sensitive_topics)::text,
		       m.body
		FROM conversations c
		JOIN msg_channels ch ON ch.id = c.channel_id
		JOIN msg_addresses a ON a.id = c.address_id
		JOIN conversation_messages m ON m.id = $2 AND m.conversation_id = c.id,
		messaging_settings s
		WHERE c.id = $1`, job.ConversationID, job.MessageID).Scan(
		&addressID, &peerKey, &peerName, &peerSrc, &c.Spam, &assigned, &lastIn,
		&c.ChannelKind, &c.ChannelOn,
		&c.Address.Mode, &c.Address.Redact, &c.Address.Disclosure, &c.Address.DisclosureText, &c.Address.HoldingText,
		&c.Address.MaxReplies, &c.Address.HourlyCap, &minConf, &autoIntents, &handoff, &optout,
		&c.Settings.Enabled, &c.Settings.AutoEnabled, &c.Settings.BusinessName, &intents, &sensitive,
		&c.Body)
	if err != nil {
		return c, mapErr(err)
	}
	c.ConversationAssigned = assigned
	c.Address.MinConfidence = minConf
	c.Address.AutoIntents, c.Address.HandoffWords, c.Address.OptOutWords = jsonList(autoIntents), jsonList(handoff), jsonList(optout)
	c.Settings.Intents, c.Settings.SensitiveTopics = jsonList(intents), jsonList(sensitive)
	if lastIn.Valid {
		t := lastIn.Time
		c.LastInboundAt = &t
	}
	// Only a name from a shared source, and only the first word of it.
	if peerSrc != "" && peerName != "" {
		c.FirstName = strings.Fields(peerName)[0]
	}
	if err := q.QueryRowContext(ctx, `SELECT `+aiFresh, job.ConversationID, job.MessageID).Scan(&c.Fresh); err != nil {
		return c, err
	}

	// The conversation up to the message: its last texts, oldest first.
	rows, err := q.QueryContext(ctx, `
		SELECT direction, sender_kind, body FROM conversation_messages
		WHERE conversation_id = $1 AND id <= $2 AND kind = 'message'
		ORDER BY id DESC LIMIT $3`, job.ConversationID, job.MessageID, msgai.MaxContextMessages)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var dir, sender, body string
		if err := rows.Scan(&dir, &sender, &body); err != nil {
			_ = rows.Close()
			return c, err
		}
		from := "agent"
		switch {
		case dir == messaging.DirIn:
			from = "customer"
		case sender == messaging.SenderAI:
			from = "assistant"
		}
		c.Lines = append(c.Lines, msgai.Line{From: from, Text: body})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}
	slices.Reverse(c.Lines)

	if c.FAQ, err = listFAQ(ctx, q, addressID, true); err != nil {
		return c, err
	}
	qrows, err := q.QueryContext(ctx, `SELECT name FROM queues WHERE enabled AND text_enabled ORDER BY name LIMIT 50`)
	if err != nil {
		return c, err
	}
	for qrows.Next() {
		var n string
		if err := qrows.Scan(&n); err != nil {
			_ = qrows.Close()
			return c, err
		}
		c.Queues = append(c.Queues, n)
	}
	_ = qrows.Close()
	if err := qrows.Err(); err != nil {
		return c, err
	}

	if job.Kind != msgai.KindTriage {
		var raw string
		err := q.QueryRowContext(ctx, `SELECT result::text FROM ai_message_runs WHERE message_id = $1 AND kind = 'triage' AND state = 'done' AND result IS NOT NULL`,
			job.MessageID).Scan(&raw)
		if err == nil {
			var t msgai.Triage
			if json.Unmarshal([]byte(raw), &t) == nil {
				c.Triage = &t
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return c, err
		}
	}

	// The guardrails' facts (spec M-30a).
	h, err := s.peerHash(peerKey)
	if err != nil {
		return c, err
	}
	var lastHuman sql.NullTime
	err = q.QueryRowContext(ctx, `
		WITH h AS (SELECT max(id) AS id, max(created_at) AS at FROM conversation_messages
		           WHERE conversation_id = $1 AND direction = 'out' AND kind = 'message' AND sender_kind = 'user')
		SELECT EXISTS (SELECT 1 FROM sms_opt_outs WHERE address_id = $2 AND peer_hash = $3 AND state = 'out'),
		       (SELECT at FROM h),
		       (SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'ai' AND kind = 'message'
		            AND id > coalesce((SELECT id FROM h), 0)),
		       (SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'ai' AND kind = 'message'),
		       (SELECT count(*) FROM conversation_messages am JOIN conversations ac ON ac.id = am.conversation_id
		            WHERE am.sender_kind = 'ai' AND am.created_at > now() - interval '1 hour' AND ac.address_id = $2),
		       EXISTS (SELECT 1 FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'system' AND ai_run_id IS NOT NULL
		            AND id > coalesce((SELECT id FROM h), 0))`,
		job.ConversationID, addressID, h).Scan(&c.OptedOut, &lastHuman, &c.AIRepliesSinceHuman, &c.AIRepliesTotal, &c.AISentLastHour, &c.HoldingSinceHuman)
	if err != nil {
		return c, err
	}
	if lastHuman.Valid {
		t := lastHuman.Time
		c.LastHumanReplyAt = &t
	}
	return c, nil
}

// draftAfter reports whether a run that did not send a reply should leave a
// draft for the agent instead.
func draftAfter(d *msgai.Decision) bool {
	if d == nil || d.Send {
		return false
	}
	switch d.Skip {
	case "", msgai.ReasonHumanActive, msgai.ReasonOff:
		// A person answers, or auto-replies are switched off: the agent
		// gets a draft (the draft run checks the switches of its own).
		return true
	}
	return false
}

// FinishAIRun stores a run's result and applies it while it is still
// current.
func (as aiStore) FinishAIRun(ctx context.Context, job msgai.Job, res msgai.Result) error {
	s := as.s
	return s.tx(ctx, func(tx *sql.Tx) error {
		var (
			state    string
			redacted bool
		)
		// A text of the conversation the run may have read that was
		// redacted since the run was queued makes the result stale: it may
		// hold some of the text. redactAIOutput marks a running run stale
		// and locks the queued ones; this also covers a run queued while
		// the redaction was committing (redacted_at is when the redaction
		// began, hence the margin).
		err := tx.QueryRowContext(ctx, `
			SELECT r.state, EXISTS (SELECT 1 FROM conversation_messages x WHERE x.conversation_id = r.conversation_id
				AND x.id <= r.message_id AND x.redacted_at > r.created_at - interval '1 minute')
			FROM ai_message_runs r WHERE r.id = $1 FOR UPDATE OF r`, job.RunID).Scan(&state, &redacted)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // erased with its conversation
		}
		if err != nil {
			return err
		}
		if state != msgai.StateRunning {
			return nil // given up meanwhile
		}
		if redacted {
			_, err := tx.ExecContext(ctx, `UPDATE ai_message_runs SET state = 'stale', result = NULL, error = 'redacted', finished_at = now()
				WHERE id = $1`, job.RunID)
			return err
		}
		var c convRow
		var mode string
		err = tx.QueryRowContext(ctx, `
			SELECT c.id, c.channel_id, c.address_id, c.peer_key, c.status, c.assignee_user_id, a.ai_mode
			FROM conversations c JOIN msg_addresses a ON a.id = c.address_id WHERE c.id = $1 FOR UPDATE OF c`,
			job.ConversationID).Scan(&c.ID, &c.ChannelID, &c.AddressID, &c.PeerKey, &c.Status, &c.Assignee, &mode)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var fresh bool
		if err := tx.QueryRowContext(ctx, `SELECT `+aiFresh, job.ConversationID, job.MessageID).Scan(&fresh); err != nil {
			return err
		}
		var result any
		switch {
		case res.Triage != nil:
			result = res.Triage
		case res.Draft != nil:
			result = res.Draft
		case res.Reply != nil:
			result = res.Reply
		}
		if !fresh && (res.State == msgai.StateDone || res.State == msgai.StateFailed) {
			res.State = msgai.StateStale
		}

		switch job.Kind {
		case msgai.KindTriage:
			if res.State == msgai.StateDone && res.Triage != nil {
				if err := applyTriage(ctx, tx, c.ID, job.MessageID, *res.Triage); err != nil {
					return err
				}
			}
			if res.State == msgai.StateDone || res.State == msgai.StateFailed {
				switch mode {
				case msgai.ModeDraft:
					err = queueAIRun(ctx, tx, c.ID, job.MessageID, msgai.KindDraft)
				case msgai.ModeAuto:
					err = queueAIRun(ctx, tx, c.ID, job.MessageID, msgai.KindAutoReply)
				}
				if err != nil {
					return err
				}
			}
		case msgai.KindDraft:
			if res.State == msgai.StateDone && res.Draft != nil {
				if err := emitConversationEvent(ctx, tx, c.ID, 0, "conversation.updated", nil); err != nil {
					return err
				}
			}
		case msgai.KindAutoReply:
			if res.Reply != nil && res.State != msgai.StateStale {
				d, err := s.decideAutoReply(ctx, tx, job, *res.Reply)
				if err != nil {
					return err
				}
				res.Reply, result = &d, &d
				if err := s.applyAutoReply(ctx, tx, c, job, d); err != nil {
					return err
				}
				if draftAfter(&d) && (mode == msgai.ModeDraft || mode == msgai.ModeAuto) {
					if err := queueAIRun(ctx, tx, c.ID, job.MessageID, msgai.KindDraft); err != nil {
						return err
					}
				}
			}
		}
		var raw any
		if result != nil && res.State != msgai.StateStale {
			b, err := json.Marshal(result)
			if err != nil {
				return err
			}
			raw = string(b)
		}
		_, err = tx.ExecContext(ctx, `UPDATE ai_message_runs SET state = $2, result = $3::jsonb, error = $4, finished_at = now() WHERE id = $1`,
			job.RunID, res.State, raw, res.Error)
		return err
	})
}

// applyTriage records a triage on its conversation and applies its
// suggestions with the AI as actor (spec M-30): its tags are added, an
// urgent text raises the priority to high, spam sets the flag. It never
// assigns or moves the conversation; the router reads the suggested queue
// for a number that routes through the AI.
func applyTriage(ctx context.Context, tx *sql.Tx, convID, msgID int64, t msgai.Triage) error {
	var tagsJSON, priority, appliedJSON string
	if err := tx.QueryRowContext(ctx, `SELECT to_json(tags)::text, priority, ai_applied::text FROM conversations WHERE id = $1`,
		convID).Scan(&tagsJSON, &priority, &appliedJSON); err != nil {
		return err
	}
	var tags []string
	var applied ConvAIApplied
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(appliedJSON), &applied); err != nil {
		return err
	}
	if tags == nil {
		tags = []string{}
	}
	for _, tag := range t.SuggestedTags {
		if len(tags) >= messaging.MaxTags {
			break
		}
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
			if !slices.Contains(applied.Tags, tag) {
				applied.Tags = append(applied.Tags, tag)
			}
		}
	}
	if t.Urgency == msgai.UrgencyHigh && priority != "high" {
		if applied.Priority == "" {
			applied.Priority = priority
		}
		priority = "high"
	}
	if applied.Tags == nil {
		applied.Tags = []string{}
	}
	ab, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	tb, err := json.Marshal(ConvTriage{Triage: t, MessageID: msgID})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET tags = $2, priority = $3, ai_spam = ai_spam OR $4, ai_triage = $5::jsonb, ai_applied = $6::jsonb
		WHERE id = $1`, convID, tags, priority, t.Spam, string(tb), string(ab)); err != nil {
		return err
	}
	return emitConversationEvent(ctx, tx, convID, 0, "conversation.updated", nil)
}

// decideAutoReply checks the guardrails again in the transaction that sends
// (spec M-30a): a person who replied, a customer who opted out, the reply
// caps and the switches are read again, so a decision taken before the
// model call never sends past them.
func (s *Store) decideAutoReply(ctx context.Context, tx *sql.Tx, job msgai.Job, d msgai.Decision) (msgai.Decision, error) {
	if d.Skip != "" || (!d.Send && !d.Handoff) {
		return d, nil
	}
	c, err := s.loadAIContext(ctx, tx, job)
	if err != nil {
		return d, err
	}
	again, ok := msgai.PreCheck(c, true, time.Now())
	switch {
	case !ok && again.Skip != "":
		if again.Skip == msgai.ReasonOff && d.Handoff {
			// A switch went off: a person answers, as for an AI failure.
			return d, nil
		}
		return again, nil
	case !ok && d.Send:
		return again, nil
	}
	return d, nil
}

// applyAutoReply sends an auto-reply, or hands the conversation to a person
// with the holding line (once until a person replies).
func (s *Store) applyAutoReply(ctx context.Context, tx *sql.Tx, c convRow, job msgai.Job, d msgai.Decision) error {
	if d.Send {
		id, sent, err := s.autoText(ctx, tx, c, d.Text, "AI reply", messaging.SenderAI, job.RunID)
		if err != nil {
			return err
		}
		if sent {
			if err := insertAudit(ctx, tx, "system:ai", "ai_autoreply", "conversation_message", strconv.FormatInt(id, 10)); err != nil {
				return err
			}
			return emitConversationEvent(ctx, tx, c.ID, id, "message.sent", nil)
		}
		d = msgai.Decision{Handoff: true, Reason: msgai.ReasonError}
	}
	if !d.Handoff {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET ai_handoff_at = now(), ai_handoff_reason = $2 WHERE id = $1`,
		c.ID, d.Reason); err != nil {
		return err
	}
	if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "",
		"The AI handed the conversation to a person: "+msgai.ReasonText(d.Reason)); err != nil {
		return err
	}
	var holding string
	var sentBefore bool
	if err := tx.QueryRowContext(ctx, `
		SELECT a.ai_holding_text, EXISTS (SELECT 1 FROM conversation_messages m WHERE m.conversation_id = $1 AND m.sender_kind = 'system'
			AND m.ai_run_id IS NOT NULL AND m.id > coalesce((SELECT max(id) FROM conversation_messages u WHERE u.conversation_id = $1
				AND u.direction = 'out' AND u.kind = 'message' AND u.sender_kind = 'user'), 0))
		FROM msg_addresses a WHERE a.id = $2`, c.ID, c.AddressID).Scan(&holding, &sentBefore); err != nil {
		return err
	}
	if strings.TrimSpace(holding) != "" && !sentBefore {
		if _, _, err := s.autoText(ctx, tx, c, holding, "AI holding line", messaging.SenderSystem, job.RunID); err != nil {
			return err
		}
	}
	return emitConversationEvent(ctx, tx, c.ID, 0, "conversation.updated", nil)
}

// ---------------------------------------------------------------------------
// The FAQ.

// MsgFAQ is one FAQ snippet of a number.
type MsgFAQ struct {
	ID        int64     `json:"id"`
	AddressID int64     `json:"addressId"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MsgFAQInput creates or replaces a snippet.
type MsgFAQInput struct {
	AddressID int64
	Title     string
	Body      string
	Enabled   bool
}

// ErrFAQFull refuses a snippet past msgai.MaxFAQ on one number.
var ErrFAQFull = errors.New("store: the number has the most FAQ snippets it may have")

func listFAQ(ctx context.Context, q querier, addressID int64, enabledOnly bool) ([]msgai.FAQ, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, title, body FROM msg_faq WHERE address_id = $1 AND (enabled OR NOT $2) ORDER BY id LIMIT $3`,
		addressID, enabledOnly, msgai.MaxFAQ)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []msgai.FAQ{}
	for rows.Next() {
		var f msgai.FAQ
		if err := rows.Scan(&f.ID, &f.Title, &f.Body); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

const faqCols = `id, address_id, title, body, enabled, created_at, updated_at`

func scanFAQ(r interface{ Scan(...any) error }) (MsgFAQ, error) {
	var f MsgFAQ
	err := r.Scan(&f.ID, &f.AddressID, &f.Title, &f.Body, &f.Enabled, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

// ListMsgFAQ lists a number's snippets (every number's with addressID 0).
func (s *Store) ListMsgFAQ(ctx context.Context, addressID int64) ([]MsgFAQ, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+faqCols+` FROM msg_faq WHERE $1 = 0 OR address_id = $1 ORDER BY address_id, id`, addressID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []MsgFAQ{}
	for rows.Next() {
		f, err := scanFAQ(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetMsgFAQ returns one snippet.
func (s *Store) GetMsgFAQ(ctx context.Context, id int64) (MsgFAQ, error) {
	f, err := scanFAQ(s.db.QueryRowContext(ctx, `SELECT `+faqCols+` FROM msg_faq WHERE id = $1`, id))
	return f, mapErr(err)
}

// CreateMsgFAQ adds a snippet to a number: at most msgai.MaxFAQ.
func (s *Store) CreateMsgFAQ(ctx context.Context, actor string, in MsgFAQInput) (MsgFAQ, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM msg_addresses WHERE id = $1 FOR UPDATE`, in.AddressID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fieldError("addressId", "no such messaging number")
		}
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM msg_faq WHERE address_id = $1`, in.AddressID).Scan(&n); err != nil {
			return err
		}
		if n >= msgai.MaxFAQ {
			return ErrFAQFull
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO msg_faq (address_id, title, body, enabled) VALUES ($1, $2, $3, $4) RETURNING id`,
			in.AddressID, in.Title, in.Body, in.Enabled).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "msg_faq", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgFAQ{}, err
	}
	return s.GetMsgFAQ(ctx, id)
}

// UpdateMsgFAQ replaces a snippet's title, body and switch; its number
// stays.
func (s *Store) UpdateMsgFAQ(ctx context.Context, actor string, id int64, in MsgFAQInput) (MsgFAQ, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE msg_faq SET title = $2, body = $3, enabled = $4, updated_at = now() WHERE id = $1`,
			id, in.Title, in.Body, in.Enabled)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, actor, "update", "msg_faq", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return MsgFAQ{}, err
	}
	return s.GetMsgFAQ(ctx, id)
}

// DeleteMsgFAQ removes a snippet.
func (s *Store) DeleteMsgFAQ(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `DELETE FROM msg_faq WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, actor, "delete", "msg_faq", strconv.FormatInt(id, 10))
	})
}

// ---------------------------------------------------------------------------
// The settings.

// MsgAddressAI is the AI of one number or chat site.
type MsgAddressAI struct {
	AddressID      int64    `json:"addressId"`
	Mode           string   `json:"mode"`
	Redact         bool     `json:"redact"`
	Disclosure     bool     `json:"disclosure"`
	DisclosureText string   `json:"disclosureText"`
	HoldingText    string   `json:"holdingText"`
	MaxReplies     int      `json:"maxReplies"`
	HourlyCap      int      `json:"hourlyCap"`
	MinConfidence  float64  `json:"minConfidence"`
	AutoIntents    []string `json:"autoIntents"`
	HandoffWords   []string `json:"handoffWords"`
	FAQCount       int      `json:"faqCount"`
}

// GetMsgAddressAI returns a number's AI settings.
func (s *Store) GetMsgAddressAI(ctx context.Context, addressID int64) (MsgAddressAI, error) {
	m := MsgAddressAI{AddressID: addressID}
	var auto, words string
	err := s.db.QueryRowContext(ctx, `
		SELECT ai_mode, ai_redact, ai_disclosure, ai_disclosure_text, ai_holding_text, ai_max_replies, ai_hourly_cap,
		       round(ai_min_confidence::numeric, 2)::float8, to_json(ai_auto_intents)::text, to_json(ai_handoff_words)::text,
		       (SELECT count(*) FROM msg_faq f WHERE f.address_id = a.id)
		FROM msg_addresses a WHERE id = $1`, addressID).Scan(&m.Mode, &m.Redact, &m.Disclosure, &m.DisclosureText, &m.HoldingText,
		&m.MaxReplies, &m.HourlyCap, &m.MinConfidence, &auto, &words, &m.FAQCount)
	if err != nil {
		return m, mapErr(err)
	}
	m.AutoIntents, m.HandoffWords = jsonList(auto), jsonList(words)
	return m, nil
}

// PutMsgAddressAI replaces a number's AI settings. Turning auto-reply on or
// off leaves its own audit row (spec M-21).
func (s *Store) PutMsgAddressAI(ctx context.Context, actor string, m MsgAddressAI) (MsgAddressAI, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var was string
		if err := tx.QueryRowContext(ctx, `SELECT ai_mode FROM msg_addresses WHERE id = $1 FOR UPDATE`, m.AddressID).Scan(&was); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE msg_addresses SET ai_mode = $2, ai_redact = $3, ai_disclosure = $4, ai_disclosure_text = $5, ai_holding_text = $6,
				ai_max_replies = $7, ai_hourly_cap = $8, ai_min_confidence = $9, ai_auto_intents = $10, ai_handoff_words = $11
			WHERE id = $1`, m.AddressID, m.Mode, m.Redact, m.Disclosure, m.DisclosureText, m.HoldingText, m.MaxReplies, m.HourlyCap,
			m.MinConfidence, m.AutoIntents, m.HandoffWords); err != nil {
			return err
		}
		id := strconv.FormatInt(m.AddressID, 10)
		switch {
		case was != msgai.ModeAuto && m.Mode == msgai.ModeAuto:
			if err := insertAudit(ctx, tx, actor, "ai_autoreply_enabled", "msg_address", id); err != nil {
				return err
			}
		case was == msgai.ModeAuto && m.Mode != msgai.ModeAuto:
			if err := insertAudit(ctx, tx, actor, "ai_autoreply_disabled", "msg_address", id); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, actor, "update_ai", "msg_address", id)
	})
	if err != nil {
		return MsgAddressAI{}, err
	}
	return s.GetMsgAddressAI(ctx, m.AddressID)
}

// MessagingAI is the inbox-wide AI settings with the switch of each channel
// and the mode of each number.
type MessagingAI struct {
	// Enabled is the global kill switch of the inbox AI; AutoEnabled that
	// of auto-replies.
	Enabled         bool                `json:"enabled"`
	AutoEnabled     bool                `json:"autoEnabled"`
	BusinessName    string              `json:"businessName"`
	Intents         []string            `json:"intents"`
	SensitiveTopics []string            `json:"sensitiveTopics"`
	Channels        []MsgChannelAI      `json:"channels"`
	Addresses       []MsgAddressAIBrief `json:"addresses"`
}

// MsgChannelAI is one channel's AI switch.
type MsgChannelAI struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	AIEnabled bool   `json:"aiEnabled"`
}

// MsgAddressAIBrief is one number's AI mode.
type MsgAddressAIBrief struct {
	ID        int64  `json:"id"`
	ChannelID int64  `json:"channelId"`
	Address   string `json:"address"`
	Label     string `json:"label"`
	Mode      string `json:"mode"`
	FAQCount  int    `json:"faqCount"`
}

// MessagingAIInput replaces the inbox-wide AI settings; Channels sets the
// switch of the channels it names.
type MessagingAIInput struct {
	Enabled         bool
	AutoEnabled     bool
	BusinessName    string
	Intents         []string
	SensitiveTopics []string
	Channels        map[int64]bool
}

// GetMessagingAI returns the inbox-wide AI settings.
func (s *Store) GetMessagingAI(ctx context.Context) (MessagingAI, error) {
	var m MessagingAI
	var intents, topics string
	if err := s.db.QueryRowContext(ctx, `SELECT ai_enabled, ai_auto_enabled, ai_business_name, to_json(ai_intents)::text, to_json(ai_sensitive_topics)::text
		FROM messaging_settings`).Scan(&m.Enabled, &m.AutoEnabled, &m.BusinessName, &intents, &topics); err != nil {
		return m, err
	}
	m.Intents, m.SensitiveTopics = jsonList(intents), jsonList(topics)
	m.Channels, m.Addresses = []MsgChannelAI{}, []MsgAddressAIBrief{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, ai_enabled FROM msg_channels ORDER BY lower(name)`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var c MsgChannelAI
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.AIEnabled); err != nil {
			_ = rows.Close()
			return m, err
		}
		m.Channels = append(m.Channels, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT a.id, a.channel_id, a.address, a.label, a.ai_mode, (SELECT count(*) FROM msg_faq f WHERE f.address_id = a.id)
		FROM msg_addresses a ORDER BY a.channel_id, a.id`)
	if err != nil {
		return m, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a MsgAddressAIBrief
		if err := rows.Scan(&a.ID, &a.ChannelID, &a.Address, &a.Label, &a.Mode, &a.FAQCount); err != nil {
			return m, err
		}
		m.Addresses = append(m.Addresses, a)
	}
	return m, rows.Err()
}

// PutMessagingAI replaces the inbox-wide AI settings and sets the channels'
// switches. A channel it names that does not exist is a field error.
func (s *Store) PutMessagingAI(ctx context.Context, actor string, in MessagingAIInput) (MessagingAI, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE messaging_settings SET ai_enabled = $1, ai_auto_enabled = $2, ai_business_name = $3, ai_intents = $4, ai_sensitive_topics = $5`,
			in.Enabled, in.AutoEnabled, in.BusinessName, in.Intents, in.SensitiveTopics); err != nil {
			return err
		}
		ids := make([]int64, 0, len(in.Channels))
		for id := range in.Channels {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			on := in.Channels[id]
			var was bool
			err := tx.QueryRowContext(ctx, `SELECT ai_enabled FROM msg_channels WHERE id = $1 FOR UPDATE`, id).Scan(&was)
			if errors.Is(err, sql.ErrNoRows) {
				return fieldError("channels", "no channel "+strconv.FormatInt(id, 10))
			}
			if err != nil {
				return err
			}
			if was == on {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE msg_channels SET ai_enabled = $2 WHERE id = $1`, id, on); err != nil {
				return err
			}
			action := "ai_disabled"
			if on {
				action = "ai_enabled"
			}
			if err := insertAudit(ctx, tx, actor, action, "msg_channel", strconv.FormatInt(id, 10)); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, actor, "update_ai", "messaging_settings", "1")
	})
	if err != nil {
		return MessagingAI{}, err
	}
	return s.GetMessagingAI(ctx)
}

// ---------------------------------------------------------------------------
// Statistics (no customer data).

// InboxAIStats counts what the inbox AI did in the last 24 hours.
type InboxAIStats struct {
	// Runs counts the runs by kind and state.
	Runs []AIRunCount `json:"runs"`
	// AutoReplies is the AI messages sent, Handoffs the conversations the AI
	// handed to a person.
	AutoReplies int `json:"autoReplies"`
	Handoffs    int `json:"handoffs"`
	// Spam is the active conversations the triage flagged as spam.
	Spam int `json:"spam"`
	// Modes counts the numbers by AI mode.
	Modes AIModeCounts `json:"modes"`
}

// AIRunCount is the runs of one kind in one state.
type AIRunCount struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Count int    `json:"count"`
}

// AIModeCounts counts the numbers by AI mode.
type AIModeCounts struct {
	Off    int `json:"off"`
	Triage int `json:"triage"`
	Draft  int `json:"draft"`
	Auto   int `json:"auto"`
}

// GetInboxAIStats counts what the inbox AI did in the last 24 hours.
func (s *Store) GetInboxAIStats(ctx context.Context) (InboxAIStats, error) {
	out := InboxAIStats{Runs: []AIRunCount{}}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, state, count(*) FROM ai_message_runs WHERE created_at > now() - interval '24 hours'
		GROUP BY kind, state ORDER BY kind, state`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c AIRunCount
		if err := rows.Scan(&c.Kind, &c.State, &c.Count); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Runs = append(out.Runs, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM conversation_messages WHERE sender_kind = 'ai' AND kind = 'message' AND created_at > now() - interval '24 hours'),
		(SELECT count(*) FROM conversations WHERE ai_handoff_at > now() - interval '24 hours'),
		(SELECT count(*) FROM conversations WHERE ai_spam AND status IN ('open', 'pending', 'snoozed')),
		(SELECT count(*) FROM msg_addresses WHERE ai_mode = 'off'),
		(SELECT count(*) FROM msg_addresses WHERE ai_mode = 'triage'),
		(SELECT count(*) FROM msg_addresses WHERE ai_mode = 'draft'),
		(SELECT count(*) FROM msg_addresses WHERE ai_mode = 'auto')`).Scan(&out.AutoReplies, &out.Handoffs, &out.Spam,
		&out.Modes.Off, &out.Modes.Triage, &out.Modes.Draft, &out.Modes.Auto)
	return out, err
}
