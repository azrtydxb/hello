package store

// Client devices (clients-webrtc-mobile-video S-13, S-24): the web (and,
// later, mobile) softphones of a user. A client is a row of devices with an
// owner and derived credentials, so nothing secret is stored: hello-sip
// derives the digest secret from HELLO_CLIENT_AUTH_KEY and the expiring
// username the API hands out. Every query here is keyed by the caller's user
// id and reaches an extension only through user_extensions, so another
// user's device or extension is ErrNotFound, never refused after being found.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

var (
	// ErrDeviceLimit is returned when a user already has the most client
	// devices allowed.
	ErrDeviceLimit = errors.New("store: client device limit reached")
	// ErrDerivedDevice is returned when a change only makes sense for a desk
	// phone: rotating a stored secret, or moving a client to another
	// extension.
	ErrDerivedDevice = errors.New("store: not allowed for a client device")
)

// ClientDevice is one of a user's web or mobile devices.
type ClientDevice struct {
	ID              int64      `json:"id"`
	Kind            string     `json:"kind"`
	Label           string     `json:"label"`
	Platform        string     `json:"platform"`
	AppVersion      string     `json:"appVersion"`
	ExtensionID     int64      `json:"extensionId"`
	ExtensionNumber string     `json:"extensionNumber"`
	SIPUsername     string     `json:"sipUsername"`
	LastSeenAt      *time.Time `json:"lastSeenAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

const softphoneCols = `d.id, d.kind, d.label, coalesce(d.platform, ''), coalesce(d.app_version, ''), d.extension_id, e.number,
	d.sip_username, d.last_seen_at, d.created_at`

func scanSoftphone(r interface{ Scan(...any) error }) (ClientDevice, error) {
	var c ClientDevice
	err := r.Scan(&c.ID, &c.Kind, &c.Label, &c.Platform, &c.AppVersion, &c.ExtensionID, &c.ExtensionNumber, &c.SIPUsername, &c.LastSeenAt, &c.CreatedAt)
	return c, err
}

// NewClientDevice is the input for CreateClientDevice.
type NewClientDevice struct {
	UserID      int64
	ExtensionID int64
	Kind        string // web or mobile
	Label       string
	Platform    string
	AppVersion  string
	Realm       string
	// Max is how many client devices the user may have at once.
	Max int
}

// CreateClientDevice enrols a client on one of the user's own extensions.
// An extension the user does not manage is ErrNotFound; a user already at
// Max is ErrDeviceLimit. The count is taken under a lock on the user's row,
// so two concurrent enrolments cannot both pass the limit.
func (s *Store) CreateClientDevice(ctx context.Context, actor string, in NewClientDevice) (ClientDevice, error) {
	var c ClientDevice
	err := s.configChangeID(ctx, actor, "create", "device", nil, func(tx *sql.Tx) (string, error) {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND enabled FOR UPDATE`, in.UserID).Scan(&in.UserID); err != nil {
			return "", err
		}
		var linked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_extensions WHERE user_id = $1 AND extension_id = $2)`,
			in.UserID, in.ExtensionID).Scan(&linked); err != nil {
			return "", err
		}
		if !linked {
			return "", sql.ErrNoRows
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM devices WHERE user_id = $1 AND kind <> 'desk' AND enabled`, in.UserID).Scan(&n); err != nil {
			return "", err
		}
		if n >= in.Max {
			return "", ErrDeviceLimit
		}
		// The stable username w<id> / m<id> embeds the id, so the id is drawn
		// first.
		var id int64
		if err := tx.QueryRowContext(ctx, `
			WITH n AS (SELECT nextval(pg_get_serial_sequence('devices', 'id')) AS id)
			INSERT INTO devices (id, extension_id, sip_username, realm, enabled, kind, user_id, label, platform, app_version, auth_mode)
			SELECT n.id, $1::bigint, (CASE WHEN $2::text = 'mobile' THEN 'm' ELSE 'w' END) || n.id::text, $3::text, TRUE, $2::text, $4::bigint,
			       $5::text, NULLIF($6::text, ''), NULLIF($7::text, ''), 'derived'
			FROM n RETURNING id`,
			in.ExtensionID, in.Kind, in.Realm, in.UserID, in.Label, in.Platform, in.AppVersion).Scan(&id); err != nil {
			return "", err
		}
		var err error
		c, err = scanSoftphone(tx.QueryRowContext(ctx, `SELECT `+softphoneCols+` FROM devices d JOIN extensions e ON e.id = d.extension_id WHERE d.id = $1`, id))
		return strconv.FormatInt(id, 10), err
	})
	return c, err
}

// ListClientDevices returns the user's active client devices, oldest first.
func (s *Store) ListClientDevices(ctx context.Context, userID int64) ([]ClientDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+softphoneCols+` FROM devices d JOIN extensions e ON e.id = d.extension_id
		WHERE d.user_id = $1 AND d.kind <> 'desk' AND d.enabled ORDER BY d.id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ClientDevice{}
	for rows.Next() {
		c, err := scanSoftphone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RefreshClientDevice returns one of the user's active client devices and
// notes that it was seen now. It changes no configuration: the credentials
// are derived, so nothing reaches hello-sip.
func (s *Store) RefreshClientDevice(ctx context.Context, userID, id int64) (ClientDevice, error) {
	c, err := scanSoftphone(s.db.QueryRowContext(ctx, `
		WITH u AS (UPDATE devices SET last_seen_at = now()
		           WHERE id = $2 AND user_id = $1 AND kind <> 'desk' AND enabled RETURNING *)
		SELECT `+softphoneCols+` FROM u d JOIN extensions e ON e.id = d.extension_id`, userID, id))
	return c, mapErr(err)
}

// RevokeClientDevice disables one of the user's client devices. The
// configuration revision moves, so hello-sip drops the device's
// registrations and refuses its credentials within its refresh interval.
func (s *Store) RevokeClientDevice(ctx context.Context, actor string, userID, id int64) error {
	return s.configChange(ctx, actor, "revoke", "device", nil, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `UPDATE devices SET enabled = FALSE, updated_at = now()
			WHERE id = $1 AND user_id = $2 AND kind <> 'desk' AND enabled`, id, userID)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}
