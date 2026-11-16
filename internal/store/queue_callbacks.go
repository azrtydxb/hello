package store

// Queue callback requests (migration 00049, pbx-gaps S-9 and S-10): the
// durable rows hello-sip's sweeps claim, and the operator's list and cancel.
// Unlike the queue configuration these rows change at call rate, so they are
// not configuration changes: no revision bump, no audit row per attempt. An
// operator's cancel is audited by the API handler's request log.

import (
	"context"
	"database/sql"
	"errors"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/queue"
)

// execer is what the settle step needs from a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// QueueCallbacks is the PostgreSQL queue.CallbackStore. Every time it reads
// is the database's clock (now()), so nodes with skewed clocks agree on
// leases and due times.
type QueueCallbacks struct{ db *dbPool }

var _ queue.CallbackStore = (*QueueCallbacks)(nil)

// QueueCallbacks returns the callback store over the same database.
func (s *Store) QueueCallbacks() *QueueCallbacks { return &QueueCallbacks{db: s.db} }

const callbackCols = `id, queue_id, number, caller_name, status, attempts, position_key, next_attempt_at,
	claimed_by, lease_until, agent, result, created_at, expires_at, finished_at`

func scanCallback(r interface{ Scan(...any) error }) (queue.Callback, error) {
	var (
		c            queue.Callback
		lease, final sql.NullTime
	)
	if err := r.Scan(&c.ID, &c.QueueID, &c.Number, &c.CallerName, &c.Status, &c.Attempts, &c.PositionKey, &c.NextAttemptAt,
		&c.ClaimedBy, &lease, &c.Agent, &c.Result, &c.CreatedAt, &c.ExpiresAt, &final); err != nil {
		return queue.Callback{}, err
	}
	c.LeaseUntil, c.FinishedAt = lease.Time, final.Time
	return c, nil
}

// settle expires the open requests that ran out of time and fails the ones
// whose last attempt lost its lease.
func (q *QueueCallbacks) settle(ctx context.Context, db execer, queueID int64) error {
	if _, err := db.ExecContext(ctx, `UPDATE queue_callbacks SET status = 'expired', result = 'expired', finished_at = now(),
		claimed_by = '', lease_until = NULL
		WHERE queue_id = @queue_id AND status IN ('waiting', 'dialling') AND expires_at <= now()`, momo.Named("queue_id", queueID)); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE queue_callbacks SET status = 'failed', result = 'node_lost', finished_at = now(),
		claimed_by = '', lease_until = NULL
		WHERE queue_id = @queue_id AND status = 'dialling' AND lease_until < now() AND attempts >= @max_attempts`, momo.Named("queue_id", queueID), momo.Named("max_attempts", queue.CallbackMaxAttempts))
	return err
}

// Request implements queue.CallbackStore.
func (q *QueueCallbacks) Request(ctx context.Context, queueID int64, number, name string, positionKey int64) (queue.Callback, bool, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return queue.Callback{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialise the requests of one queue, so the cap and the coalescing
	// cannot be raced past by two callers at once.
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM queues WHERE id = @id FOR UPDATE`, momo.Named("id", queueID)); err != nil {
		return queue.Callback{}, false, err
	}
	if err := q.settle(ctx, tx, queueID); err != nil {
		return queue.Callback{}, false, err
	}
	row := tx.QueryRowContext(ctx, `SELECT `+callbackCols+` FROM queue_callbacks
		WHERE queue_id = @queue_id AND number = @number AND status IN ('waiting', 'dialling')`, momo.Named("queue_id", queueID), momo.Named("number", number))
	if c, err := scanCallback(row); err == nil {
		return c, false, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return queue.Callback{}, false, err
	}
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM queue_callbacks
		WHERE queue_id = @queue_id AND status IN ('waiting', 'dialling')`, momo.Named("queue_id", queueID)).Scan(&open); err != nil {
		return queue.Callback{}, false, err
	}
	if open >= queue.CallbackMaxWaiting {
		return queue.Callback{}, false, queue.ErrCallbackFull
	}
	c, err := scanCallback(tx.QueryRowContext(ctx, `INSERT INTO queue_callbacks (queue_id, number, caller_name, position_key, expires_at)
		VALUES (@queue_id, @number, @caller_name, @position_key, now() + make_interval(secs => @ttl)) RETURNING `+callbackCols,
		momo.Named("queue_id", queueID), momo.Named("number", number), momo.Named("caller_name", name), momo.Named("position_key", positionKey), momo.Named("ttl", queue.CallbackTTL.Seconds())))
	if err != nil {
		return queue.Callback{}, false, err
	}
	return c, true, tx.Commit()
}

// callbackDue binds @queue_id.
const callbackDue = `queue_id = @queue_id AND ((status = 'waiting' AND next_attempt_at <= now())
	OR (status = 'dialling' AND lease_until < now()))`

// Head implements queue.CallbackStore.
func (q *QueueCallbacks) Head(ctx context.Context, queueID int64) (queue.Callback, bool, error) {
	if err := q.settle(ctx, q.db, queueID); err != nil {
		return queue.Callback{}, false, err
	}
	c, err := scanCallback(q.db.QueryRowContext(ctx, `SELECT `+callbackCols+` FROM queue_callbacks
		WHERE `+callbackDue+` ORDER BY position_key, id LIMIT 1`, momo.Named("queue_id", queueID)))
	if errors.Is(err, sql.ErrNoRows) {
		return queue.Callback{}, false, nil
	}
	return c, err == nil, err
}

// Claim implements queue.CallbackStore. FOR UPDATE SKIP LOCKED means two
// nodes sweeping at once take different rows (or one takes none), never the
// same one.
func (q *QueueCallbacks) Claim(ctx context.Context, queueID int64, node string) (queue.Callback, bool, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return queue.Callback{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := q.settle(ctx, tx, queueID); err != nil {
		return queue.Callback{}, false, err
	}
	c, err := scanCallback(tx.QueryRowContext(ctx, `UPDATE queue_callbacks SET status = 'dialling', attempts = attempts + 1,
		claimed_by = @node, lease_until = now() + make_interval(secs => @lease)
		WHERE id = (SELECT id FROM queue_callbacks WHERE `+callbackDue+`
			ORDER BY position_key, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING `+callbackCols, momo.Named("queue_id", queueID), momo.Named("node", node), momo.Named("lease", queue.CallbackLease.Seconds())))
	if errors.Is(err, sql.ErrNoRows) {
		return queue.Callback{}, false, tx.Commit()
	}
	if err != nil {
		return queue.Callback{}, false, err
	}
	return c, true, tx.Commit()
}

// Renew implements queue.CallbackStore.
func (q *QueueCallbacks) Renew(ctx context.Context, id int64, node string) error {
	res, err := q.db.ExecContext(ctx, `UPDATE queue_callbacks SET claimed_by = @node, lease_until = now() + make_interval(secs => @lease)
		WHERE id = @id AND status = 'dialling'`, momo.Named("id", id), momo.Named("node", node), momo.Named("lease", queue.CallbackLease.Seconds()))
	if err != nil {
		return err
	}
	return callbackRow(res)
}

func callbackRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return queue.ErrCallbackNotFound
	}
	return nil
}

// Fail implements queue.CallbackStore.
func (q *QueueCallbacks) Fail(ctx context.Context, id int64, node, result string) (bool, error) {
	var status string
	err := q.db.QueryRowContext(ctx, `UPDATE queue_callbacks SET result = @result, claimed_by = '', lease_until = NULL,
		status = CASE WHEN attempts >= @max_attempts THEN 'failed' ELSE 'waiting' END,
		next_attempt_at = now() + make_interval(secs => @retry_after),
		finished_at = CASE WHEN attempts >= @max_attempts THEN now() END
		WHERE id = @id AND status = 'dialling' AND claimed_by = @node RETURNING status`,
		momo.Named("id", id), momo.Named("node", node), momo.Named("result", result), momo.Named("max_attempts", queue.CallbackMaxAttempts), momo.Named("retry_after", queue.CallbackRetryAfter.Seconds())).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, queue.ErrCallbackNotFound
	}
	return status == queue.CallbackFailed, err
}

// Connect implements queue.CallbackStore.
func (q *QueueCallbacks) Connect(ctx context.Context, id int64, node, agent string) error {
	res, err := q.db.ExecContext(ctx, `UPDATE queue_callbacks SET status = 'connected', agent = @agent, result = 'connected',
		claimed_by = '', lease_until = NULL, finished_at = now()
		WHERE id = @id AND status = 'dialling' AND claimed_by = @node`, momo.Named("id", id), momo.Named("node", node), momo.Named("agent", agent))
	if err != nil {
		return err
	}
	return callbackRow(res)
}

// Cancel implements queue.CallbackStore.
func (q *QueueCallbacks) Cancel(ctx context.Context, queueID, id int64) error {
	res, err := q.db.ExecContext(ctx, `UPDATE queue_callbacks SET status = 'cancelled', result = 'cancelled',
		claimed_by = '', lease_until = NULL, finished_at = now()
		WHERE id = @id AND queue_id = @queue_id AND status IN ('waiting', 'dialling')`, momo.Named("id", id), momo.Named("queue_id", queueID))
	if err != nil {
		return err
	}
	return callbackRow(res)
}

// Due implements queue.CallbackStore.
func (q *QueueCallbacks) Due(ctx context.Context) (map[int64]int64, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT queue_id, min(position_key) FROM queue_callbacks
		WHERE (status = 'waiting' AND next_attempt_at <= now() AND expires_at > now())
		   OR (status = 'dialling' AND expires_at > now())
		GROUP BY queue_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]int64{}
	for rows.Next() {
		var id, key int64
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, rows.Err()
}

// Dialling implements queue.CallbackStore.
func (q *QueueCallbacks) Dialling(ctx context.Context, queueID int64) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT count(*) FROM queue_callbacks
		WHERE queue_id = @queue_id AND status = 'dialling' AND lease_until >= now()`, momo.Named("queue_id", queueID)).Scan(&n)
	return n, err
}

// List implements queue.CallbackStore.
func (q *QueueCallbacks) List(ctx context.Context, queueID int64) ([]queue.Callback, error) {
	if err := q.settle(ctx, q.db, queueID); err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, `SELECT `+callbackCols+` FROM queue_callbacks WHERE queue_id = @queue_id
		ORDER BY id DESC LIMIT 500`, momo.Named("queue_id", queueID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []queue.Callback{}
	for rows.Next() {
		c, err := scanCallback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Purge implements queue.CallbackStore.
func (q *QueueCallbacks) Purge(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM queue_callbacks
		WHERE finished_at IS NOT NULL AND finished_at < now() - make_interval(secs => @keep)`, momo.Named("keep", queue.CallbackKeep.Seconds()))
	return err
}

// CallbackExists reports whether the queue has a request with that id, for
// the API's 404.
func (s *Store) CallbackExists(ctx context.Context, queueID, id int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM queue_callbacks WHERE id = @id AND queue_id = @queue_id`, momo.Named("id", id), momo.Named("queue_id", queueID)).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// QueuePromptLanguages lists the languages queues have set for themselves,
// sorted, with no duplicates and no empty value. The static prompts of each
// are pre-generated.
func (s *Store) QueuePromptLanguages(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT prompt_language FROM queues WHERE prompt_language IS NOT NULL AND prompt_language <> '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
