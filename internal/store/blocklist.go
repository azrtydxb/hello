package store

// The call blocklist (migration 00042, pbx-gaps S-7): numbers an operator
// blocks or allows, globally or for one trunk. Every mutation runs through
// configChange, so it is audited (ids only), bumps the configuration
// revision and reaches every hello-sip node as a snapshot; the SIP path never
// reads the table.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBlocklistFull is returned when a change would take the blocklist past
// routing.MaxBlocklistEntries.
var ErrBlocklistFull = errors.New("store: blocklist is full")

// BlocklistEntry is a blocklist row as the API shows it.
type BlocklistEntry struct {
	ID      int64  `json:"id"`
	Pattern string `json:"pattern"`
	Match   string `json:"match"`
	Action  string `json:"action"`
	// TrunkID is the trunk the entry applies to; null is every trunk.
	TrunkID   *int64 `json:"trunkId"`
	TrunkName string `json:"trunkName"`
	// Reason is the administrators' note; the API shows it to admins only.
	Reason         string    `json:"reason"`
	AnnouncementID *int64    `json:"announcementId"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// BlocklistInput is the writable part of an entry. Pattern is already in its
// stored form (routing.NormalizeBlockPattern).
type BlocklistInput struct {
	Pattern        string
	Match          string
	Action         string
	TrunkID        *int64
	Reason         string
	AnnouncementID *int64
	Enabled        bool
}

// Input returns e's writable fields.
func (e BlocklistEntry) Input() BlocklistInput {
	return BlocklistInput{Pattern: e.Pattern, Match: e.Match, Action: e.Action, TrunkID: e.TrunkID,
		Reason: e.Reason, AnnouncementID: e.AnnouncementID, Enabled: e.Enabled}
}

// BlocklistFilter narrows a listing; the zero value lists everything.
type BlocklistFilter struct {
	// Q keeps entries whose pattern contains it.
	Q string
	// Action keeps one action ("block" or "allow").
	Action string
	// Scope keeps entries of one scope: "" every scope, "global", or a trunk
	// id in decimal.
	Scope string
	// After lists entries with an id above it; Limit is the page size.
	After int64
	Limit int
}

const blocklistCols = `b.id, b.pattern, b.match, b.action, b.trunk_id, coalesce(t.name, ''), b.reason,
	b.announcement_id, b.enabled, b.created_at, b.updated_at`

const blocklistFrom = ` FROM call_blocklist b LEFT JOIN trunks t ON t.id = b.trunk_id`

func scanBlocklist(r interface{ Scan(...any) error }) (BlocklistEntry, error) {
	var e BlocklistEntry
	var trunk, ann sql.NullInt64
	if err := r.Scan(&e.ID, &e.Pattern, &e.Match, &e.Action, &trunk, &e.TrunkName, &e.Reason, &ann, &e.Enabled,
		&e.CreatedAt, &e.UpdatedAt); err != nil {
		return e, err
	}
	if trunk.Valid {
		e.TrunkID = &trunk.Int64
	}
	if ann.Valid {
		e.AnnouncementID = &ann.Int64
	}
	return e, nil
}

// ListBlocklist returns up to f.Limit entries in id order, the cursor of the
// next page ("" at the end) and how many entries match f in all.
func (s *Store) ListBlocklist(ctx context.Context, f BlocklistFilter) ([]BlocklistEntry, string, int, error) {
	if f.Limit <= 0 {
		f.Limit = 200
	}
	where := ` WHERE ($1 = '' OR strpos(b.pattern, $1) > 0) AND ($2 = '' OR b.action = $2)
		AND ($3 = '' OR ($3 = 'global' AND b.trunk_id IS NULL) OR b.trunk_id::text = $3)`
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+blocklistFrom+where, f.Q, f.Action, f.Scope).Scan(&total); err != nil {
		return nil, "", 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+blocklistCols+blocklistFrom+where+` AND b.id > $4 ORDER BY b.id LIMIT $5`,
		f.Q, f.Action, f.Scope, f.After, f.Limit+1)
	if err != nil {
		return nil, "", 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []BlocklistEntry{}
	for rows.Next() {
		e, err := scanBlocklist(rows)
		if err != nil {
			return nil, "", 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = strconv.FormatInt(out[f.Limit-1].ID, 10)
	}
	return out, next, total, nil
}

// GetBlocklistEntry returns one entry.
func (s *Store) GetBlocklistEntry(ctx context.Context, id int64) (BlocklistEntry, error) {
	e, err := scanBlocklist(s.db.QueryRowContext(ctx, `SELECT `+blocklistCols+blocklistFrom+` WHERE b.id = $1`, id))
	return e, mapErr(err)
}

// blocklistFK turns a foreign-key failure into the field it is about.
func blocklistFK(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		switch {
		case strings.Contains(pg.ConstraintName, "trunk"):
			return fieldError("trunkId", "names a trunk that does not exist")
		case strings.Contains(pg.ConstraintName, "announcement"):
			return fieldError("announcementId", "names an announcement that does not exist")
		}
	}
	return err
}

// blocklistConflict says a pattern is already listed for that scope.
func blocklistConflict(err error) error {
	if errors.Is(mapErr(err), ErrConflict) {
		return fieldError("pattern", "is already on the blocklist for this scope")
	}
	return err
}

// lockBlocklistRoom errors with ErrBlocklistFull unless room more entries
// fit. The configuration lock configChange takes serialises writers, so the
// count cannot move underneath it.
func lockBlocklistRoom(ctx context.Context, tx *sql.Tx, room int) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM call_blocklist`).Scan(&n); err != nil {
		return err
	}
	if n+room > routing.MaxBlocklistEntries {
		return ErrBlocklistFull
	}
	return nil
}

// CreateBlocklistEntry adds an entry.
func (s *Store) CreateBlocklistEntry(ctx context.Context, actor string, in BlocklistInput) (BlocklistEntry, error) {
	var e BlocklistEntry
	err := s.configChange(ctx, actor, "create", "blocklist", nil, func(tx *sql.Tx) (int64, error) {
		if err := lockBlocklistRoom(ctx, tx, 1); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO call_blocklist (pattern, match, action, trunk_id, reason, announcement_id, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			in.Pattern, in.Match, in.Action, in.TrunkID, in.Reason, in.AnnouncementID, in.Enabled).Scan(&id); err != nil {
			return 0, blocklistConflict(blocklistFK(err))
		}
		var err error
		e, err = scanBlocklist(tx.QueryRowContext(ctx, `SELECT `+blocklistCols+blocklistFrom+` WHERE b.id = $1`, id))
		return id, err
	})
	return e, err
}

// UpdateBlocklistEntry replaces an entry's writable fields. apply gets the
// current values and returns the new ones, so a caller can leave a field it
// may not see untouched.
func (s *Store) UpdateBlocklistEntry(ctx context.Context, actor string, id int64, apply func(cur BlocklistInput) (BlocklistInput, error)) (BlocklistEntry, error) {
	var e BlocklistEntry
	err := s.configChange(ctx, actor, "update", "blocklist", nil, func(tx *sql.Tx) (int64, error) {
		cur, err := scanBlocklist(tx.QueryRowContext(ctx, `SELECT `+blocklistCols+blocklistFrom+` WHERE b.id = $1 FOR UPDATE OF b`, id))
		if err != nil {
			return id, err
		}
		in, err := apply(cur.Input())
		if err != nil {
			return id, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE call_blocklist SET pattern = $2, match = $3, action = $4, trunk_id = $5,
				reason = $6, announcement_id = $7, enabled = $8, updated_at = now() WHERE id = $1`,
			id, in.Pattern, in.Match, in.Action, in.TrunkID, in.Reason, in.AnnouncementID, in.Enabled); err != nil {
			return id, blocklistConflict(blocklistFK(err))
		}
		e, err = scanBlocklist(tx.QueryRowContext(ctx, `SELECT `+blocklistCols+blocklistFrom+` WHERE b.id = $1`, id))
		return id, err
	})
	return e, err
}

// DeleteBlocklistEntry removes an entry.
func (s *Store) DeleteBlocklistEntry(ctx context.Context, actor string, id int64) error {
	return s.configChange(ctx, actor, "delete", "blocklist", nil, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM call_blocklist WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return id, ErrNotFound
		}
		return id, nil
	})
}

// BlocklistImportResult is what an import changed.
type BlocklistImportResult struct {
	Created int
	Updated int
}

// ImportBlocklist writes rows in one transaction: a pattern already listed
// for its scope is updated, any other is added. All or nothing; the audit
// row carries no pattern.
func (s *Store) ImportBlocklist(ctx context.Context, actor string, rows []BlocklistInput) (BlocklistImportResult, error) {
	var res BlocklistImportResult
	err := s.configChangeID(ctx, actor, "import", "blocklist", nil, func(tx *sql.Tx) (string, error) {
		res = BlocklistImportResult{}
		for i, in := range rows {
			var inserted bool
			err := tx.QueryRowContext(ctx, `INSERT INTO call_blocklist (pattern, match, action, trunk_id, reason, announcement_id, enabled)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (pattern, match, (coalesce(trunk_id, 0))) DO UPDATE
				SET action = EXCLUDED.action, reason = EXCLUDED.reason, enabled = EXCLUDED.enabled, updated_at = now()
				RETURNING (xmax = 0)`,
				in.Pattern, in.Match, in.Action, in.TrunkID, in.Reason, in.AnnouncementID, in.Enabled).Scan(&inserted)
			if err != nil {
				if f := blocklistFK(err); f != err {
					return "", fieldError(fmt.Sprintf("rows[%d]", i), "names a trunk or announcement that does not exist")
				}
				return "", err
			}
			if inserted {
				res.Created++
			} else {
				res.Updated++
			}
		}
		if err := lockBlocklistRoom(ctx, tx, 0); err != nil {
			return "", err
		}
		return "", nil
	})
	return res, err
}
