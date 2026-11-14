package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/azrtydxb/hello/internal/secret"
)

// Door station cameras (migration 00062, spec phone-feature-parity S-49). The
// password is sealed and never read back except by DoorCameraForView.

// Camera kinds.
const (
	CameraMJPEG    = "mjpeg"
	CameraSnapshot = "snapshot"
)

// DoorCamera is a station's camera as the API shows it; never the password.
type DoorCamera struct {
	Configured  bool   `json:"configured"`
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
}

// DoorCameraInput is the editable part of a camera. Password nil keeps the
// stored one, a pointer to "" clears it.
type DoorCameraInput struct {
	Kind     string
	URL      string
	Username string
	Password *string
}

// DoorCameraAccess is what the viewer needs to fetch the picture.
type DoorCameraAccess struct {
	Kind, URL, Username, Password string
}

// checkCameraURL accepts an absolute http or https URL without credentials
// or fragment. Whether its address may be reached is the proxy's decision, at
// connection time.
func checkCameraURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return fieldError("url", "must be an absolute http:// or https:// URL")
	case strings.EqualFold(u.Scheme, "rtsp") || strings.EqualFold(u.Scheme, "rtsps"):
		return fieldError("url", "rtsp:// cannot be shown in a browser; use the camera's MJPEG or snapshot URL (http://)")
	case u.Scheme != "http" && u.Scheme != "https":
		return fieldError("url", "must be an absolute http:// or https:// URL")
	case u.User != nil:
		return fieldError("url", "must not contain a user name or password; enter them in their own fields")
	case u.Fragment != "":
		return fieldError("url", "must not contain a fragment")
	case len(raw) > 2048:
		return fieldError("url", "is too long")
	}
	return nil
}

// GetDoorCamera returns door station id's camera; an unconfigured station has
// Configured false.
func (s *Store) GetDoorCamera(ctx context.Context, id int64) (DoorCamera, error) {
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM extensions WHERE id = $1`, id).Scan(&kind); err != nil {
		return DoorCamera{}, mapErr(err)
	}
	var c DoorCamera
	err := s.db.QueryRowContext(ctx, `SELECT kind, url, username, password_enc IS NOT NULL FROM door_cameras WHERE extension_id = $1`, id).
		Scan(&c.Kind, &c.URL, &c.Username, &c.PasswordSet)
	if errors.Is(err, sql.ErrNoRows) {
		return DoorCamera{}, nil
	}
	if err != nil {
		return DoorCamera{}, mapErr(err)
	}
	c.Configured = true
	return c, nil
}

// SetDoorCamera sets door station id's camera.
func (s *Store) SetDoorCamera(ctx context.Context, actor string, id int64, in DoorCameraInput) (DoorCamera, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM extensions WHERE id = $1 FOR UPDATE`, id).Scan(&kind); err != nil {
			return err
		}
		if kind != ExtensionDoor {
			return fieldError("kind", "only a door station has a camera")
		}
		if in.Kind != CameraMJPEG && in.Kind != CameraSnapshot {
			return fieldError("kind", "must be mjpeg or snapshot")
		}
		if err := checkCameraURL(in.URL); err != nil {
			return err
		}
		if len(in.Username) > 128 || strings.ContainsAny(in.Username, ":\r\n") {
			return fieldError("username", "at most 128 characters, no colon or line break")
		}
		var sealed []byte
		keep := in.Password == nil
		if in.Password != nil && *in.Password != "" {
			if len(*in.Password) > 256 {
				return fieldError("password", "at most 256 characters")
			}
			if s.box == nil {
				return errors.New("store: no secret box configured for door cameras")
			}
			aad := secret.DoorCameraAAD(id)
			var err error
			if sealed, err = s.box.Seal(*in.Password, aad); err != nil {
				return fmt.Errorf("store: seal door camera %d password: %w", id, err)
			}
			if back, err := s.box.Open(sealed, aad); err != nil || back != *in.Password {
				return fmt.Errorf("store: door camera %d password does not round-trip under HELLO_SECRET_KEY", id)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO door_cameras (extension_id, kind, url, username, password_enc)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (extension_id) DO UPDATE SET kind = $2, url = $3, username = $4,
					password_enc = CASE WHEN $6 THEN door_cameras.password_enc ELSE $5 END, updated_at = now()`,
			id, in.Kind, in.URL, in.Username, sealed, keep); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "door_camera", fmt.Sprint(id))
	})
	if err != nil {
		return DoorCamera{}, err
	}
	return s.GetDoorCamera(ctx, id)
}

// DeleteDoorCamera removes door station id's camera.
func (s *Store) DeleteDoorCamera(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM door_cameras WHERE extension_id = $1`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return insertAudit(ctx, tx, actor, "delete", "door_camera", fmt.Sprint(id))
	})
}

// DoorCameraForView returns what is needed to fetch door station id's
// picture, the password opened; ok is false without a camera.
func (s *Store) DoorCameraForView(ctx context.Context, id int64) (DoorCameraAccess, bool, error) {
	var a DoorCameraAccess
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT kind, url, username, password_enc FROM door_cameras WHERE extension_id = $1`, id).
		Scan(&a.Kind, &a.URL, &a.Username, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return DoorCameraAccess{}, false, nil
	}
	if err != nil {
		return DoorCameraAccess{}, false, err
	}
	if len(enc) > 0 {
		if s.box == nil {
			return DoorCameraAccess{}, false, nil
		}
		pw, err := s.box.Open(enc, secret.DoorCameraAAD(id))
		if err != nil {
			return DoorCameraAccess{}, false, nil //nolint:nilerr // a password that does not open cannot be used
		}
		a.Password = pw
	}
	return a, true, nil
}
