package store

// Ring tags (migration 00042, spec phone-feature-parity S-39 and S-40): the
// built-in and custom tags with their Bellcore slots, and the ordered rules
// that choose a tag. Every mutation runs through configChange, so it is
// audited, bumps the configuration revision and reaches hello-sip as a new
// snapshot.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/azrtydxb/hello/internal/ringtag"
)

// RingTag is a ring tag as the API shows it; UsedBy counts the rules and
// ring groups that name it.
type RingTag struct {
	ringtag.Tag
	UsedBy int `json:"usedBy"`
}

// RingTagInput is the writable shape of a tag.
type RingTagInput struct {
	Name         string
	BellcoreSlot int
	Description  string
}

// RingRuleInput is one rule of the ordered list; the position is its index.
type RingRuleInput struct {
	Name  string
	Match ringtag.Match
	Tag   string
}

// ListRingTags returns every tag, built-ins first, with the count of rules
// and groups that use it.
func (s *Store) ListRingTags(ctx context.Context) ([]RingTag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.name, t.builtin, t.bellcore_slot, t.description,
		       (SELECT count(*) FROM ring_rules r WHERE r.tag = t.name)
		     + (SELECT count(*) FROM ring_groups g WHERE g.ring_tag = t.name)
		FROM ring_tags t ORDER BY t.builtin DESC, t.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RingTag{}
	for rows.Next() {
		var t RingTag
		if err := rows.Scan(&t.Name, &t.Builtin, &t.BellcoreSlot, &t.Description, &t.UsedBy); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetRingTag returns one tag.
func (s *Store) GetRingTag(ctx context.Context, name string) (RingTag, error) {
	ts, err := s.ListRingTags(ctx)
	if err != nil {
		return RingTag{}, err
	}
	for _, t := range ts {
		if t.Name == name {
			return t, nil
		}
	}
	return RingTag{}, ErrNotFound
}

// CreateRingTag adds a custom tag (at most ringtag.MaxCustomTags).
func (s *Store) CreateRingTag(ctx context.Context, actor string, in RingTagInput) (RingTag, error) {
	err := s.configChangeID(ctx, actor, "create", "ring_tag", nil, func(tx *sql.Tx) (string, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ring_tags WHERE NOT builtin`).Scan(&n); err != nil {
			return in.Name, err
		}
		if n >= ringtag.MaxCustomTags {
			return in.Name, &InUseError{Msg: fmt.Sprintf("at most %d custom ring tags", ringtag.MaxCustomTags)}
		}
		var dup int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ring_tags WHERE name = $1`, in.Name).Scan(&dup); err != nil {
			return in.Name, err
		}
		if dup > 0 {
			return in.Name, ErrConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ring_tags (name, builtin, bellcore_slot, description) VALUES ($1, FALSE, $2, $3)`,
			in.Name, in.BellcoreSlot, in.Description)
		return in.Name, err
	})
	if err != nil {
		return RingTag{}, err
	}
	return s.GetRingTag(ctx, in.Name)
}

// UpdateRingTag changes a tag's Bellcore slot and description; the name never
// changes.
func (s *Store) UpdateRingTag(ctx context.Context, actor, name string, in RingTagInput) (RingTag, error) {
	err := s.configChangeID(ctx, actor, "update", "ring_tag", nil, func(tx *sql.Tx) (string, error) {
		res, err := tx.ExecContext(ctx, `UPDATE ring_tags SET bellcore_slot = $2, description = $3, updated_at = now() WHERE name = $1`,
			name, in.BellcoreSlot, in.Description)
		if err != nil {
			return name, err
		}
		return name, requireRow(res)
	})
	if err != nil {
		return RingTag{}, err
	}
	return s.GetRingTag(ctx, name)
}

// DeleteRingTag removes a custom tag nothing uses.
func (s *Store) DeleteRingTag(ctx context.Context, actor, name string) error {
	return s.configChangeID(ctx, actor, "delete", "ring_tag", nil, func(tx *sql.Tx) (string, error) {
		var builtin bool
		if err := tx.QueryRowContext(ctx, `SELECT builtin FROM ring_tags WHERE name = $1`, name).Scan(&builtin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return name, ErrNotFound
			}
			return name, err
		}
		if builtin {
			return name, &InUseError{Msg: "ring tag " + name + " is built in and cannot be deleted"}
		}
		var rules, groups []string
		for _, q := range []struct {
			sql string
			dst *[]string
		}{
			{`SELECT 'rule ' || position FROM ring_rules WHERE tag = $1 ORDER BY position`, &rules},
			{`SELECT 'ring group ' || name FROM ring_groups WHERE ring_tag = $1 ORDER BY name`, &groups},
		} {
			rows, err := tx.QueryContext(ctx, q.sql, name)
			if err != nil {
				return name, err
			}
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					_ = rows.Close()
					return name, err
				}
				*q.dst = append(*q.dst, v)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return name, err
			}
		}
		if used := append(rules, groups...); len(used) > 0 {
			if len(used) > 5 {
				used = used[:5]
			}
			return name, &InUseError{Msg: "ring tag " + name + " is used by " + strings.Join(used, ", ")}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM ring_tags WHERE name = $1`, name)
		return name, err
	})
}

// RingRule is a stored rule.
type RingRule = ringtag.Rule

// ListRingRules returns the rules in position order.
func (s *Store) ListRingRules(ctx context.Context) ([]RingRule, error) {
	return listRingRules(ctx, s.db)
}

func listRingRules(ctx context.Context, q querier) ([]RingRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, position, name, match, tag FROM ring_rules ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RingRule{}
	for rows.Next() {
		var r RingRule
		var raw []byte
		if err := rows.Scan(&r.ID, &r.Position, &r.Name, &raw, &r.Tag); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r.Match); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceRingRules replaces the ordered rule list as a whole (at most
// ringtag.MaxRules); the position of a rule is its index plus one.
func (s *Store) ReplaceRingRules(ctx context.Context, actor string, in []RingRuleInput) ([]RingRule, error) {
	var out []RingRule
	err := s.configChange(ctx, actor, "update", "ring_rules", nil, func(tx *sql.Tx) (int64, error) {
		if len(in) > ringtag.MaxRules {
			return 0, fieldError("rules", fmt.Sprintf("must have at most %d rules", ringtag.MaxRules))
		}
		known := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT name FROM ring_tags`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return 0, err
			}
			known[n] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		for i, r := range in {
			if !known[r.Tag] {
				return 0, fieldError(fmt.Sprintf("rules[%d].tag", i), "no such ring tag")
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ring_rules`); err != nil {
			return 0, err
		}
		for i, r := range in {
			raw, err := json.Marshal(r.Match)
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ring_rules (position, name, match, tag) VALUES ($1, $2, $3, $4)`,
				i+1, r.Name, string(raw), r.Tag); err != nil {
				return 0, err
			}
		}
		out, err = listRingRules(ctx, tx)
		return 0, err
	})
	return out, err
}

// checkRingTag refuses a ring group's tag that does not exist.
func checkRingTag(ctx context.Context, q querier, path, tag string) error {
	if tag == "" {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM ring_tags WHERE name = $1`, tag).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fieldError(path, "no such ring tag")
	}
	return nil
}
