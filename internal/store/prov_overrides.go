package store

// Setting overrides (phone-feature-parity S-12): see prov.Override. The
// values of secret overrides are sealed with prov.OverrideAAD.

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// OverrideMask is what a view shows in place of a secret value, and what a
// client sends back to keep the stored one.
const OverrideMask = "********"

// SettingOverride is one override as the API returns it. A secret's Value is
// the mask.
type SettingOverride struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Secret    bool      `json:"secret"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SettingOverrideInput is one override in a replacement. For a secret, a nil
// Value or the mask keeps the value stored for the key.
type SettingOverrideInput struct {
	Key    string  `json:"key"`
	Value  *string `json:"value"`
	Secret bool    `json:"secret"`
}

// ownerArgs are the group_id and phone_id values of a scope's owner: one is
// NULL. Queries compare both with IS NOT DISTINCT FROM.
func ownerArgs(scope prov.OverrideScope, id int64) (group, phone sql.NullInt64) {
	if scope == prov.ScopeGroup {
		return sql.NullInt64{Int64: id, Valid: true}, sql.NullInt64{}
	}
	return sql.NullInt64{}, sql.NullInt64{Int64: id, Valid: true}
}

const ownerIs = `group_id IS NOT DISTINCT FROM $1 AND phone_id IS NOT DISTINCT FROM $2`

// ListSettingOverrides returns the overrides of a group or a phone by key,
// secrets masked. prov.ErrNotFound when the owner is gone.
func (s *Store) ListSettingOverrides(ctx context.Context, scope prov.OverrideScope, ownerID int64) ([]SettingOverride, error) {
	if err := s.overrideOwner(ctx, s.q(ctx), scope, ownerID); err != nil {
		return nil, err
	}
	g, p := ownerArgs(scope, ownerID)
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT key, value, secret, updated_at FROM prov_setting_overrides
		WHERE `+ownerIs+` ORDER BY key`, g, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SettingOverride{}
	for rows.Next() {
		var o SettingOverride
		if err := rows.Scan(&o.Key, &o.Value, &o.Secret, &o.UpdatedAt); err != nil {
			return nil, err
		}
		if o.Secret {
			o.Value = OverrideMask
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) overrideOwner(ctx context.Context, q querier, scope prov.OverrideScope, ownerID int64) error {
	table := "phones"
	if scope == prov.ScopeGroup {
		table = "phone_groups"
	}
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = $1`, ownerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return prov.ErrNotFound
	}
	return err
}

// ReplaceSettingOverrides replaces all overrides of a group or a phone. Every
// key is validated (a phone's against its vendor); a failure names the
// position and changes nothing.
func (s *Store) ReplaceSettingOverrides(ctx context.Context, actor string, scope prov.OverrideScope, ownerID int64, in []SettingOverrideInput) ([]SettingOverride, error) {
	if len(in) > prov.MaxOverridesPerScope {
		return nil, fieldError("overrides", "at most "+strconv.Itoa(prov.MaxOverridesPerScope)+" settings")
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var vendor prov.Vendor
		if scope == prov.ScopePhone {
			if err := tx.QueryRowContext(ctx, `SELECT vendor FROM phones WHERE id = $1 FOR UPDATE`, ownerID).Scan(&vendor); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return prov.ErrNotFound
				}
				return err
			}
		} else if err := s.overrideOwner(ctx, tx, scope, ownerID); err != nil {
			return err
		}
		g, p := ownerArgs(scope, ownerID)
		// The sealed values already stored, for a secret sent back masked.
		kept := map[string][]byte{}
		rows, err := tx.QueryContext(ctx, `SELECT key, value_enc FROM prov_setting_overrides WHERE `+ownerIs+` AND secret`, g, p)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			var enc []byte
			if err := rows.Scan(&k, &enc); err != nil {
				_ = rows.Close()
				return err
			}
			kept[k] = enc
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		seen := map[string]bool{}
		type row struct {
			key, value string
			enc        []byte
		}
		var write []row
		for i, o := range in {
			path := "overrides[" + strconv.Itoa(i) + "]"
			if seen[o.Key] {
				return fieldError(path+".key", "the key "+o.Key+" is listed twice")
			}
			seen[o.Key] = true
			val := ""
			if o.Value != nil {
				val = *o.Value
			}
			keep := o.Secret && (o.Value == nil || val == OverrideMask)
			check := val
			if keep {
				check = ""
			}
			if err := prov.ValidateOverride(vendor, o.Key, check); err != nil {
				return fieldError(path, err.Error())
			}
			switch {
			case keep:
				enc, ok := kept[o.Key]
				if !ok {
					return fieldError(path+".value", "a secret needs a value")
				}
				write = append(write, row{key: o.Key, enc: enc})
			case o.Secret:
				if s.box == nil {
					return prov.ErrSealed
				}
				enc, err := s.box.Seal(val, prov.OverrideAAD(scope, ownerID, o.Key))
				if err != nil {
					return err
				}
				write = append(write, row{key: o.Key, enc: enc})
			default:
				write = append(write, row{key: o.Key, value: val})
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM prov_setting_overrides WHERE `+ownerIs, g, p); err != nil {
			return err
		}
		for _, w := range write {
			var enc any // NULL for a plain value
			if w.enc != nil {
				enc = w.enc
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO prov_setting_overrides (group_id, phone_id, key, value, value_enc, secret)
				VALUES ($1, $2, $3, $4, $5, $6)`, g, p, w.key, w.value, enc, w.enc != nil); err != nil {
				return err
			}
		}
		// The keys only, never a value, in the audit row.
		keys := make([]string, len(write))
		for i, w := range write {
			keys[i] = w.key
		}
		slices.Sort(keys)
		return insertAudit(ctx, tx, actor, "set-overrides", "phone_"+string(scope), strconv.FormatInt(ownerID, 10)+" "+strings.Join(keys, ","))
	})
	if err != nil {
		return nil, err
	}
	return s.ListSettingOverrides(ctx, scope, ownerID)
}

// renderOverrides opens the overrides of a phone's group and of the phone
// and resolves them for its vendor.
func (s *Store) renderOverrides(ctx context.Context, phoneID int64, groupID sql.NullInt64, vendor prov.Vendor) ([]prov.Override, error) {
	if !slices.Contains(prov.OverrideVendors, vendor) {
		return nil, nil
	}
	load := func(scope prov.OverrideScope, id int64) ([]prov.Override, error) {
		g, p := ownerArgs(scope, id)
		rows, err := s.db.QueryContext(ctx, `SELECT key, value, value_enc, secret FROM prov_setting_overrides
			WHERE `+ownerIs, g, p)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []prov.Override
		for rows.Next() {
			var o prov.Override
			var enc []byte
			if err := rows.Scan(&o.Key, &o.Value, &enc, &o.Secret); err != nil {
				return nil, err
			}
			if o.Secret {
				if s.box == nil {
					return nil, prov.ErrSealed
				}
				v, err := s.box.Open(enc, prov.OverrideAAD(scope, id, o.Key))
				if err != nil {
					return nil, prov.ErrSealed
				}
				o.Value = v
			}
			out = append(out, o)
		}
		return out, rows.Err()
	}
	var group []prov.Override
	if groupID.Valid {
		var err error
		if group, err = load(prov.ScopeGroup, groupID.Int64); err != nil {
			return nil, err
		}
	}
	phone, err := load(prov.ScopePhone, phoneID)
	if err != nil {
		return nil, err
	}
	return prov.ResolveOverrides(vendor, group, phone), nil
}
