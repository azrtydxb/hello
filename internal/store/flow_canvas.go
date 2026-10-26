package store

// Flow canvases (spec flow-canvas S-7): named, shared views of the call flow
// that hold only node positions. A canvas is not configuration, so none of
// these writes goes through configChange: no revision bump and no notify to
// hello-sip. Each write is audited in its own transaction.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ErrLayoutStale is returned by PutFlowLayout when the canvas's layout moved
// since the caller read it.
var ErrLayoutStale = errors.New("store: flow layout is stale")

// MaxFlowCanvases and MaxFlowCanvasNodes bound the stored layout.
const (
	MaxFlowCanvases    = 200
	MaxFlowCanvasNodes = 5000
)

// FlowCanvas is a named view of the call flow.
type FlowCanvas struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Viewport      json.RawMessage `json:"viewport,omitempty"`
	LayoutVersion int64           `json:"layoutVersion"`
	NodeCount     int             `json:"nodeCount"`
	CreatedBy     string          `json:"createdBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// FlowCanvasNode is where one node sits on a canvas.
type FlowCanvasNode struct {
	Node      string  `json:"node"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Collapsed bool    `json:"collapsed"`
}

// FlowCanvasChange is a partial update of a canvas; nil fields keep their
// value.
type FlowCanvasChange struct {
	Name        *string
	Description *string
	Viewport    *json.RawMessage
}

const flowCanvasCols = `c.id, c.name, c.description, c.viewport, c.layout_version,
	(SELECT count(*) FROM flow_canvas_nodes n WHERE n.canvas_id = c.id),
	c.created_by, c.created_at, c.updated_at`

func scanFlowCanvas(r interface{ Scan(...any) error }) (FlowCanvas, error) {
	var c FlowCanvas
	var vp []byte
	err := r.Scan(&c.ID, &c.Name, &c.Description, &vp, &c.LayoutVersion, &c.NodeCount, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if len(vp) > 0 {
		c.Viewport = json.RawMessage(vp)
	}
	return c, err
}

// ListFlowCanvases returns every canvas by name.
func (s *Store) ListFlowCanvases(ctx context.Context) ([]FlowCanvas, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+flowCanvasCols+` FROM flow_canvases c ORDER BY lower(c.name), c.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FlowCanvas{}
	for rows.Next() {
		c, err := scanFlowCanvas(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetFlowCanvas returns one canvas and the positions saved on it.
func (s *Store) GetFlowCanvas(ctx context.Context, id int64) (FlowCanvas, []FlowCanvasNode, error) {
	c, err := scanFlowCanvas(s.db.QueryRowContext(ctx, `SELECT `+flowCanvasCols+` FROM flow_canvases c WHERE c.id = $1`, id))
	if err != nil {
		return c, nil, mapErr(err)
	}
	nodes, err := s.flowCanvasNodes(ctx, s.db, id)
	return c, nodes, err
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) flowCanvasNodes(ctx context.Context, q queryer, id int64) ([]FlowCanvasNode, error) {
	rows, err := q.QueryContext(ctx, `SELECT node_id, x, y, collapsed FROM flow_canvas_nodes WHERE canvas_id = $1 ORDER BY node_id`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FlowCanvasNode{}
	for rows.Next() {
		var n FlowCanvasNode
		if err := rows.Scan(&n.Node, &n.X, &n.Y, &n.Collapsed); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CreateFlowCanvas inserts an empty canvas.
func (s *Store) CreateFlowCanvas(ctx context.Context, actor, name, description string) (FlowCanvas, error) {
	var c FlowCanvas
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM flow_canvases`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxFlowCanvases {
			return &InUseError{Msg: "too many canvases"}
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO flow_canvases (name, description, created_by) VALUES ($1, $2, $3) RETURNING id`,
			name, description, actor).Scan(&id); err != nil {
			return err
		}
		var err error
		c, err = scanFlowCanvas(tx.QueryRowContext(ctx, `SELECT `+flowCanvasCols+` FROM flow_canvases c WHERE c.id = $1`, id))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "flow_canvas", itoa(id))
	})
	return c, mapErr(err)
}

// UpdateFlowCanvas changes a canvas's name, description or saved view.
func (s *Store) UpdateFlowCanvas(ctx context.Context, actor string, id int64, ch FlowCanvasChange) (FlowCanvas, error) {
	var c FlowCanvas
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var viewport any
		setVP := ch.Viewport != nil
		if setVP {
			viewport = []byte(*ch.Viewport)
		}
		res, err := tx.ExecContext(ctx, `UPDATE flow_canvases SET
			name = COALESCE($2, name), description = COALESCE($3, description),
			viewport = CASE WHEN $4 THEN $5::jsonb ELSE viewport END, updated_at = now()
			WHERE id = $1`, id, ch.Name, ch.Description, setVP, viewport)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		c, err = scanFlowCanvas(tx.QueryRowContext(ctx, `SELECT `+flowCanvasCols+` FROM flow_canvases c WHERE c.id = $1`, id))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "flow_canvas", itoa(id))
	})
	return c, mapErr(err)
}

// DeleteFlowCanvas removes a canvas and its layout. The configuration the
// canvas drew is untouched.
func (s *Store) DeleteFlowCanvas(ctx context.Context, actor string, id int64) error {
	return mapErr(s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM flow_canvases WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "flow_canvas", itoa(id))
	}))
}

// FlowLayoutWrite is one layout save: positions to set and nodes to drop.
// Keep, when non-nil, is the set of node ids that still exist; every other
// saved node is pruned (spec S-7).
type FlowLayoutWrite struct {
	LayoutVersion int64
	Nodes         []FlowCanvasNode
	Remove        []string
	Keep          map[string]bool
}

// PutFlowLayout saves positions on a canvas. It returns ErrLayoutStale when
// the layout version moved since the caller read it, and otherwise the
// canvas with its new version and the saved nodes. The audit row is the only
// other write: this is not a configuration change.
func (s *Store) PutFlowLayout(ctx context.Context, actor string, id int64, w FlowLayoutWrite) (FlowCanvas, []FlowCanvasNode, error) {
	var c FlowCanvas
	var nodes []FlowCanvasNode
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var cur int64
		if err := tx.QueryRowContext(ctx, `SELECT layout_version FROM flow_canvases WHERE id = $1 FOR UPDATE`, id).Scan(&cur); err != nil {
			return err
		}
		if cur != w.LayoutVersion {
			return ErrLayoutStale
		}
		for _, n := range w.Nodes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO flow_canvas_nodes (canvas_id, node_id, x, y, collapsed)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (canvas_id, node_id) DO UPDATE SET x = EXCLUDED.x, y = EXCLUDED.y, collapsed = EXCLUDED.collapsed`,
				id, n.Node, n.X, n.Y, n.Collapsed); err != nil {
				return err
			}
		}
		for _, n := range w.Remove {
			if _, err := tx.ExecContext(ctx, `DELETE FROM flow_canvas_nodes WHERE canvas_id = $1 AND node_id = $2`, id, n); err != nil {
				return err
			}
		}
		if w.Keep != nil {
			saved, err := s.flowCanvasNodes(ctx, tx, id)
			if err != nil {
				return err
			}
			for _, n := range saved {
				if w.Keep[n.Node] {
					continue
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM flow_canvas_nodes WHERE canvas_id = $1 AND node_id = $2`, id, n.Node); err != nil {
					return err
				}
			}
		}
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM flow_canvas_nodes WHERE canvas_id = $1`, id).Scan(&total); err != nil {
			return err
		}
		if total > MaxFlowCanvasNodes {
			return &InUseError{Msg: "too many nodes on one canvas"}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE flow_canvases SET layout_version = layout_version + 1, updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		var err error
		c, err = scanFlowCanvas(tx.QueryRowContext(ctx, `SELECT `+flowCanvasCols+` FROM flow_canvases c WHERE c.id = $1`, id))
		if err != nil {
			return err
		}
		if nodes, err = s.flowCanvasNodes(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "layout", "flow_canvas", itoa(id))
	})
	if errors.Is(err, ErrLayoutStale) {
		return c, nil, err
	}
	return c, nodes, mapErr(err)
}
