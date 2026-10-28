package store

// ACD call queues (migration 00040). The configuration lives here; what
// changes while calls wait (callers, agent login, pause) lives in Valkey
// (internal/queue). Every mutation runs through configChange, so it is
// audited, bumps the configuration revision and notifies hello-sip.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// MaxQueues and MaxQueueMembers bound a deployment's queues.
const (
	MaxQueues       = 100
	MaxQueueMembers = 200
)

// IVRQueueDest is the destination kind "queue": the target is a queue name.
// It joins the IVR destination kinds, and is the kind of the queue's own
// timeout, full and no-agents destinations.
const IVRQueueDest = "queue"

// Queue feature-code actions (migration 00040).
const (
	ActionQueueLogin   = "queue_login"
	ActionQueueLogout  = "queue_logout"
	ActionQueuePause   = "queue_pause"
	ActionQueueUnpause = "queue_unpause"
)

// QueueMember is one agent of a queue. Number and Name are for display.
type QueueMember struct {
	ExtensionID int64  `json:"extensionId"`
	Number      string `json:"number"`
	Name        string `json:"name"`
	Dynamic     bool   `json:"dynamic"`
	Penalty     int    `json:"penalty"`
	Position    int    `json:"position"`
}

// Queue is a queue as the API shows it. Prompt names are read-only display
// values of the announcement ids.
type Queue struct {
	ID                 int64          `json:"id"`
	Name               string         `json:"name"`
	Number             string         `json:"number"`
	Enabled            bool           `json:"enabled"`
	Strategy           string         `json:"strategy"`
	AgentTimeout       int            `json:"agentTimeout"`
	RetryDelay         int            `json:"retryDelay"`
	WrapUp             int            `json:"wrapUp"`
	MaxCallers         int            `json:"maxCallers"`
	MaxWait            int            `json:"maxWait"`
	JoinEmpty          bool           `json:"joinEmpty"`
	LeaveEmpty         bool           `json:"leaveEmpty"`
	Timeout            IVRDestination `json:"timeout"`
	Full               IVRDestination `json:"full"`
	NoAgents           IVRDestination `json:"noAgents"`
	MOHClassID         *int64         `json:"mohClassId"`
	AnnounceFrequency  int            `json:"announceFrequency"`
	AnnouncePosition   bool           `json:"announcePosition"`
	AnnounceWait       bool           `json:"announceWait"`
	PeriodicPromptID   *int64         `json:"periodicPromptId"`
	PeriodicPromptName string         `json:"periodicPromptName"`
	PositionPromptID   *int64         `json:"positionPromptId"`
	PositionPromptName string         `json:"positionPromptName"`
	WaitPromptID       *int64         `json:"waitPromptId"`
	WaitPromptName     string         `json:"waitPromptName"`
	MinutesPromptID    *int64         `json:"minutesPromptId"`
	MinutesPromptName  string         `json:"minutesPromptName"`
	DigitsPrefix       string         `json:"digitsPrefix"`
	BreakoutKey        string         `json:"breakoutKey"`
	BreakoutPromptID   *int64         `json:"breakoutPromptId"`
	BreakoutPromptName string         `json:"breakoutPromptName"`
	BreakoutVoicemail  string         `json:"breakoutVoicemail"`
	Members            []QueueMember  `json:"members"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
}

// QueueInput is the writable shape of a queue; an update replaces the whole
// queue, its members included.
type QueueInput struct {
	Name              string
	Number            string
	Enabled           bool
	Strategy          string
	AgentTimeout      int
	RetryDelay        int
	WrapUp            int
	MaxCallers        int
	MaxWait           int
	JoinEmpty         bool
	LeaveEmpty        bool
	Timeout           IVRDestination
	Full              IVRDestination
	NoAgents          IVRDestination
	MOHClassID        *int64
	AnnounceFrequency int
	AnnouncePosition  bool
	AnnounceWait      bool
	PeriodicPromptID  *int64
	PositionPromptID  *int64
	WaitPromptID      *int64
	MinutesPromptID   *int64
	DigitsPrefix      string
	BreakoutKey       string
	BreakoutPromptID  *int64
	BreakoutVoicemail string
	Members           []QueueMember
}

const queueCols = `q.id, q.name, q.number, q.enabled, q.strategy, q.agent_timeout, q.retry_delay, q.wrap_up,
	q.max_callers, q.max_wait, q.join_empty, q.leave_empty,
	q.timeout_kind, q.timeout_target, q.full_kind, q.full_target, q.no_agents_kind, q.no_agents_target, q.moh_class_id,
	q.announce_frequency, q.announce_position, q.announce_wait,
	q.periodic_prompt_id, COALESCE(pp.name, ''), q.position_prompt_id, COALESCE(po.name, ''),
	q.wait_prompt_id, COALESCE(wp.name, ''), q.minutes_prompt_id, COALESCE(mp.name, ''), q.digits_prefix,
	q.breakout_key, q.breakout_prompt_id, COALESCE(bp.name, ''), q.breakout_voicemail, q.created_at, q.updated_at`

const queueFrom = ` FROM queues q
	LEFT JOIN announcements pp ON pp.id = q.periodic_prompt_id
	LEFT JOIN announcements po ON po.id = q.position_prompt_id
	LEFT JOIN announcements wp ON wp.id = q.wait_prompt_id
	LEFT JOIN announcements mp ON mp.id = q.minutes_prompt_id
	LEFT JOIN announcements bp ON bp.id = q.breakout_prompt_id `

func listQueues(ctx context.Context, q querier, where string, args ...any) ([]Queue, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+queueCols+queueFrom+where+` ORDER BY q.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Queue{}
	var ids []int64
	for rows.Next() {
		var x Queue
		if err := rows.Scan(&x.ID, &x.Name, &x.Number, &x.Enabled, &x.Strategy, &x.AgentTimeout, &x.RetryDelay, &x.WrapUp,
			&x.MaxCallers, &x.MaxWait, &x.JoinEmpty, &x.LeaveEmpty,
			&x.Timeout.Kind, &x.Timeout.Target, &x.Full.Kind, &x.Full.Target, &x.NoAgents.Kind, &x.NoAgents.Target, &x.MOHClassID,
			&x.AnnounceFrequency, &x.AnnouncePosition, &x.AnnounceWait,
			&x.PeriodicPromptID, &x.PeriodicPromptName, &x.PositionPromptID, &x.PositionPromptName,
			&x.WaitPromptID, &x.WaitPromptName, &x.MinutesPromptID, &x.MinutesPromptName, &x.DigitsPrefix,
			&x.BreakoutKey, &x.BreakoutPromptID, &x.BreakoutPromptName, &x.BreakoutVoicemail, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		x.Members = []QueueMember{}
		out = append(out, x)
		ids = append(ids, x.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	byID := map[int64]*Queue{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	mrows, err := q.QueryContext(ctx, `SELECT m.queue_id, m.extension_id, e.number, e.name, m.dynamic, m.penalty, m.position
		FROM queue_members m JOIN extensions e ON e.id = m.extension_id
		WHERE m.queue_id = ANY($1) ORDER BY m.queue_id, m.position`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = mrows.Close() }()
	for mrows.Next() {
		var (
			id int64
			m  QueueMember
		)
		if err := mrows.Scan(&id, &m.ExtensionID, &m.Number, &m.Name, &m.Dynamic, &m.Penalty, &m.Position); err != nil {
			return nil, err
		}
		byID[id].Members = append(byID[id].Members, m)
	}
	return out, mrows.Err()
}

// ListQueues returns every queue.
func (s *Store) ListQueues(ctx context.Context) ([]Queue, error) {
	return listQueues(ctx, s.q(ctx), "")
}

// GetQueue returns one queue.
func (s *Store) GetQueue(ctx context.Context, id int64) (Queue, error) {
	qs, err := listQueues(ctx, s.q(ctx), ` WHERE q.id = $1`, id)
	if err != nil {
		return Queue{}, err
	}
	if len(qs) == 0 {
		return Queue{}, ErrNotFound
	}
	return qs[0], nil
}

// QueueNameExists reports whether a queue with that name exists, for the
// settings that name one.
func (s *Store) QueueNameExists(ctx context.Context, name string) (bool, error) {
	var n int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM queues WHERE name = $1`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (in QueueInput) destinations() []ivrDest {
	return []ivrDest{{"timeout", in.Timeout}, {"full", in.Full}, {"noAgents", in.NoAgents}}
}

// checkQueue verifies the references of a queue before it is written.
func checkQueue(ctx context.Context, tx *sql.Tx, id int64, in QueueInput) error {
	if err := checkDialNumber(ctx, tx, "number", in.Number, numQueue); err != nil {
		return err
	}
	if in.BreakoutKey != "" {
		if in.BreakoutVoicemail == "" {
			return fieldError("breakoutVoicemail", "must name the extension whose box takes the message")
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voicemail_boxes b JOIN extensions e ON e.id = b.extension_id
			WHERE e.number = $1`, in.BreakoutVoicemail).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError("breakoutVoicemail", "extension "+in.BreakoutVoicemail+" has no voicemail box")
		}
	}
	if err := checkMOHClass(ctx, tx, "mohClassId", in.MOHClassID); err != nil {
		return err
	}
	for _, d := range in.destinations() {
		if d.Kind == IVRQueueDest && d.Target == in.Name {
			return fieldError(d.path+".target", "a queue cannot send its callers back to itself")
		}
		if err := checkIVRDestination(ctx, tx, d); err != nil {
			return err
		}
	}
	if err := checkQueueLoops(ctx, tx, id, in); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for i, m := range in.Members {
		if seen[m.ExtensionID] {
			return fieldError(fmt.Sprintf("members[%d].extensionId", i), "extension is listed twice")
		}
		seen[m.ExtensionID] = true
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM extensions WHERE id = $1`, m.ExtensionID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError(fmt.Sprintf("members[%d].extensionId", i), "extension does not exist")
		}
	}
	return nil
}

// checkQueueLoops refuses queues whose failover destinations lead back to
// the queue through other queues: a call would be passed round for ever.
func checkQueueLoops(ctx context.Context, tx *sql.Tx, id int64, in QueueInput) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, timeout_kind, timeout_target, full_kind, full_target,
		no_agents_kind, no_agents_target FROM queues WHERE id <> $1`, id)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	next := map[string][]string{}
	for rows.Next() {
		var name, k1, t1, k2, t2, k3, t3 string
		if err := rows.Scan(&name, &k1, &t1, &k2, &t2, &k3, &t3); err != nil {
			return err
		}
		for _, d := range [][2]string{{k1, t1}, {k2, t2}, {k3, t3}} {
			if d[0] == IVRQueueDest {
				next[name] = append(next[name], d[1])
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range in.destinations() {
		if d.Kind == IVRQueueDest && reaches(next, d.Target, in.Name, map[string]bool{}) {
			return fieldError(d.path+".target", fmt.Sprintf("queue %s would lead back to this queue through its failover destinations", d.Target))
		}
	}
	return nil
}

func writeQueueMembers(ctx context.Context, tx *sql.Tx, id int64, members []QueueMember) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM queue_members WHERE queue_id = $1`, id); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO queue_members (queue_id, extension_id, dynamic, penalty, position)
			VALUES ($1, $2, $3, $4, $5)`, id, m.ExtensionID, m.Dynamic, m.Penalty, m.Position); err != nil {
			return err
		}
	}
	return nil
}

// queueFK turns an unknown announcement id into a field error.
func queueFK(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		for _, f := range []struct{ frag, field string }{
			{"periodic_prompt", "periodicPromptId"}, {"position_prompt", "positionPromptId"},
			{"wait_prompt", "waitPromptId"}, {"minutes_prompt", "minutesPromptId"}, {"breakout_prompt", "breakoutPromptId"},
		} {
			if strings.Contains(pg.ConstraintName, f.frag) {
				return fieldError(f.field, "names an announcement that does not exist")
			}
		}
	}
	return err
}

const queueWriteCols = `name, number, enabled, strategy, agent_timeout, retry_delay, wrap_up, max_callers, max_wait,
	join_empty, leave_empty, timeout_kind, timeout_target, full_kind, full_target, no_agents_kind, no_agents_target,
	moh_class_id, announce_frequency, announce_position, announce_wait, periodic_prompt_id, position_prompt_id,
	wait_prompt_id, minutes_prompt_id, digits_prefix, breakout_key, breakout_prompt_id, breakout_voicemail`

func (in QueueInput) args() []any {
	return []any{in.Name, in.Number, in.Enabled, in.Strategy, in.AgentTimeout, in.RetryDelay, in.WrapUp, in.MaxCallers, in.MaxWait,
		in.JoinEmpty, in.LeaveEmpty, in.Timeout.Kind, in.Timeout.Target, in.Full.Kind, in.Full.Target, in.NoAgents.Kind, in.NoAgents.Target,
		in.MOHClassID, in.AnnounceFrequency, in.AnnouncePosition, in.AnnounceWait, in.PeriodicPromptID, in.PositionPromptID,
		in.WaitPromptID, in.MinutesPromptID, in.DigitsPrefix, in.BreakoutKey, in.BreakoutPromptID, in.BreakoutVoicemail}
}

// CreateQueue inserts a queue with its members.
func (s *Store) CreateQueue(ctx context.Context, actor string, in QueueInput, check Check) (Queue, error) {
	var out Queue
	err := s.configChange(ctx, actor, "create", "queue", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM queues`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxQueues {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d queues", MaxQueues)}
		}
		if err := checkQueue(ctx, tx, 0, in); err != nil {
			return 0, err
		}
		ph := make([]string, 29)
		for i := range ph {
			ph[i] = fmt.Sprintf("$%d", i+1)
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO queues (`+queueWriteCols+`) VALUES (`+strings.Join(ph, ",")+`) RETURNING id`,
			in.args()...).Scan(&id); err != nil {
			return 0, queueFK(err)
		}
		if err := writeQueueMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		qs, err := listQueues(ctx, tx, ` WHERE q.id = $1`, id)
		if err == nil && len(qs) == 1 {
			out = qs[0]
		}
		return id, err
	})
	return out, err
}

// queueReferences names what points at the queue name.
func queueReferences(ctx context.Context, tx *sql.Tx, id int64, name string) ([]string, error) {
	var refs []string
	for _, q := range []struct {
		query, label string
		args         []any
	}{
		{`SELECT name FROM inbound_routes WHERE destination_kind = 'queue' AND destination = $1`, "inbound route", []any{name}},
		{`SELECT name FROM ring_groups WHERE failure_kind = 'queue' AND failure_target = $1`, "ring group", []any{name}},
		{`SELECT name FROM ivr_menus WHERE (timeout_kind = 'queue' AND timeout_target = $1) OR (invalid_kind = 'queue' AND invalid_target = $1)
			OR EXISTS (SELECT 1 FROM ivr_menu_options o WHERE o.menu_id = ivr_menus.id AND o.dest_kind = 'queue' AND o.dest_target = $1)`, "IVR menu", []any{name}},
		{`SELECT name FROM queues WHERE id <> $2 AND ((timeout_kind = 'queue' AND timeout_target = $1)
			OR (full_kind = 'queue' AND full_target = $1) OR (no_agents_kind = 'queue' AND no_agents_target = $1))`, "queue", []any{name, id}},
		{`SELECT name FROM time_conditions WHERE (open_kind = 'queue' AND open_target = $1)
			OR (closed_kind = 'queue' AND closed_target = $1) OR (holiday_kind = 'queue' AND holiday_target = $1)`, "time condition", []any{name}},
	} {
		rows, err := tx.QueryContext(ctx, q.query, q.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return nil, err
			}
			refs = append(refs, q.label+" "+n)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// UpdateQueue replaces a queue. Renaming a queue that something points at is
// refused: the references name it.
func (s *Store) UpdateQueue(ctx context.Context, actor string, id int64, in QueueInput, check Check) (Queue, error) {
	var out Queue
	err := s.configChange(ctx, actor, "update", "queue", check, func(tx *sql.Tx) (int64, error) {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM queues WHERE id = $1 FOR UPDATE`, id).Scan(&cur); err != nil {
			return id, err
		}
		if cur != in.Name {
			refs, err := queueReferences(ctx, tx, id, cur)
			if err != nil {
				return id, err
			}
			if len(refs) > 0 {
				return id, inUseBy("queue "+cur+", which cannot be renamed while it", refs)
			}
		}
		if err := checkQueue(ctx, tx, id, in); err != nil {
			return id, err
		}
		sets := strings.Split(queueWriteCols, ",")
		for i := range sets {
			sets[i] = fmt.Sprintf("%s = $%d", strings.TrimSpace(sets[i]), i+2)
		}
		//nolint:gosec // G202: the SET list is built from the constant queueWriteCols
		if _, err := tx.ExecContext(ctx, `UPDATE queues SET `+strings.Join(sets, ", ")+`, updated_at = now() WHERE id = $1`,
			append([]any{id}, in.args()...)...); err != nil {
			return id, queueFK(err)
		}
		if err := writeQueueMembers(ctx, tx, id, in.Members); err != nil {
			return id, err
		}
		qs, err := listQueues(ctx, tx, ` WHERE q.id = $1`, id)
		if err == nil && len(qs) == 1 {
			out = qs[0]
		}
		return id, err
	})
	return out, err
}

// DeleteQueue removes a queue and, by cascade, its members. A queue that an
// inbound route, ring group, IVR menu or another queue names is refused.
func (s *Store) DeleteQueue(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "queue", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM queues WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		refs, err := queueReferences(ctx, tx, id, name)
		if err != nil {
			return id, err
		}
		if len(refs) > 0 {
			return id, inUseBy("queue "+name, refs)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM queues WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}
