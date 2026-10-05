package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// Extension is a dialable number. ExternalNumber is the caller ID it
// presents on outbound trunk calls ("" if none). DND, the forwarding
// targets and VoicemailEnabled are the Phase 4 per-extension call features;
// RecordDefault is the Phase 5 default-recording switch.
type Extension struct {
	ID               int64     `json:"id"`
	Number           string    `json:"number"`
	Name             string    `json:"name"`
	ExternalNumber   string    `json:"externalNumber"`
	DND              bool      `json:"dnd"`
	ForwardAlways    string    `json:"forwardAlways"`
	ForwardBusy      string    `json:"forwardBusy"`
	ForwardNoAnswer  string    `json:"forwardNoAnswer"`
	VoicemailEnabled bool      `json:"voicemailEnabled"`
	RecordDefault    bool      `json:"recordDefault"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

const extensionCols = `id, number, name, external_number, dnd, forward_always, forward_busy, forward_no_answer,
	voicemail_enabled, record_default, created_at, updated_at`

func scanExtension(r interface{ Scan(...any) error }) (Extension, error) {
	var e Extension
	err := r.Scan(&e.ID, &e.Number, &e.Name, &e.ExternalNumber, &e.DND, &e.ForwardAlways, &e.ForwardBusy,
		&e.ForwardNoAnswer, &e.VoicemailEnabled, &e.RecordDefault, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// ListExtensions returns every extension ordered by number.
func (s *Store) ListExtensions(ctx context.Context) ([]Extension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+extensionCols+` FROM extensions ORDER BY number`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Extension{}
	for rows.Next() {
		e, err := scanExtension(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetExtension returns one extension.
func (s *Store) GetExtension(ctx context.Context, id int64) (Extension, error) {
	e, err := scanExtension(s.db.QueryRowContext(ctx, `SELECT `+extensionCols+` FROM extensions WHERE id = $1`, id))
	return e, mapErr(err)
}

// CreateExtension inserts an extension and, with it, its voicemail box
// (spec S-8: a box per extension, created automatically).
func (s *Store) CreateExtension(ctx context.Context, actor, number, name, externalNumber string, check Check) (Extension, error) {
	var e Extension
	err := s.configChange(ctx, actor, "create", "extension", check, func(tx *sql.Tx) (int64, error) {
		var err error
		e, err = scanExtension(tx.QueryRowContext(ctx,
			`INSERT INTO extensions (number, name, external_number) VALUES ($1, $2, $3) RETURNING `+extensionCols,
			number, name, externalNumber))
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO voicemail_boxes (extension_id) VALUES ($1) ON CONFLICT (extension_id) DO NOTHING`, e.ID)
		return e.ID, err
	})
	return e, err
}

// ExtensionChange holds the fields of an extension update; nil keeps one.
type ExtensionChange struct {
	Number, Name, ExternalNumber *string
	DND                          *bool
	ForwardAlways                *string
	ForwardBusy                  *string
	ForwardNoAnswer              *string
	VoicemailEnabled             *bool
	RecordDefault                *bool
}

// UpdateExtension changes the fields that are not nil. Renumbering an
// extension a route refers to (an inbound destination or an outbound
// source extension) is an *InUseError naming the routes.
func (s *Store) UpdateExtension(ctx context.Context, actor string, id int64, c ExtensionChange, check Check) (Extension, error) {
	var e Extension
	err := s.configChange(ctx, actor, "update", "extension", check, func(tx *sql.Tx) (int64, error) {
		number, routes, err := extensionRoutes(ctx, tx, id)
		if err != nil {
			return id, err
		}
		if c.Number != nil && *c.Number != number && len(routes) != 0 {
			return id, inUse("extension "+number, routes)
		}
		e, err = scanExtension(tx.QueryRowContext(ctx, `
			UPDATE extensions SET number = COALESCE($2, number), name = COALESCE($3, name),
			       external_number = COALESCE($4, external_number), dnd = COALESCE($5, dnd),
			       forward_always = COALESCE($6, forward_always), forward_busy = COALESCE($7, forward_busy),
			       forward_no_answer = COALESCE($8, forward_no_answer), voicemail_enabled = COALESCE($9, voicemail_enabled),
			       record_default = COALESCE($10, record_default), updated_at = now()
			WHERE id = $1 RETURNING `+extensionCols, id, c.Number, c.Name, c.ExternalNumber,
			c.DND, c.ForwardAlways, c.ForwardBusy, c.ForwardNoAnswer, c.VoicemailEnabled, c.RecordDefault))
		return id, err
	})
	return e, err
}

// DeleteExtension removes an extension and, by cascade, its devices. An
// extension a route refers to (an inbound destination or an outbound
// source extension) is an *InUseError naming the routes.
func (s *Store) DeleteExtension(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "extension", check, func(tx *sql.Tx) (int64, error) {
		number, routes, err := extensionRoutes(ctx, tx, id)
		if err != nil {
			return id, err
		}
		if len(routes) > 0 {
			return id, inUse("extension "+number, routes)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM extensions WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// Device is a SIP endpoint of an extension. Its secret is never stored; only
// the digest HA1 values are, and they are never returned.
type Device struct {
	ID          int64     `json:"id"`
	ExtensionID int64     `json:"extensionId"`
	SIPUsername string    `json:"sipUsername"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const deviceCols = `id, extension_id, sip_username, enabled, created_at, updated_at`

func scanDevice(r interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := r.Scan(&d.ID, &d.ExtensionID, &d.SIPUsername, &d.Enabled, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// ListDevices returns every device ordered by SIP username.
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY sip_username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDevice returns one device.
func (s *Store) GetDevice(ctx context.Context, id int64) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = $1`, id))
	return d, mapErr(err)
}

// NewDevice is the input for CreateDevice. Secret is hashed into HA1 values
// for Realm and then discarded.
type NewDevice struct {
	ExtensionID int64
	SIPUsername string
	Enabled     bool
	Realm       string
	Secret      string
}

// CreateDevice inserts a device; a missing extension is ErrNotFound.
func (s *Store) CreateDevice(ctx context.Context, actor string, in NewDevice) (Device, error) {
	md5Hex, shaHex := auth.HA1(in.SIPUsername, in.Realm, in.Secret)
	var d Device
	err := s.configChange(ctx, actor, "create", "device", nil, func(tx *sql.Tx) (int64, error) {
		var err error
		d, err = scanDevice(tx.QueryRowContext(ctx, `
			INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256, enabled)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+deviceCols,
			in.ExtensionID, in.SIPUsername, in.Realm, md5Hex, shaHex, in.Enabled))
		return d.ID, err
	})
	return d, err
}

// UpdateDevice changes the fields that are not nil.
func (s *Store) UpdateDevice(ctx context.Context, actor string, id int64, enabled *bool, extensionID *int64) (Device, error) {
	var d Device
	err := s.configChange(ctx, actor, "update", "device", nil, func(tx *sql.Tx) (int64, error) {
		var err error
		d, err = scanDevice(tx.QueryRowContext(ctx, `
			UPDATE devices SET enabled = COALESCE($2, enabled), extension_id = COALESCE($3, extension_id), updated_at = now()
			WHERE id = $1 RETURNING `+deviceCols, id, enabled, extensionID))
		return id, err
	})
	return d, err
}

// RotateDeviceSecret replaces a device's HA1 values with ones computed from
// secret for realm.
func (s *Store) RotateDeviceSecret(ctx context.Context, actor string, id int64, realm, secret string) (Device, error) {
	var d Device
	err := s.configChange(ctx, actor, "rotate-secret", "device", nil, func(tx *sql.Tx) (int64, error) {
		var username string
		if err := tx.QueryRowContext(ctx, `SELECT sip_username FROM devices WHERE id = $1 FOR UPDATE`, id).Scan(&username); err != nil {
			return id, err
		}
		md5Hex, shaHex := auth.HA1(username, realm, secret)
		var err error
		d, err = scanDevice(tx.QueryRowContext(ctx, `
			UPDATE devices SET realm = $2, ha1_md5 = $3, ha1_sha256 = $4, updated_at = now()
			WHERE id = $1 RETURNING `+deviceCols, id, realm, md5Hex, shaHex))
		return id, err
	})
	return d, err
}

// DeleteDevice removes a device.
func (s *Store) DeleteDevice(ctx context.Context, actor string, id int64) error {
	return s.configChange(ctx, actor, "delete", "device", nil, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}
