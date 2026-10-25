package snapshot

import (
	"context"
	"fmt"
)

// QueueDest is where a queue sends a caller it cannot serve (timeout, full,
// no agents). Kind is an IVR destination kind or "queue".
type QueueDest = IVRDest

// QueueMember is one agent of a queue.
type QueueMember struct {
	Extension string
	Dynamic   bool
	Penalty   int
	Position  int
}

// Queue is a call queue (migration 00032). Prompt names are announcement
// names (resolved with Snapshot.Announcement); empty means none.
type Queue struct {
	ID                                       int64
	Name, Number                             string
	Enabled                                  bool
	Strategy                                 string
	AgentTimeout, RetryDelay, WrapUp         int // seconds
	MaxCallers, MaxWait                      int // 0: no limit
	JoinEmpty, LeaveEmpty                    bool
	Timeout, Full, NoAgents                  QueueDest
	MOHClassID                               int64 // 0: the global default
	AnnounceFrequency                        int
	AnnouncePosition, AnnounceWait           bool
	PeriodicPrompt, PositionPrompt           string
	WaitPrompt, MinutesPrompt, DigitsPrefix  string
	BreakoutKey, BreakoutPrompt, BreakoutBox string
	Members                                  []QueueMember
}

// WithQueues installs the call queues (tests and Load); it returns s.
func (s *Snapshot) WithQueues(qs []Queue) *Snapshot {
	if s.queueByName == nil {
		s.queueByName, s.queueByNumber = map[string]Queue{}, map[string]Queue{}
	}
	for _, q := range qs {
		s.queueByName[q.Name] = q
		if q.Number != "" {
			s.queueByNumber[q.Number] = q
		}
	}
	return s
}

// Queue returns the queue with that name.
func (s *Snapshot) Queue(name string) (Queue, bool) {
	q, ok := s.queueByName[name]
	return q, ok
}

// QueueByNumber returns the queue dialled as number.
func (s *Snapshot) QueueByNumber(number string) (Queue, bool) {
	if number == "" {
		return Queue{}, false
	}
	q, ok := s.queueByNumber[number]
	return q, ok
}

// Queues lists every queue (in no particular order).
func (s *Snapshot) Queues() []Queue {
	out := make([]Queue, 0, len(s.queueByName))
	for _, q := range s.queueByName {
		out = append(out, q)
	}
	return out
}

func loadQueues(ctx context.Context, db Querier) ([]Queue, error) {
	rows, err := db.Query(ctx, `SELECT q.id, q.name, q.number, q.enabled, q.strategy, q.agent_timeout, q.retry_delay, q.wrap_up,
		q.max_callers, q.max_wait, q.join_empty, q.leave_empty,
		q.timeout_kind, q.timeout_target, q.full_kind, q.full_target, q.no_agents_kind, q.no_agents_target, COALESCE(q.moh_class_id, 0),
		q.announce_frequency, q.announce_position, q.announce_wait,
		COALESCE(pp.name, ''), COALESCE(po.name, ''), COALESCE(wp.name, ''), COALESCE(mp.name, ''), q.digits_prefix,
		q.breakout_key, COALESCE(bp.name, ''), q.breakout_voicemail
		FROM queues q
		LEFT JOIN announcements pp ON pp.id = q.periodic_prompt_id
		LEFT JOIN announcements po ON po.id = q.position_prompt_id
		LEFT JOIN announcements wp ON wp.id = q.wait_prompt_id
		LEFT JOIN announcements mp ON mp.id = q.minutes_prompt_id
		LEFT JOIN announcements bp ON bp.id = q.breakout_prompt_id
		ORDER BY q.id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: queues: %w", err)
	}
	var qs []Queue
	for rows.Next() {
		var q Queue
		if err := rows.Scan(&q.ID, &q.Name, &q.Number, &q.Enabled, &q.Strategy, &q.AgentTimeout, &q.RetryDelay, &q.WrapUp,
			&q.MaxCallers, &q.MaxWait, &q.JoinEmpty, &q.LeaveEmpty,
			&q.Timeout.Kind, &q.Timeout.Target, &q.Full.Kind, &q.Full.Target, &q.NoAgents.Kind, &q.NoAgents.Target, &q.MOHClassID,
			&q.AnnounceFrequency, &q.AnnouncePosition, &q.AnnounceWait,
			&q.PeriodicPrompt, &q.PositionPrompt, &q.WaitPrompt, &q.MinutesPrompt, &q.DigitsPrefix,
			&q.BreakoutKey, &q.BreakoutPrompt, &q.BreakoutBox); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan queue: %w", err)
		}
		qs = append(qs, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: queues: %w", err)
	}
	if len(qs) == 0 {
		return nil, nil
	}
	byID := map[int64]int{}
	for i, q := range qs {
		byID[q.ID] = i
	}
	mrows, err := db.Query(ctx, `SELECT m.queue_id, e.number, m.dynamic, m.penalty, m.position
		FROM queue_members m JOIN extensions e ON e.id = m.extension_id ORDER BY m.queue_id, m.position`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: queue members: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var (
			id int64
			m  QueueMember
		)
		if err := mrows.Scan(&id, &m.Extension, &m.Dynamic, &m.Penalty, &m.Position); err != nil {
			return nil, fmt.Errorf("snapshot: scan queue member: %w", err)
		}
		if i, ok := byID[id]; ok {
			qs[i].Members = append(qs[i].Members, m)
		}
	}
	return qs, mrows.Err()
}
