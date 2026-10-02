package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// Extension is a dialable number.
type Extension struct {
	ID        int64     `json:"id"`
	Number    string    `json:"number"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const extensionCols = `id, number, name, created_at, updated_at`

func scanExtension(r interface{ Scan(...any) error }) (Extension, error) {
	var e Extension
	err := r.Scan(&e.ID, &e.Number, &e.Name, &e.CreatedAt, &e.UpdatedAt)
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

// CreateExtension inserts an extension.
func (s *Store) CreateExtension(ctx context.Context, actor, number, name string) (Extension, error) {
	var e Extension
	err := s.configChange(ctx, actor, "create", "extension", func(tx *sql.Tx) (int64, error) {
		var err error
		e, err = scanExtension(tx.QueryRowContext(ctx,
			`INSERT INTO extensions (number, name) VALUES ($1, $2) RETURNING `+extensionCols, number, name))
		return e.ID, err
	})
	return e, err
}

// UpdateExtension changes the fields that are not nil.
func (s *Store) UpdateExtension(ctx context.Context, actor string, id int64, number, name *string) (Extension, error) {
	var e Extension
	err := s.configChange(ctx, actor, "update", "extension", func(tx *sql.Tx) (int64, error) {
		var err error
		e, err = scanExtension(tx.QueryRowContext(ctx, `
			UPDATE extensions SET number = COALESCE($2, number), name = COALESCE($3, name), updated_at = now()
			WHERE id = $1 RETURNING `+extensionCols, id, number, name))
		return id, err
	})
	return e, err
}

// DeleteExtension removes an extension and, by cascade, its devices.
func (s *Store) DeleteExtension(ctx context.Context, actor string, id int64) error {
	return s.configChange(ctx, actor, "delete", "extension", func(tx *sql.Tx) (int64, error) {
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
	err := s.configChange(ctx, actor, "create", "device", func(tx *sql.Tx) (int64, error) {
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
	err := s.configChange(ctx, actor, "update", "device", func(tx *sql.Tx) (int64, error) {
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
	err := s.configChange(ctx, actor, "rotate-secret", "device", func(tx *sql.Tx) (int64, error) {
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
	return s.configChange(ctx, actor, "delete", "device", func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// CDR is one call detail record written by hello-sip.
type CDR struct {
	ID              int64      `json:"id"`
	CorrelationID   string     `json:"correlationId"`
	SIPCallID       string     `json:"sipCallId"`
	Source          string     `json:"source"`
	Destination     string     `json:"destination"`
	StartTime       time.Time  `json:"startTime"`
	RingTime        *time.Time `json:"ringTime,omitempty"`
	AnswerTime      *time.Time `json:"answerTime,omitempty"`
	EndTime         time.Time  `json:"endTime"`
	DurationMs      int64      `json:"durationMs"`
	BillableMs      int64      `json:"billableMs"`
	SIPNode         string     `json:"sipNode"`
	MediaMode       string     `json:"mediaMode"`
	FinalStatus     int        `json:"finalStatus"`
	TerminationSide string     `json:"terminationSide"`
	FailureReason   string     `json:"failureReason"`
}

// ListCDRs returns up to limit CDRs with id below before (0 means from the
// newest), newest first, and the cursor for the next page ("" at the end).
func (s *Store) ListCDRs(ctx context.Context, before int64, limit int) ([]CDR, string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, correlation_id, sip_call_id, source, destination, start_time, ring_time, answer_time,
		       end_time, duration_ms, billable_ms, sip_node, media_mode, final_status, termination_side, failure_reason
		FROM cdrs WHERE $1 = 0 OR id < $1 ORDER BY id DESC LIMIT $2`, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []CDR{}
	for rows.Next() {
		var c CDR
		var ring, answer sql.NullTime
		if err := rows.Scan(&c.ID, &c.CorrelationID, &c.SIPCallID, &c.Source, &c.Destination, &c.StartTime, &ring, &answer,
			&c.EndTime, &c.DurationMs, &c.BillableMs, &c.SIPNode, &c.MediaMode, &c.FinalStatus, &c.TerminationSide, &c.FailureReason); err != nil {
			return nil, "", err
		}
		c.RingTime, c.AnswerTime = nullTime(ring), nullTime(answer)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[limit-1].ID, 10)
	}
	return out, next, nil
}
