package store

// What the supervisors and the agents see of routing: each text queue's
// waiting list and agents, and the queues an agent's extensions belong to.
// Ids, counts and names of queues and users only; no customer text or number.

import (
	"context"
)

// TextWaiting is a conversation waiting in a text queue.
type TextWaiting struct {
	ConversationID int64  `json:"conversationId"`
	Channel        string `json:"channel"`
	// Position is the place in the waiting list, 1 is next.
	Position    int   `json:"position"`
	WaitSeconds int64 `json:"waitSeconds"`
}

// TextAgent is a user who answers a text queue.
type TextAgent struct {
	UserID    int64  `json:"userId"`
	Name      string `json:"name"`
	Extension string `json:"extension"`
	Dynamic   bool   `json:"dynamic"`
	// Open is how many open conversations the user holds in all.
	Open int `json:"open"`
	// Answered is how many conversations the queue has given the user.
	Answered int64 `json:"answered"`
	// State is available, paused, logged_out, on_call or at_capacity; the
	// API fills it from the live agent state.
	State string `json:"state"`
}

// TextQueueStatus is one text queue as a supervisor sees it.
type TextQueueStatus struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Strategy string `json:"strategy"`
	Capacity int    `json:"capacity"`
	// Waiting is the unassigned conversations, longest waiting first (at
	// most 200).
	Waiting []TextWaiting `json:"waiting"`
	// WaitingTotal counts them all.
	WaitingTotal int `json:"waitingTotal"`
	// Assigned is how many open conversations agents hold from this queue.
	Assigned          int         `json:"assigned"`
	OldestWaitSeconds int64       `json:"oldestWaitSeconds"`
	Agents            []TextAgent `json:"agents"`
}

// GetTextQueueStatus lists the enabled text queues with their waiting lists
// and agents.
func (s *Store) GetTextQueueStatus(ctx context.Context) ([]TextQueueStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, strategy, text_capacity FROM queues WHERE enabled AND text_enabled ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	var out []TextQueueStatus
	for rows.Next() {
		q := TextQueueStatus{Waiting: []TextWaiting{}, Agents: []TextAgent{}}
		if err := rows.Scan(&q.ID, &q.Name, &q.Strategy, &q.Capacity); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, q)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		q := &out[i]
		if err := s.db.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE assignee_user_id IS NULL AND status IN ('open', 'pending')),
			       count(*) FILTER (WHERE assignee_user_id IS NOT NULL AND status IN ('open', 'pending'))
			FROM conversations WHERE queue_id = $1`, q.ID).Scan(&q.WaitingTotal, &q.Assigned); err != nil {
			return nil, err
		}
		wrows, err := s.db.QueryContext(ctx, `
			SELECT c.id, ch.kind, greatest(extract(epoch FROM now() - coalesce(c.routed_at, c.created_at)), 0)::bigint
			FROM conversations c JOIN msg_channels ch ON ch.id = c.channel_id
			WHERE c.queue_id = $1 AND c.assignee_user_id IS NULL AND c.status IN ('open', 'pending')
			ORDER BY c.routed_at, c.id LIMIT 200`, q.ID)
		if err != nil {
			return nil, err
		}
		for wrows.Next() {
			w := TextWaiting{Position: len(q.Waiting) + 1}
			if err := wrows.Scan(&w.ConversationID, &w.Channel, &w.WaitSeconds); err != nil {
				_ = wrows.Close()
				return nil, err
			}
			q.Waiting = append(q.Waiting, w)
		}
		_ = wrows.Close()
		if err := wrows.Err(); err != nil {
			return nil, err
		}
		if len(q.Waiting) > 0 {
			q.OldestWaitSeconds = q.Waiting[0].WaitSeconds
		}
		arows, err := s.db.QueryContext(ctx, `
			SELECT ue.user_id, coalesce(NULLIF(u.display_name, ''), u.username), e.number, m.dynamic,
			       (SELECT count(*) FROM conversations c WHERE c.assignee_user_id = ue.user_id AND c.status IN ('open', 'pending')),
			       coalesce(st.text_answered, 0)
			FROM queue_members m
			JOIN extensions e ON e.id = m.extension_id
			JOIN user_extensions ue ON ue.extension_id = m.extension_id
			JOIN users u ON u.id = ue.user_id AND u.enabled AND u.role IN ('user', 'operator', 'admin')
			LEFT JOIN msg_agent_stats st ON st.user_id = ue.user_id AND st.queue_id = m.queue_id
			WHERE m.queue_id = $1 ORDER BY m.position, ue.user_id`, q.ID)
		if err != nil {
			return nil, err
		}
		for arows.Next() {
			var a TextAgent
			if err := arows.Scan(&a.UserID, &a.Name, &a.Extension, &a.Dynamic, &a.Open, &a.Answered); err != nil {
				_ = arows.Close()
				return nil, err
			}
			q.Agents = append(q.Agents, a)
		}
		_ = arows.Close()
		if err := arows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TextAgentExt is an extension of a user that belongs to text queues.
type TextAgentExt struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	// Dynamic is true when a queue the extension is in needs it to log in.
	Dynamic bool     `json:"dynamic"`
	Queues  []string `json:"queues"`
}

// UserTextAgent lists the extensions of a user that are members of enabled
// text queues, with those queues. Empty for someone who answers no text
// queue.
func (s *Store) UserTextAgent(ctx context.Context, userID int64) ([]TextAgentExt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.number, e.name, m.dynamic, q.name
		FROM user_extensions ue
		JOIN extensions e ON e.id = ue.extension_id
		JOIN queue_members m ON m.extension_id = e.id
		JOIN queues q ON q.id = m.queue_id AND q.enabled AND q.text_enabled
		WHERE ue.user_id = $1 ORDER BY e.number, q.name`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TextAgentExt
	for rows.Next() {
		var (
			num, name, queue string
			dyn              bool
		)
		if err := rows.Scan(&num, &name, &dyn, &queue); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Number != num {
			out = append(out, TextAgentExt{Number: num, Name: name, Queues: []string{}})
		}
		last := &out[len(out)-1]
		last.Dynamic = last.Dynamic || dyn
		last.Queues = append(last.Queues, queue)
	}
	return out, rows.Err()
}
