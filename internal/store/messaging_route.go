package store

// Routing and ACD of the unified inbox (spec messaging-channels M-13, M-14).
//
// A number's route says where its new conversations go; the router places
// each new conversation once (routed_at), a text queue's waiting list is
// worked off by the router's sweep, and an unanswered conversation goes back
// to the pool after the queue's reassign time. Placing and assigning are
// compare-and-set in PostgreSQL under a per-agent advisory lock while the
// capacity is counted, so two control pods or two agents cannot both win, and
// nothing is held in memory.
//
// Message text is customer data: nothing here logs it, and the lines the
// router writes name queues and users, never customers.

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
	"github.com/azrtydxb/hello/internal/messaging/router"
)

// Why a conversation moved, as the assignment history records it.
const (
	assignRoute    = "route"        // a number's route named the user
	assignACD      = "acd"          // the queue's strategy picked the agent
	assignTimeout  = "acd_timeout"  // the agent left it unanswered
	assignUnlinked = "acd_unlinked" // the agent left the queue
)

// agentLock is the first key of the advisory lock that serialises the
// capacity count of one agent.
const agentLock = 0x6d736700 // "msg"

// The condition "the user is one of the people who may take the unassigned
// conversation c": a member of its text queue, or of its ring group, through
// the user their extension is linked to. The two forms differ only in how the
// user is named: poolMemberParam for the caller ($1 of the inbox queries),
// poolMemberUser for a row u of users. They are constants so the queries that
// use them stay constants.
const (
	poolMemberParam = `(EXISTS (SELECT 1 FROM queues pq JOIN queue_members pm ON pm.queue_id = pq.id
			JOIN user_extensions pe ON pe.extension_id = pm.extension_id
			WHERE pq.id = c.queue_id AND pq.enabled AND pq.text_enabled AND pe.user_id = $1::bigint)
		OR EXISTS (SELECT 1 FROM ring_group_members pg JOIN user_extensions pge ON pge.extension_id = pg.extension_id
			WHERE pg.group_id = c.ring_group_id AND pge.user_id = $1::bigint))`
	poolMemberUser = `(EXISTS (SELECT 1 FROM queues pq JOIN queue_members pm ON pm.queue_id = pq.id
			JOIN user_extensions pe ON pe.extension_id = pm.extension_id
			WHERE pq.id = c.queue_id AND pq.enabled AND pq.text_enabled AND pe.user_id = u.id)
		OR EXISTS (SELECT 1 FROM ring_group_members pg JOIN user_extensions pge ON pge.extension_id = pg.extension_id
			WHERE pg.group_id = c.ring_group_id AND pge.user_id = u.id))`
)

// MsgRouting is how a number routes new conversations and when it is open.
type MsgRouting struct {
	AddressID int64 `json:"addressId"`
	// RouteKind is inbox, queue, user, extension or ring_group; RouteTarget
	// is the id of the queue, user, extension or ring group ("" for inbox).
	RouteKind   string             `json:"routeKind"`
	RouteTarget string             `json:"routeTarget"`
	RouteName   string             `json:"routeName"`
	Schedule    messaging.Schedule `json:"schedule"`
	AfterHours  AfterHours         `json:"afterHours"`
	// FirstContactText is sent once to a customer who texts the number for the
	// first time; empty sends nothing.
	FirstContactText string `json:"firstContactText"`
}

// AfterHours is what a number does with a conversation that starts outside
// its schedule.
type AfterHours struct {
	// Action is none, reply or route.
	Action      string `json:"action"`
	Text        string `json:"text"`
	RouteKind   string `json:"routeKind"`
	RouteTarget string `json:"routeTarget"`
	RouteName   string `json:"routeName"`
}

// MsgRoutingInput replaces a number's routing.
type MsgRoutingInput struct {
	RouteKind   string
	RouteTarget string
	Schedule    messaging.Schedule
	AfterHours  AfterHours
	// FirstContactText: see MsgRouting.
	FirstContactText string
}

// routeName looks up what a route target is called, "" when it is gone.
func routeName(ctx context.Context, q rowQuerier, kind, target string) (string, error) {
	id, err := strconv.ParseInt(target, 10, 64)
	if err != nil {
		return "", nil
	}
	var query string
	switch kind {
	case messaging.RouteQueue:
		query = `SELECT name FROM queues WHERE id = $1`
	case messaging.RouteUser:
		query = `SELECT coalesce(NULLIF(display_name, ''), username) FROM users WHERE id = $1`
	case messaging.RouteExtension:
		query = `SELECT number || ' ' || name FROM extensions WHERE id = $1`
	case messaging.RouteRingGroup:
		query = `SELECT name FROM ring_groups WHERE id = $1`
	default:
		return "", nil
	}
	var name string
	err = q.QueryRowContext(ctx, query, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// GetMsgRouting returns how a number routes.
func (s *Store) GetMsgRouting(ctx context.Context, addressID int64) (MsgRouting, error) {
	var (
		r        = MsgRouting{AddressID: addressID}
		schedule string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT route_kind, route_target, schedule::text, after_hours_action, after_hours_text,
		       after_hours_route_kind, after_hours_route_target, first_contact_text
		FROM msg_addresses WHERE id = $1`, addressID).Scan(&r.RouteKind, &r.RouteTarget, &schedule,
		&r.AfterHours.Action, &r.AfterHours.Text, &r.AfterHours.RouteKind, &r.AfterHours.RouteTarget, &r.FirstContactText)
	if err != nil {
		return r, mapErr(err)
	}
	if err := json.Unmarshal([]byte(schedule), &r.Schedule); err != nil {
		return r, err
	}
	if r.Schedule.Days == nil {
		r.Schedule.Days = []messaging.ScheduleDay{}
	}
	if r.RouteName, err = routeName(ctx, s.db, r.RouteKind, r.RouteTarget); err != nil {
		return r, err
	}
	r.AfterHours.RouteName, err = routeName(ctx, s.db, r.AfterHours.RouteKind, r.AfterHours.RouteTarget)
	return r, err
}

// checkRouteTarget verifies that a route names something the router can use.
func checkRouteTarget(ctx context.Context, tx *sql.Tx, path, kind, target string) error {
	if kind == messaging.RouteInbox {
		if target != "" {
			return fieldError(path, "the pool takes no target")
		}
		return nil
	}
	id, err := strconv.ParseInt(target, 10, 64)
	if err != nil || id <= 0 {
		return fieldError(path, "must be the id of the "+strings.ReplaceAll(kind, "_", " "))
	}
	switch kind {
	case messaging.RouteQueue:
		var on, text bool
		err := tx.QueryRowContext(ctx, `SELECT enabled, text_enabled FROM queues WHERE id = $1`, id).Scan(&on, &text)
		if errors.Is(err, sql.ErrNoRows) {
			return fieldError(path, "no such queue")
		}
		if err != nil {
			return err
		}
		if !on || !text {
			return fieldError(path, "the queue is off or does not take texts; turn on texting on the queue first")
		}
	case messaging.RouteUser:
		if _, err := eligibleAgent(ctx, tx, id); errors.Is(err, ErrNotAgent) {
			return fieldError(path, "no such user, or the user cannot take conversations")
		} else if err != nil {
			return err
		}
	case messaging.RouteExtension:
		var linked bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_extensions WHERE extension_id = $1)`, id).Scan(&linked)
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM extensions WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fieldError(path, "no such extension")
		}
		if !linked {
			return fieldError(path, "the extension is not linked to a user, so nobody could answer")
		}
	case messaging.RouteRingGroup:
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM ring_groups WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fieldError(path, "no such ring group")
		}
	default:
		return fieldError(path, "unknown route kind")
	}
	return nil
}

// PutMsgRouting replaces how a number routes. A queue target must take
// texts, a user must be able to take conversations, an extension must be
// linked to a user, a ring group must exist.
func (s *Store) PutMsgRouting(ctx context.Context, actor string, addressID int64, in MsgRoutingInput) (MsgRouting, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM msg_addresses WHERE id = $1 FOR UPDATE`, addressID).Scan(&one); err != nil {
			return err
		}
		if err := checkRouteTarget(ctx, tx, "routeTarget", in.RouteKind, in.RouteTarget); err != nil {
			return err
		}
		if in.AfterHours.Action == messaging.AfterRoute {
			if err := checkRouteTarget(ctx, tx, "afterHours.routeTarget", in.AfterHours.RouteKind, in.AfterHours.RouteTarget); err != nil {
				return err
			}
		}
		days := in.Schedule.Days
		if days == nil {
			days = []messaging.ScheduleDay{}
		}
		in.Schedule.Days = days
		sched, err := json.Marshal(in.Schedule)
		if err != nil {
			return err
		}
		if len(days) == 0 {
			sched = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE msg_addresses SET route_kind = $2, route_target = $3, schedule = $4::jsonb, after_hours_action = $5,
				after_hours_text = $6, after_hours_route_kind = $7, after_hours_route_target = $8, first_contact_text = $9
			WHERE id = $1`, addressID, in.RouteKind, in.RouteTarget, string(sched), in.AfterHours.Action, in.AfterHours.Text,
			in.AfterHours.RouteKind, in.AfterHours.RouteTarget, in.FirstContactText); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update_routing", "msg_address", strconv.FormatInt(addressID, 10))
	})
	if err != nil {
		return MsgRouting{}, err
	}
	return s.GetMsgRouting(ctx, addressID)
}

// MessageRouter returns the store as the router's persistence.
func (s *Store) MessageRouter() router.Store { return routerStore{s} }

type routerStore struct{ s *Store }

// afterAssign runs in the transaction that gave a conversation to a user, for
// whatever else an assignment must do. The desk-phone delivery of item 3
// belongs here (deliverLastToDeskPhone) once that item is on main.
func (s *Store) afterAssign(_ context.Context, _ *sql.Tx, _, _ int64) error { return nil }

// ---------------------------------------------------------------------------
// Placing new conversations.

// placement is where a route puts a conversation.
type placement struct {
	user    int64
	queue   int64
	group   int64
	label   string // "Alice", "queue Support", "ring group Sales"; "" for the pool
	members bool   // there is a pool to tell
}

// resolvePlacement turns a route into a placement; a target that is gone or
// cannot take conversations falls back to the supervisors' pool.
func resolvePlacement(ctx context.Context, tx *sql.Tx, kind, target string) (placement, error) {
	var p placement
	id, err := strconv.ParseInt(target, 10, 64)
	if err != nil || id <= 0 {
		return p, nil
	}
	switch kind {
	case messaging.RouteQueue:
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM queues WHERE id = $1 AND enabled AND text_enabled`, id).Scan(&name)
		if err == nil {
			p = placement{queue: id, label: "queue " + name, members: true}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
	case messaging.RouteUser, messaging.RouteExtension:
		user := id
		if kind == messaging.RouteExtension {
			user = 0
			err := tx.QueryRowContext(ctx, `SELECT user_id FROM user_extensions WHERE extension_id = $1`, id).Scan(&user)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return p, err
			}
		}
		if user > 0 {
			name, err := eligibleAgent(ctx, tx, user)
			if err == nil {
				p = placement{user: user, label: name}
			} else if !errors.Is(err, ErrNotAgent) {
				return p, err
			}
		}
	case messaging.RouteRingGroup:
		var name string
		var n int
		err := tx.QueryRowContext(ctx, `
			SELECT g.name, (SELECT count(*) FROM ring_group_members gm
				JOIN user_extensions ue ON ue.extension_id = gm.extension_id
				JOIN users u ON u.id = ue.user_id AND u.enabled AND u.role IN ('user', 'operator', 'admin')
				WHERE gm.group_id = g.id)
			FROM ring_groups g WHERE g.id = $1`, id).Scan(&name, &n)
		if err == nil && n > 0 {
			p = placement{group: id, label: "ring group " + name, members: true}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
	}
	return p, nil
}

func nilIf(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// systemText queues a text the router sends on its own (the closed-hours
// reply) through the conversation's channel. A customer who opted out gets
// nothing. Only a SIP trunk channel can send today; another provider's
// channel gets a line in the thread saying the text was not sent. sent is
// whether a text was queued.
func (s *Store) systemText(ctx context.Context, tx *sql.Tx, c convRow, body string) (sent bool, err error) {
	var (
		provider, address    string
		chEnabled, adEnabled bool
		trunk                sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT ch.provider, ch.enabled, ch.trunk_id, a.address, a.enabled
		FROM msg_channels ch JOIN msg_addresses a ON a.channel_id = ch.id WHERE ch.id = $1 AND a.id = $2`,
		c.ChannelID, c.AddressID).Scan(&provider, &chEnabled, &trunk, &address, &adEnabled)
	if err != nil {
		return false, err
	}
	if !chEnabled || !adEnabled {
		return false, nil
	}
	out, err := optedOut(ctx, tx, s, c.AddressID, c.PeerKey)
	if err != nil || out {
		return false, err
	}
	if provider != messaging.ProviderSIPTrunk || !trunk.Valid {
		_, err = addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", "The closed-hours reply was not sent: this channel cannot send automatic texts")
		return false, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO conversation_messages (conversation_id, channel_id, direction, kind, sender_kind, sender_name, body, status)
		VALUES ($1, $2, 'out', 'message', 'system', '', $3, 'sending') RETURNING id`, c.ID, c.ChannelID, body).Scan(&id); err != nil {
		return false, err
	}
	for _, part := range messaging.SplitText(body, messaging.MaxPartBytes) {
		text, raw := splitBody(messageDefaultCType, []byte(part))
		var m MessageSent
		err := s.sendMessageTx(ctx, tx, MessageInput{
			Sender: address, SenderKind: MsgSenderSystem, Actor: "system:router",
			TargetKind: MsgKindExternal, Target: c.PeerKey, ContentType: messageDefaultCType, Body: []byte(part),
			TrunkID: trunk.Int64, ConversationMessageID: id,
		}, text, raw, &m)
		if errors.Is(err, ErrMessagesDisabled) || errors.Is(err, ErrNoExternalTrunk) || errors.Is(err, ErrMessageRate) ||
			errors.Is(err, ErrMessageTarget) || errors.Is(err, ErrMessageForbidden) {
			// Texting is off or refuses: the message row would never be sent.
			if _, derr := tx.ExecContext(ctx, `DELETE FROM conversation_messages WHERE id = $1`, id); derr != nil {
				return false, derr
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversations SET last_message_id = $2, last_message_at = now() WHERE id = $1`, c.ID, id)
	return err == nil, err
}

// RouteNew places the conversations no router has placed yet, oldest first,
// at most limit of them. Each one is its own transaction and is claimed with
// SKIP LOCKED, so any number of pods can run it at once.
func (rs routerStore) RouteNew(ctx context.Context, now time.Time, limit int) (router.RouteResult, error) {
	var res router.RouteResult
	for i := 0; i < limit; i++ {
		var more, replied bool
		err := rs.s.tx(ctx, func(tx *sql.Tx) error {
			more, replied = false, false
			var err error
			more, replied, err = rs.s.routeOne(ctx, tx, now)
			return err
		})
		if err != nil {
			return res, err
		}
		if !more {
			break
		}
		res.Placed++
		if replied {
			res.Replies++
		}
	}
	return res, nil
}

func (s *Store) routeOne(ctx context.Context, tx *sql.Tx, now time.Time) (more, replied bool, err error) {
	var (
		c                                 convRow
		status                            string
		assignee                          sql.NullInt64
		addrOn                            bool
		kind, target, sched, action, text string
		ahKind, ahTarget, firstText       string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT c.id, c.channel_id, c.address_id, c.peer_key, c.status, c.assignee_user_id,
		       a.enabled, a.route_kind, a.route_target, a.schedule::text, a.after_hours_action, a.after_hours_text,
		       a.after_hours_route_kind, a.after_hours_route_target, a.first_contact_text
		FROM conversations c JOIN msg_addresses a ON a.id = c.address_id
		WHERE c.routed_at IS NULL ORDER BY c.id LIMIT 1 FOR UPDATE OF c SKIP LOCKED`).Scan(
		&c.ID, &c.ChannelID, &c.AddressID, &c.PeerKey, &status, &assignee, &addrOn, &kind, &target, &sched, &action, &text, &ahKind, &ahTarget, &firstText)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	c.Status, c.Assignee = status, assignee
	// Done with already: a person answered or took it before the router got
	// to it, or it ended.
	if assignee.Valid || status == messaging.StatusResolved || status == messaging.StatusSpam {
		_, err = tx.ExecContext(ctx, `UPDATE conversations SET routed_at = now() WHERE id = $1`, c.ID)
		return true, false, err
	}
	var schedule messaging.Schedule
	if err := json.Unmarshal([]byte(sched), &schedule); err != nil {
		schedule = messaging.Schedule{} // an unreadable schedule is always open
	}
	closed := !schedule.Open(now)
	if closed {
		switch action {
		case messaging.AfterRoute:
			kind, target = ahKind, ahTarget
		case messaging.AfterReply:
			if strings.TrimSpace(text) != "" {
				if replied, err = s.systemText(ctx, tx, c, text); err != nil {
					return true, false, err
				}
			}
		}
	}
	// A customer's first text to the number gets the first-contact text, unless
	// the closed-hours reply already went (one automatic text is enough).
	if !replied && strings.TrimSpace(firstText) != "" {
		var earlier bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM conversations WHERE address_id = $1 AND peer_key = $2 AND id <> $3)`,
			c.AddressID, c.PeerKey, c.ID).Scan(&earlier); err != nil {
			return true, false, err
		}
		if !earlier {
			if replied, err = s.systemText(ctx, tx, c, firstText); err != nil {
				return true, false, err
			}
		}
	}
	p, err := resolvePlacement(ctx, tx, kind, target)
	if err != nil {
		return true, replied, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET routed_at = now(), queue_id = $2, ring_group_id = $3, assignee_user_id = $4,
			assigned_at = CASE WHEN $4::bigint IS NULL THEN NULL ELSE now() END
		WHERE id = $1`, c.ID, nilIf(p.queue), nilIf(p.group), nilIf(p.user)); err != nil {
		return true, replied, err
	}
	if p.label == "" {
		return true, replied, nil // the supervisors' pool: the ingest told them already
	}
	if p.user > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, to_user_id, reason) VALUES ($1, $2, $3)`,
			c.ID, p.user, assignRoute); err != nil {
			return true, replied, err
		}
	}
	if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", "Routed to "+p.label); err != nil {
		return true, replied, err
	}
	if p.user > 0 {
		if err := s.afterAssign(ctx, tx, c.ID, p.user); err != nil {
			return true, replied, err
		}
	}
	return true, replied, announceRouted(ctx, tx, c.ID, p.user)
}

// announceRouted tells everyone a routed conversation concerns: its assignee
// or the pool it waits in get "new conversation" (the notice an agent is
// emailed about), the supervisors get "assigned" so their lists refresh.
func announceRouted(ctx context.Context, tx *sql.Tx, convID, assignee int64) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(inboxEventLock)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_events (user_id, type, conversation_id, message_id)
		SELECT u.id, CASE WHEN u.role IN ('operator', 'admin') AND u.id IS DISTINCT FROM $2::bigint
		                       AND NOT `+poolMemberUser+` THEN 'conversation.assigned' ELSE 'conversation.new' END,
		       c.id, c.last_message_id
		FROM conversations c JOIN users u ON u.enabled
		WHERE c.id = $1 AND (u.id = $2::bigint OR u.role IN ('operator', 'admin') OR `+poolMemberUser+`)`,
		convID, nilIf(assignee))
	return err
}

// ---------------------------------------------------------------------------
// The waiting list and assignment.

// TextQueues lists the enabled queues that take texts.
func (rs routerStore) TextQueues(ctx context.Context) ([]router.Queue, error) {
	rows, err := rs.s.db.QueryContext(ctx, `SELECT id, name, strategy, text_capacity, text_requires_idle_voice, text_reassign_seconds
		FROM queues WHERE enabled AND text_enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []router.Queue
	for rows.Next() {
		var q router.Queue
		if err := rows.Scan(&q.ID, &q.Name, &q.Strategy, &q.Capacity, &q.RequiresIdleVoice, &q.ReassignSeconds); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// Agents lists a queue's members linked to enabled users that can take
// conversations, with their open conversations and text statistics.
func (rs routerStore) Agents(ctx context.Context, queueID int64) ([]router.Agent, error) {
	rows, err := rs.s.db.QueryContext(ctx, `
		SELECT ue.user_id, e.number, m.dynamic, m.penalty, m.position,
		       (SELECT count(*) FROM conversations c WHERE c.assignee_user_id = ue.user_id AND c.status IN ('open', 'pending')),
		       coalesce(st.text_answered, 0), st.last_end
		FROM queue_members m
		JOIN extensions e ON e.id = m.extension_id
		JOIN user_extensions ue ON ue.extension_id = m.extension_id
		JOIN users u ON u.id = ue.user_id AND u.enabled AND u.role IN ('user', 'operator', 'admin')
		LEFT JOIN msg_agent_stats st ON st.user_id = ue.user_id AND st.queue_id = m.queue_id
		WHERE m.queue_id = $1 ORDER BY m.position, ue.user_id`, queueID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []router.Agent
	for rows.Next() {
		var (
			a   router.Agent
			end sql.NullTime
		)
		if err := rows.Scan(&a.UserID, &a.Ext, &a.Dynamic, &a.Penalty, &a.Position, &a.Open, &a.Answered, &end); err != nil {
			return nil, err
		}
		if end.Valid {
			a.LastEnd = end.Time
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Waiting lists a queue's unassigned conversations, oldest first. A ring_all
// queue shares its pool: nothing waits to be assigned.
func (rs routerStore) Waiting(ctx context.Context, queueID int64, limit int) ([]router.Waiting, error) {
	rows, err := rs.s.db.QueryContext(ctx, `
		SELECT c.id, coalesce(CASE WHEN la.reason = $3 THEN la.from_user_id END, 0)
		FROM conversations c
		JOIN queues q ON q.id = c.queue_id
		LEFT JOIN LATERAL (SELECT a.reason, a.from_user_id FROM conversation_assignments a
			WHERE a.conversation_id = c.id ORDER BY a.id DESC LIMIT 1) la ON TRUE
		WHERE c.queue_id = $1 AND c.assignee_user_id IS NULL AND c.status IN ('open', 'pending')
		  AND c.routed_at IS NOT NULL AND q.strategy <> 'ring_all'
		ORDER BY c.routed_at, c.id LIMIT $2`, queueID, limit, assignTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []router.Waiting
	for rows.Next() {
		var w router.Waiting
		if err := rows.Scan(&w.ID, &w.Exclude); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Assign gives a waiting conversation to a user: a compare-and-set on the
// conversation, with the user's open conversations counted under an advisory
// lock so two pods cannot both fill the last place.
func (rs routerStore) Assign(ctx context.Context, convID, userID int64, q router.Queue) (messaging.AssignOutcome, error) {
	out := messaging.AssignGone
	err := rs.s.tx(ctx, func(tx *sql.Tx) error {
		out = messaging.AssignGone
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, ($2::bigint % 2147483647)::int)`, agentLock, userID); err != nil {
			return err
		}
		var c convRow
		var queueID sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT id, channel_id, address_id, peer_key, status, assignee_user_id, queue_id
			FROM conversations WHERE id = $1 FOR UPDATE`, convID).Scan(&c.ID, &c.ChannelID, &c.AddressID, &c.PeerKey, &c.Status, &c.Assignee, &queueID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if c.Assignee.Valid || !queueID.Valid || queueID.Int64 != q.ID ||
			(c.Status != messaging.StatusOpen && c.Status != messaging.StatusPending) {
			return nil
		}
		name, err := eligibleAgent(ctx, tx, userID)
		if errors.Is(err, ErrNotAgent) {
			out = messaging.AssignFull // not an agent any more: next one
			return nil
		}
		if err != nil {
			return err
		}
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE assignee_user_id = $1 AND status IN ('open', 'pending')`,
			userID).Scan(&open); err != nil {
			return err
		}
		if open >= q.Capacity {
			out = messaging.AssignFull
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE conversations SET assignee_user_id = $2, assigned_at = now() WHERE id = $1 AND assignee_user_id IS NULL`,
			convID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, to_user_id, reason) VALUES ($1, $2, $3)`,
			convID, userID, assignACD); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO msg_agent_stats (user_id, queue_id, text_answered, last_end) VALUES ($1, $2, 1, now())
			ON CONFLICT (user_id, queue_id) DO UPDATE SET text_answered = msg_agent_stats.text_answered + 1, last_end = now()`,
			userID, q.ID); err != nil {
			return err
		}
		if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", "Assigned to "+name+" from queue "+q.Name); err != nil {
			return err
		}
		if err := rs.s.afterAssign(ctx, tx, convID, userID); err != nil {
			return err
		}
		if err := announceRouted(ctx, tx, convID, userID); err != nil {
			return err
		}
		out = messaging.AssignDone
		return nil
	})
	return out, err
}

// NextRotation returns the queue's text rotation counter and advances it.
func (rs routerStore) NextRotation(ctx context.Context, queueID int64) (uint64, error) {
	var rr int64
	err := rs.s.db.QueryRowContext(ctx, `
		INSERT INTO msg_queue_rr (queue_id, rr) VALUES ($1, 1)
		ON CONFLICT (queue_id) DO UPDATE SET rr = msg_queue_rr.rr + 1 RETURNING rr - 1`, queueID).Scan(&rr)
	return uint64(max(rr, 0)), err
}

// ---------------------------------------------------------------------------
// Giving conversations back.

// pool returns an assigned conversation to the waiting list: the assignee is
// cleared, the history says why, the thread says so and the pool is told.
func (s *Store) pool(ctx context.Context, tx *sql.Tx, c convRow, from int64, reason, line string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET assignee_user_id = NULL, assigned_at = NULL WHERE id = $1 AND assignee_user_id = $2`,
		c.ID, from); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_assignments (conversation_id, from_user_id, reason) VALUES ($1, $2, $3)`,
		c.ID, from, reason); err != nil {
		return err
	}
	if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", line); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(inboxEventLock)); err != nil {
		return err
	}
	// The one who lost it, the supervisors and the pool see it move.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_events (user_id, type, conversation_id)
		SELECT u.id, 'conversation.assigned', c.id FROM conversations c JOIN users u ON u.enabled
		WHERE c.id = $1 AND (u.id = $2 OR u.role IN ('operator', 'admin') OR `+poolMemberUser+`)`, c.ID, from)
	return err
}

const staleBatch = 50

// latestRouterAssignment is the SQL condition "the conversation's latest
// assignment was the router's": a person's later claim, reply or manual
// assignment takes it out of the router's hands.
const latestRouterAssignment = `(SELECT a.reason FROM conversation_assignments a WHERE a.conversation_id = c.id ORDER BY a.id DESC LIMIT 1) = '` + assignACD + `'`

// ReturnStale returns the conversations the router gave an agent who left the
// customer's last text unanswered for longer than the queue's reassign time.
func (rs routerStore) ReturnStale(ctx context.Context, now time.Time) (int, error) {
	n := 0
	err := rs.s.tx(ctx, func(tx *sql.Tx) error {
		n = 0
		rows, err := tx.QueryContext(ctx, `
			SELECT c.id, c.channel_id, c.address_id, c.peer_key, c.status, c.assignee_user_id, q.text_reassign_seconds
			FROM conversations c JOIN queues q ON q.id = c.queue_id AND q.text_enabled AND q.text_reassign_seconds > 0
			WHERE c.assignee_user_id IS NOT NULL AND c.status = 'open' AND c.last_inbound_at IS NOT NULL
			  AND greatest(c.last_inbound_at, coalesce(c.assigned_at, c.last_inbound_at))
			      < $1::timestamptz - make_interval(secs => q.text_reassign_seconds)
			  AND `+latestRouterAssignment+`
			  AND NOT EXISTS (SELECT 1 FROM conversation_messages m WHERE m.conversation_id = c.id AND m.direction = 'out'
			      AND m.kind = 'message' AND m.sender_kind = 'user' AND m.created_at > c.last_inbound_at)
			ORDER BY c.last_inbound_at LIMIT $2 FOR UPDATE OF c SKIP LOCKED`, now, staleBatch)
		if err != nil {
			return err
		}
		type stale struct {
			row  convRow
			secs int
		}
		var list []stale
		for rows.Next() {
			var x stale
			if err := rows.Scan(&x.row.ID, &x.row.ChannelID, &x.row.AddressID, &x.row.PeerKey, &x.row.Status, &x.row.Assignee, &x.secs); err != nil {
				_ = rows.Close()
				return err
			}
			list = append(list, x)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range list {
			line := fmt.Sprintf("No reply within %d minutes: returned to the queue", max(x.secs/60, 1))
			if err := rs.s.pool(ctx, tx, x.row, x.row.Assignee.Int64, assignTimeout, line); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ReleaseOrphans moves what no longer fits its queue: an unassigned
// conversation of a queue that stopped taking texts goes back to the
// supervisors' pool, and a conversation the router gave to a user who is no
// longer one of the queue's agents (their extension was unlinked, they were
// disabled, they left the queue) returns to the waiting list.
func (rs routerStore) ReleaseOrphans(ctx context.Context) (int, error) {
	n := 0
	err := rs.s.tx(ctx, func(tx *sql.Tx) error {
		n = 0
		rows, err := tx.QueryContext(ctx, `
			UPDATE conversations c SET queue_id = NULL FROM queues q
			WHERE q.id = c.queue_id AND NOT (q.enabled AND q.text_enabled)
			  AND c.assignee_user_id IS NULL AND c.status IN ('open', 'pending', 'snoozed')
			RETURNING c.id, c.channel_id`)
		if err != nil {
			return err
		}
		var moved []convRow
		for rows.Next() {
			var c convRow
			if err := rows.Scan(&c.ID, &c.ChannelID); err != nil {
				_ = rows.Close()
				return err
			}
			moved = append(moved, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range moved {
			if _, err := addLine(ctx, tx, c, messaging.KindSystem, messaging.SenderSystem, 0, "", "The queue stopped taking texts: moved to the pool"); err != nil {
				return err
			}
			if err := emitPoolEvent(ctx, tx, c.ID, "conversation.assigned", 0); err != nil {
				return err
			}
			n++
		}
		rows, err = tx.QueryContext(ctx, `
			SELECT c.id, c.channel_id, c.address_id, c.peer_key, c.status, c.assignee_user_id
			FROM conversations c
			WHERE c.queue_id IS NOT NULL AND c.assignee_user_id IS NOT NULL AND c.status IN ('open', 'pending')
			  AND `+latestRouterAssignment+`
			  AND NOT EXISTS (SELECT 1 FROM queue_members m
			        JOIN user_extensions ue ON ue.extension_id = m.extension_id
			        JOIN users u ON u.id = ue.user_id AND u.enabled AND u.role IN ('user', 'operator', 'admin')
			        WHERE m.queue_id = c.queue_id AND ue.user_id = c.assignee_user_id)
			ORDER BY c.id LIMIT $1 FOR UPDATE OF c SKIP LOCKED`, staleBatch)
		if err != nil {
			return err
		}
		var list []convRow
		for rows.Next() {
			var c convRow
			if err := rows.Scan(&c.ID, &c.ChannelID, &c.AddressID, &c.PeerKey, &c.Status, &c.Assignee); err != nil {
				_ = rows.Close()
				return err
			}
			list = append(list, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range list {
			if err := rs.s.pool(ctx, tx, c, c.Assignee.Int64, assignUnlinked, "The agent is no longer in the queue: returned to the queue"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// CountFirstResponses marks the first responses not yet counted and returns
// how long each took, from the start of the conversation.
func (rs routerStore) CountFirstResponses(ctx context.Context) ([]time.Duration, error) {
	rows, err := rs.s.db.QueryContext(ctx, `
		UPDATE conversations SET frt_counted = TRUE
		WHERE id IN (SELECT id FROM conversations WHERE NOT frt_counted AND first_response_at IS NOT NULL
			ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED)
		RETURNING greatest(extract(epoch FROM first_response_at - created_at), 0)::float8`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []time.Duration
	for rows.Next() {
		var secs float64
		if err := rows.Scan(&secs); err != nil {
			return nil, err
		}
		out = append(out, time.Duration(secs*float64(time.Second)))
	}
	return out, rows.Err()
}

var _ router.Store = routerStore{}
