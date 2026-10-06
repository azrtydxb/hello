package store

// Provisioning templates, firmware and pins, and vendor redirect accounts
// (spec S-8, S-11, S-13). Every change writes its audit row in the same
// transaction; none bumps the configuration revision (hello-sip does not
// read them).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
)

// TemplateError rejects a template, naming each failing field and line.
type TemplateError struct{ Fields []prov.FieldError }

func (e *TemplateError) Error() string { return "store: template validation failed" }

// ProvTemplate is a stored template with its last change time.
type ProvTemplate struct {
	prov.Template
	UpdatedAt time.Time
}

const templateCols = `id, vendor, model_glob, priority, name, files, COALESCE(builtin_ref, ''), version, updated_at`

func scanTemplate(r interface{ Scan(...any) error }) (ProvTemplate, error) {
	var (
		t     ProvTemplate
		files []byte
	)
	if err := r.Scan(&t.ID, &t.Vendor, &t.ModelGlob, &t.Priority, &t.Name, &files, &t.BuiltinRef, &t.Version, &t.UpdatedAt); err != nil {
		return t, err
	}
	return t, json.Unmarshal(files, &t.Files)
}

// ListProvTemplates returns the stored templates by id.
func (s *Store) ListProvTemplates(ctx context.Context) ([]ProvTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateCols+` FROM prov_templates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ProvTemplate{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetProvTemplate returns one stored template.
func (s *Store) GetProvTemplate(ctx context.Context, id int64) (ProvTemplate, error) {
	t, err := scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateCols+` FROM prov_templates WHERE id = $1`, id))
	return t, mapErr(err)
}

func saveVersion(ctx context.Context, tx *sql.Tx, actor string, t ProvTemplate) error {
	files, _ := json.Marshal(t.Files)
	_, err := tx.ExecContext(ctx, `INSERT INTO prov_template_versions (template_id, version, files, actor) VALUES ($1, $2, $3, $4)`,
		t.ID, t.Version, string(files), actor)
	return err
}

// CreateProvTemplate stores a validated template as version 1.
func (s *Store) CreateProvTemplate(ctx context.Context, actor string, t prov.Template) (ProvTemplate, error) {
	var out ProvTemplate
	err := s.provChange(ctx, actor, "create", "prov_template", func(tx *sql.Tx) (string, bool, error) {
		files, _ := json.Marshal(t.Files)
		var err error
		out, err = scanTemplate(tx.QueryRowContext(ctx, `
			INSERT INTO prov_templates (vendor, model_glob, priority, name, files, builtin_ref)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')) RETURNING `+templateCols,
			t.Vendor, t.ModelGlob, t.Priority, t.Name, string(files), t.BuiltinRef))
		if err != nil {
			return "", false, err
		}
		return strconv.FormatInt(out.ID, 10), false, saveVersion(ctx, tx, actor, out)
	})
	return out, err
}

// UpdateProvTemplate applies apply to the stored template and saves it as
// a new version (the previous one stays in prov_template_versions); apply
// returns the validation failures of the result.
func (s *Store) UpdateProvTemplate(ctx context.Context, actor string, id int64, apply func(*prov.Template) []prov.FieldError) (ProvTemplate, error) {
	var out ProvTemplate
	err := s.provChange(ctx, actor, "update", "prov_template", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		cur, err := scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM prov_templates WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return sid, false, err
		}
		t := cur.Template
		if f := apply(&t); len(f) > 0 {
			return sid, false, &TemplateError{Fields: f}
		}
		files, _ := json.Marshal(t.Files)
		out, err = scanTemplate(tx.QueryRowContext(ctx, `
			UPDATE prov_templates SET vendor = $2, model_glob = $3, priority = $4, name = $5, files = $6,
			       version = version + 1, updated_at = now()
			WHERE id = $1 RETURNING `+templateCols, id, t.Vendor, t.ModelGlob, t.Priority, t.Name, string(files)))
		if err != nil {
			return sid, false, err
		}
		return sid, false, saveVersion(ctx, tx, actor, out)
	})
	return out, err
}

// DeleteProvTemplate removes a template; one a phone names as its override
// is refused with the phone named.
func (s *Store) DeleteProvTemplate(ctx context.Context, actor string, id int64) error {
	return s.provChange(ctx, actor, "delete", "prov_template", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		var (
			phone int64
			mac   string
		)
		switch err := tx.QueryRowContext(ctx, `SELECT id, mac FROM phones WHERE template_id = $1 ORDER BY id LIMIT 1`, id).Scan(&phone, &mac); {
		case err == nil:
			return sid, false, conflict("phone %d (%s) uses this template as its override; change the phone first", phone, FormatMAC(mac))
		case !errors.Is(err, sql.ErrNoRows):
			return sid, false, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM prov_templates WHERE id = $1`, id)
		if err != nil {
			return sid, false, err
		}
		return sid, false, requireRow(res)
	})
}

// FirmwareFile is an uploaded firmware file with whether a pin names it.
type FirmwareFile struct {
	prov.Firmware
	Pinned bool
}

const firmwareCols = `f.id, f.vendor, f.model_glob, f.version, f.filename, f.object_key, f.size, f.sha256, f.uploaded_at,
	EXISTS (SELECT 1 FROM prov_firmware_pins pn WHERE pn.firmware_id = f.id)`

func scanFirmware(r interface{ Scan(...any) error }) (FirmwareFile, error) {
	var f FirmwareFile
	err := r.Scan(&f.ID, &f.Vendor, &f.ModelGlob, &f.Version, &f.Filename, &f.ObjectKey, &f.Size, &f.SHA256, &f.UploadedAt, &f.Pinned)
	return f, err
}

// ListFirmware returns every firmware file, newest first.
func (s *Store) ListFirmware(ctx context.Context) ([]FirmwareFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+firmwareCols+` FROM prov_firmware f ORDER BY f.id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FirmwareFile{}
	for rows.Next() {
		f, err := scanFirmware(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CreateFirmware records an uploaded file. A file name the vendor already
// has is a conflict naming it.
func (s *Store) CreateFirmware(ctx context.Context, actor string, f prov.Firmware) (FirmwareFile, error) {
	var out FirmwareFile
	err := s.provChange(ctx, actor, "create", "prov_firmware", func(tx *sql.Tx) (string, bool, error) {
		var id int64
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM prov_firmware WHERE vendor = $1 AND filename = $2`, f.Vendor, f.Filename).Scan(&id); {
		case err == nil:
			return "", false, conflict("%s firmware %s already exists (id %d); delete it first", f.Vendor, f.Filename, id)
		case !errors.Is(err, sql.ErrNoRows):
			return "", false, err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO prov_firmware (vendor, model_glob, version, filename, object_key, size, sha256)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			f.Vendor, f.ModelGlob, f.Version, f.Filename, f.ObjectKey, f.Size, f.SHA256).Scan(&id); err != nil {
			return "", false, err
		}
		var err error
		out, err = scanFirmware(tx.QueryRowContext(ctx, `SELECT `+firmwareCols+` FROM prov_firmware f WHERE f.id = $1`, id))
		return strconv.FormatInt(id, 10), false, err
	})
	return out, err
}

// DeleteFirmware removes a firmware row and returns its object key for the
// caller to remove from MinIO; a pinned file is refused with the pin named,
// so a rendered firmware URL never points at a missing object.
func (s *Store) DeleteFirmware(ctx context.Context, actor string, id int64) (string, error) {
	var key string
	err := s.provChange(ctx, actor, "delete", "prov_firmware", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		var vendor, glob string
		switch err := tx.QueryRowContext(ctx, `SELECT vendor, model_glob FROM prov_firmware_pins WHERE firmware_id = $1 LIMIT 1`, id).Scan(&vendor, &glob); {
		case err == nil:
			return sid, false, conflict("the firmware is pinned for %s %q; unpin it first", vendor, glob)
		case !errors.Is(err, sql.ErrNoRows):
			return sid, false, err
		}
		return sid, false, tx.QueryRowContext(ctx, `DELETE FROM prov_firmware WHERE id = $1 RETURNING object_key`, id).Scan(&key)
	})
	return key, err
}

// FirmwarePin pins one firmware file for a vendor's models.
type FirmwarePin struct {
	Vendor     prov.Vendor `json:"vendor"`
	ModelGlob  string      `json:"modelGlob"`
	FirmwareID int64       `json:"firmwareId"`
}

// ListFirmwarePins returns every pin.
func (s *Store) ListFirmwarePins(ctx context.Context) ([]FirmwarePin, error) {
	return listPins(ctx, s.db)
}

func listPins(ctx context.Context, q querier) ([]FirmwarePin, error) {
	rows, err := q.QueryContext(ctx, `SELECT vendor, model_glob, firmware_id FROM prov_firmware_pins ORDER BY vendor, model_glob`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FirmwarePin{}
	for rows.Next() {
		var p FirmwarePin
		if err := rows.Scan(&p.Vendor, &p.ModelGlob, &p.FirmwareID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutFirmwarePins replaces every pin with pins. A pin must name a firmware
// file of its own vendor.
func (s *Store) PutFirmwarePins(ctx context.Context, actor string, pins []FirmwarePin) ([]FirmwarePin, error) {
	var out []FirmwarePin
	err := s.provChange(ctx, actor, "update", "prov_firmware_pins", func(tx *sql.Tx) (string, bool, error) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM prov_firmware_pins`); err != nil {
			return "", false, err
		}
		for i, p := range pins {
			var v prov.Vendor
			err := tx.QueryRowContext(ctx, `SELECT vendor FROM prov_firmware WHERE id = $1`, p.FirmwareID).Scan(&v)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return "", false, fieldError(fmt.Sprintf("[%d].firmwareId", i), "no such firmware")
			case err != nil:
				return "", false, err
			case v != p.Vendor:
				return "", false, fieldError(fmt.Sprintf("[%d].firmwareId", i), "the firmware is for "+string(v))
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO prov_firmware_pins (vendor, model_glob, firmware_id) VALUES ($1, $2, $3)`,
				p.Vendor, p.ModelGlob, p.FirmwareID); err != nil {
				if errors.Is(mapErr(err), ErrConflict) {
					return "", false, fieldError(fmt.Sprintf("[%d].modelGlob", i), "pinned twice")
				}
				return "", false, err
			}
		}
		var err error
		out, err = listPins(ctx, tx)
		return "all", false, err
	})
	return out, err
}

// RedirectAccount is a vendor's stored redirect account; its credentials
// are never part of it.
type RedirectAccount struct {
	Vendor          prov.Vendor
	Enabled         bool
	HasCredentials  bool
	Settings        json.RawMessage
	LastCheckAt     *time.Time
	LastCheckResult string
}

// ListRedirectAccounts returns the stored accounts by vendor.
func (s *Store) ListRedirectAccounts(ctx context.Context) (map[prov.Vendor]RedirectAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vendor, enabled, credentials_enc IS NOT NULL, settings, last_check_at, COALESCE(last_check_result, '')
		FROM prov_redirect_accounts`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[prov.Vendor]RedirectAccount{}
	for rows.Next() {
		var (
			a  RedirectAccount
			at sql.NullTime
		)
		if err := rows.Scan(&a.Vendor, &a.Enabled, &a.HasCredentials, &a.Settings, &at, &a.LastCheckResult); err != nil {
			return nil, err
		}
		a.LastCheckAt = nullTime(at)
		out[a.Vendor] = a
	}
	return out, rows.Err()
}

// RedirectAccountChange holds the fields of an account update; nil keeps
// one. Credentials are sealed with prov.RedirectAAD.
type RedirectAccountChange struct {
	Enabled     *bool
	Credentials redirect.Credentials
	Settings    json.RawMessage
}

// PutRedirectAccount creates or changes a vendor's account. When the
// account becomes usable (enabled with credentials), every phone of the
// vendor is queued for registration.
func (s *Store) PutRedirectAccount(ctx context.Context, actor string, v prov.Vendor, c RedirectAccountChange) error {
	if c.Credentials != nil && s.box == nil {
		return errors.New("store: no secret box to seal redirect credentials")
	}
	return s.provChange(ctx, actor, "update", "prov_redirect_account", func(tx *sql.Tx) (string, bool, error) {
		var enc []byte
		if c.Credentials != nil {
			b, err := json.Marshal(map[string]string(c.Credentials))
			if err != nil {
				return string(v), false, err
			}
			if enc, err = s.box.Seal(string(b), prov.RedirectAAD(v)); err != nil {
				return string(v), false, err
			}
		}
		var settings any
		if c.Settings != nil {
			settings = string(c.Settings)
		}
		var before bool
		err := tx.QueryRowContext(ctx, `SELECT enabled AND credentials_enc IS NOT NULL FROM prov_redirect_accounts WHERE vendor = $1 FOR UPDATE`, v).Scan(&before)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return string(v), false, err
		}
		var after bool
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO prov_redirect_accounts (vendor, enabled, credentials_enc, settings)
			VALUES ($1, COALESCE($2, FALSE), $3, COALESCE($4::jsonb, '{}'))
			ON CONFLICT (vendor) DO UPDATE SET enabled = COALESCE($2, prov_redirect_accounts.enabled),
			       credentials_enc = COALESCE($3, prov_redirect_accounts.credentials_enc),
			       settings = COALESCE($4::jsonb, prov_redirect_accounts.settings)
			RETURNING enabled AND credentials_enc IS NOT NULL`, v, c.Enabled, enc, settings).Scan(&after); err != nil {
			return string(v), false, err
		}
		if after && !before && !s.prov.Deployment[v] {
			if err := s.queueVendor(ctx, tx, v); err != nil {
				return string(v), false, err
			}
		}
		return string(v), false, nil
	})
}

// queueVendor queues every phone of v for registration.
func (s *Store) queueVendor(ctx context.Context, tx *sql.Tx, v prov.Vendor) error {
	if err := upsertJob(ctx, tx, `SELECT vendor, mac, 'register' FROM phones WHERE vendor = $1`, v); err != nil {
		return err
	}
	b, _ := json.Marshal(redirect.Status{State: redirect.StatePending, At: time.Now().UTC()})
	_, err := tx.ExecContext(ctx, `UPDATE phones SET redirect_status = $2 WHERE vendor = $1`, v, string(b))
	return err
}

// DeleteRedirectAccount removes a vendor's stored account and credentials.
func (s *Store) DeleteRedirectAccount(ctx context.Context, actor string, v prov.Vendor) error {
	return s.provChange(ctx, actor, "delete", "prov_redirect_account", func(tx *sql.Tx) (string, bool, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM prov_redirect_accounts WHERE vendor = $1`, v)
		if err != nil {
			return string(v), false, err
		}
		return string(v), false, requireRow(res)
	})
}

// RecordRedirectCheck stores the outcome of a credential check.
func (s *Store) RecordRedirectCheck(ctx context.Context, v prov.Vendor, result string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO prov_redirect_accounts (vendor, last_check_at, last_check_result) VALUES ($1, now(), $2)
		ON CONFLICT (vendor) DO UPDATE SET last_check_at = now(), last_check_result = $2`, v, result)
	return err
}

// RedirectAccountCredentials returns a vendor's stored account with its
// credentials opened; prov.ErrNotFound when there is none.
func (s *Store) RedirectAccountCredentials(ctx context.Context, v prov.Vendor) (redirect.Account, error) {
	a := redirect.Account{Vendor: v}
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT enabled, credentials_enc, settings FROM prov_redirect_accounts WHERE vendor = $1`, v).
		Scan(&a.Enabled, &enc, &a.Settings)
	if errors.Is(err, sql.ErrNoRows) {
		return a, prov.ErrNotFound
	}
	if err != nil || enc == nil {
		return a, err
	}
	if s.box == nil {
		return a, prov.ErrSealed
	}
	plain, err := s.box.Open(enc, prov.RedirectAAD(v))
	if err != nil {
		return a, prov.ErrSealed
	}
	if err := json.Unmarshal([]byte(plain), &a.Credentials); err != nil {
		return a, prov.ErrSealed // never echo the opened value
	}
	return a, nil
}
