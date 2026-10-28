package store

// Configuration batches (spec flow-canvas S-13, S-14). A batch runs many
// configuration changes in one transaction: every store write made while the
// batch's context is in use joins the batch's transaction instead of opening
// its own, the whole-configuration check runs once at the end, and the
// revision is bumped and hello-sip notified once. A failure anywhere rolls
// everything back, so nothing is applied, no audit row is written and the
// revision does not move.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
)

// BatchViaPrefix is the audit via of every change a batch makes:
// "batch:<uuid>". The outer origin (console, an MCP client, an AI proposal)
// is stored on the config_batches row.
const BatchViaPrefix = "batch:"

// BatchRetention is how long applied batches are kept.
const BatchRetention = 90 * 24 * time.Hour

// dbq is what both *sql.DB and *sql.Tx offer.
type dbq interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// q is the handle a query made under ctx runs on: the open batch's
// transaction, so a later operation of a batch sees the earlier ones, or the
// pool.
func (s *Store) q(ctx context.Context) dbq {
	if b := s.batchOf(ctx); b != nil {
		return b.tx
	}
	return s.db
}

type batchKey struct{}

// BatchFrom returns the batch whose transaction ctx belongs to.
func BatchFrom(ctx context.Context) *Batch {
	b, _ := ctx.Value(batchKey{}).(*Batch)
	return b
}

func (s *Store) batchOf(ctx context.Context) *Batch {
	if b := BatchFrom(ctx); b != nil && b.s == s {
		return b
	}
	return nil
}

type batchTouch struct {
	op       int
	resource string
	id       string
}

// Batch is one open configuration batch.
type Batch struct {
	s        *Store
	tx       *sql.Tx
	id       string
	revision int64
	op       int
	checks   []Check
	checkPtr map[uintptr]bool
	dirty    bool
	touches  []batchTouch
	baseline routing.Config
}

// ID is the batch's uuid.
func (b *Batch) ID() string { return b.id }

// Revision is the configuration revision when the batch took the lock.
func (b *Batch) Revision() int64 { return b.revision }

// SetOp records which operation of the batch is running, so an error found
// by the final check can name it.
func (b *Batch) SetOp(i int) { b.op = i }

// Changed reports whether any operation changed the configuration.
func (b *Batch) Changed() bool { return b.dirty }

func (b *Batch) record(check Check, resource, id string) {
	b.dirty = true
	b.touches = append(b.touches, batchTouch{op: b.op, resource: resource, id: id})
	if check == nil {
		return
	}
	p := reflect.ValueOf(check).Pointer()
	if b.checkPtr[p] {
		return
	}
	b.checkPtr[p] = true
	b.checks = append(b.checks, check)
}

// BatchRecord is what a batch stores about itself in config_batches.
type BatchRecord struct {
	Actor, Via   string
	BaseRevision int64
	Ops, Results json.RawMessage
	// Reverts is the batch this one undoes, or "".
	Reverts string
}

// BatchOutcome is the result of RunBatch.
type BatchOutcome struct {
	// ID is the stored batch's id; empty for a dry run.
	ID string
	// Revision is the configuration revision after the batch (the current
	// one for a dry run or a batch that changed nothing).
	Revision int64
	Applied  bool
}

// BatchFieldError is one error the batch's final check found. OpIndex is the
// operation that changed the item it is about, or -1 when none can be named.
type BatchFieldError struct {
	OpIndex int
	Path    string
	Message string
}

// BatchValidationError rejects a batch whose final configuration has errors
// the starting one did not.
type BatchValidationError struct{ Fields []BatchFieldError }

func (e *BatchValidationError) Error() string { return "store: batch validation failed" }

// ErrBatchNested refuses a batch started inside a batch.
var ErrBatchNested = errors.New("store: a batch cannot start another batch")

// RunBatch opens one transaction under the configuration lock and calls fn
// with a context whose store writes join it. fn returns the record to store.
// A dry run (or an fn error) rolls back; otherwise the whole-configuration
// check runs against the baseline taken before fn, the revision is bumped
// once when anything changed, and the batch row is written.
func (s *Store) RunBatch(ctx context.Context, dryRun bool, fn func(context.Context, *Batch) (BatchRecord, error)) (BatchOutcome, error) {
	if BatchFrom(ctx) != nil {
		return BatchOutcome{}, ErrBatchNested
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(configLockKey)); err != nil {
		return BatchOutcome{}, err
	}
	b := &Batch{s: s, tx: tx, checkPtr: map[uintptr]bool{}}
	if err := tx.QueryRowContext(ctx, `SELECT gen_random_uuid()::text, config_revision FROM schema_info`).Scan(&b.id, &b.revision); err != nil {
		return BatchOutcome{}, err
	}
	snap, err := s.loadRouting(ctx, tx)
	if err != nil {
		return BatchOutcome{}, err
	}
	b.baseline = snap.Config
	rec, err := fn(context.WithValue(ctx, batchKey{}, b), b)
	if err != nil {
		return BatchOutcome{}, err
	}
	if err := b.finalCheck(ctx); err != nil {
		return BatchOutcome{}, err
	}
	out := BatchOutcome{Revision: b.revision}
	if dryRun {
		return out, nil
	}
	if b.dirty {
		var rev int64
		if err := tx.QueryRowContext(ctx,
			`UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision`).Scan(&rev); err != nil {
			return BatchOutcome{}, err
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, NotifyChannel, itoa(rev)); err != nil {
			return BatchOutcome{}, err
		}
		out.Revision = rev
	}
	var reverts any
	if rec.Reverts != "" {
		reverts = rec.Reverts
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO config_batches (id, actor, via, base_revision, revision, ops, results, reverts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		b.id, rec.Actor, rec.Via, rec.BaseRevision, out.Revision, []byte(rec.Ops), []byte(rec.Results), reverts); err != nil {
		return BatchOutcome{}, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM config_batches WHERE created_at < now() - make_interval(secs => $1)`,
		BatchRetention.Seconds()); err != nil {
		return BatchOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return BatchOutcome{}, err
	}
	out.ID, out.Applied = b.id, true
	return out, nil
}

// finalCheck runs each whole-configuration check the batch's changes asked
// for, once, and fails with the errors the batch introduced.
func (b *Batch) finalCheck(ctx context.Context) error {
	if len(b.checks) == 0 {
		return nil
	}
	snap, err := b.s.loadRouting(ctx, b.tx)
	if err != nil {
		return err
	}
	var fresh []routing.FieldError
	seen := map[string]bool{}
	for _, check := range b.checks {
		baseline := errorKeys(b.baseline, check(b.baseline))
		for _, f := range check(snap.Config) {
			k := errorKey(snap.Config, f)
			if baseline[k] || seen[k+"|"+f.Message] {
				continue
			}
			seen[k+"|"+f.Message] = true
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	prefixes := make([]string, len(b.touches))
	for i, t := range b.touches {
		if prefixes[i], err = configPath(ctx, b.tx, snap.Config, t.resource, t.id); err != nil {
			return err
		}
	}
	out := &BatchValidationError{}
	for _, f := range fresh {
		fe := BatchFieldError{OpIndex: -1, Path: f.Path, Message: f.Message}
		for i := len(b.touches) - 1; i >= 0; i-- {
			p := prefixes[i]
			if p == "" {
				continue
			}
			if rest, ok := strings.CutPrefix(f.Path, p+"."); ok {
				fe.OpIndex, fe.Path = b.touches[i].op, rest
				break
			}
			if f.Path == p {
				fe.OpIndex = b.touches[i].op
				break
			}
		}
		out.Fields = append(out.Fields, fe)
	}
	return out
}

// ConfigBatch is a stored batch.
type ConfigBatch struct {
	ID           string          `json:"id"`
	Actor        string          `json:"actor"`
	Via          string          `json:"via"`
	BaseRevision int64           `json:"baseRevision"`
	Revision     int64           `json:"revision"`
	OpCount      int             `json:"opCount"`
	Reverts      string          `json:"reverts,omitempty"`
	RevertedBy   string          `json:"revertedBy,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	Ops          json.RawMessage `json:"ops,omitempty"`
	Results      json.RawMessage `json:"results,omitempty"`
}

const configBatchCols = `b.id::text, b.actor, b.via, b.base_revision, b.revision, jsonb_array_length(b.ops),
	coalesce(b.reverts::text, ''),
	coalesce((SELECT r.id::text FROM config_batches r WHERE r.reverts = b.id ORDER BY r.created_at LIMIT 1), ''),
	b.created_at`

func scanConfigBatch(r interface{ Scan(...any) error }, extra ...any) (ConfigBatch, error) {
	var c ConfigBatch
	dst := append([]any{&c.ID, &c.Actor, &c.Via, &c.BaseRevision, &c.Revision, &c.OpCount, &c.Reverts, &c.RevertedBy, &c.CreatedAt}, extra...)
	err := r.Scan(dst...)
	return c, err
}

// ListConfigBatches returns the newest batches first, without their ops and
// results. before, when non-zero, keeps only batches older than it.
func (s *Store) ListConfigBatches(ctx context.Context, before time.Time, limit int) ([]ConfigBatch, error) {
	var cutoff any
	if !before.IsZero() {
		cutoff = before
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+configBatchCols+` FROM config_batches b
		WHERE ($1::timestamptz IS NULL OR b.created_at < $1) ORDER BY b.created_at DESC, b.id LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ConfigBatch{}
	for rows.Next() {
		c, err := scanConfigBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConfigBatch returns one batch with its ops and results; ErrNotFound for
// an unknown id (or one that is not a uuid).
func (s *Store) GetConfigBatch(ctx context.Context, id string) (ConfigBatch, error) {
	if !validUUID(id) {
		return ConfigBatch{}, ErrNotFound
	}
	var ops, results []byte
	c, err := scanConfigBatch(s.q(ctx).QueryRowContext(ctx, `SELECT `+configBatchCols+`, b.ops, b.results
		FROM config_batches b WHERE b.id = $1`, id), &ops, &results)
	if err != nil {
		return ConfigBatch{}, mapErr(err)
	}
	c.Ops, c.Results = ops, results
	return c, nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F'):
			return false
		}
	}
	return true
}

// Layout in a batch (spec S-7, S-14).

// NodeLayout is where one node sits on one canvas.
type NodeLayout struct {
	CanvasID  int64   `json:"canvasId"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Collapsed bool    `json:"collapsed,omitempty"`
}

// NodeLayouts returns every canvas position of a node, so a delete can be
// reverted with the node back where it was.
func (s *Store) NodeLayouts(ctx context.Context, node string) ([]NodeLayout, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT canvas_id, x, y, collapsed FROM flow_canvas_nodes WHERE node_id = $1 ORDER BY canvas_id`, node)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []NodeLayout
	for rows.Next() {
		var l NodeLayout
		if err := rows.Scan(&l.CanvasID, &l.X, &l.Y, &l.Collapsed); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PlaceNode saves where a node sits on each canvas in ls and moves those
// canvases' layout version. A canvas that does not exist is ErrNotFound.
// Like every layout write it is not a configuration change.
func (s *Store) PlaceNode(ctx context.Context, actor, node string, ls []NodeLayout) error {
	for _, l := range ls {
		var n int
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM flow_canvas_nodes WHERE canvas_id = $1`, l.CanvasID).Scan(&n); err != nil {
			return mapErr(err)
		}
		if n >= MaxFlowCanvasNodes {
			return &InUseError{Msg: "too many nodes on one canvas"}
		}
		res, err := s.q(ctx).ExecContext(ctx, `UPDATE flow_canvases SET layout_version = layout_version + 1, updated_at = now() WHERE id = $1`, l.CanvasID)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO flow_canvas_nodes (canvas_id, node_id, x, y, collapsed) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (canvas_id, node_id) DO UPDATE SET x = EXCLUDED.x, y = EXCLUDED.y, collapsed = EXCLUDED.collapsed`,
			l.CanvasID, node, l.X, l.Y, l.Collapsed); err != nil {
			return mapErr(err)
		}
		if err := insertAudit(ctx, s.q(ctx), actor, "layout", "flow_canvas", itoa(l.CanvasID)); err != nil {
			return err
		}
	}
	return nil
}

// PruneLayout deletes the positions of nodes that are not in live, on every
// canvas, and moves the layout version of each canvas it changed. It returns
// how many positions it removed.
func (s *Store) PruneLayout(ctx context.Context, live []string) (int, error) {
	if live == nil {
		live = []string{}
	}
	var n int
	err := s.q(ctx).QueryRowContext(ctx, `WITH del AS (
			DELETE FROM flow_canvas_nodes WHERE NOT (node_id = ANY($1::text[])) RETURNING canvas_id
		), bump AS (
			UPDATE flow_canvases SET layout_version = layout_version + 1, updated_at = now()
			WHERE id IN (SELECT canvas_id FROM del) RETURNING id
		) SELECT count(*) FROM del`, live).Scan(&n)
	return n, mapErr(err)
}
