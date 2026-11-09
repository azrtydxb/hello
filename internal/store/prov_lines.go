package store

// Multiple lines per phone (phone-feature-parity S-10) and account sharing
// (S-11).
//
// Line 1 of a phone is phones.device_id; lines 2 and up are phone_lines
// rows. Every line is a device of an extension, so a phone with two lines
// registers two SIP accounts, and one extension may be a line of several
// phones (each with a device of its own), which is a shared line
// appearance: hello-sip forks a call to all of the extension's devices.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/azrtydxb/hello/internal/prov"
)

// PhoneLine is one line of a phone: the device it registers and the
// extension the device belongs to.
type PhoneLine struct {
	// Line is 1 for the phone's first line, 2 and up for the others.
	Line            int    `json:"line"`
	DeviceID        int64  `json:"deviceId"`
	ExtensionID     int64  `json:"extensionId"`
	ExtensionNumber string `json:"extensionNumber"`
	ExtensionName   string `json:"extensionName"`
	SIPUsername     string `json:"sipUsername"`
	// Provisioned is false for a line the phone's template does not write
	// (prov.SupportsLineKeys): the line is held in Hello but does not reach
	// the phone.
	Provisioned bool `json:"provisioned"`
}

// attachLines fills Lines, MaxLines and the line summary of phones from the
// database, one query for all of them. Line 1 comes from the phone's own
// binding.
func attachLines(ctx context.Context, q querier, ps []Phone) error {
	if len(ps) == 0 {
		return nil
	}
	ids := make([]int64, len(ps))
	at := make(map[int64]int, len(ps))
	for i := range ps {
		ids[i], at[ps[i].ID] = ps[i].ID, i
		ps[i].Lines = []PhoneLine{}
		ps[i].MaxLines = prov.MaxLines(ps[i].Vendor, ps[i].Model)
	}
	rows, err := q.QueryContext(ctx, `
		SELECT l.phone_id, l.line, d.id, e.id, e.number, e.name, d.sip_username
		FROM (SELECT id AS phone_id, 1 AS line, device_id FROM phones WHERE id = ANY($1) AND device_id IS NOT NULL
		      UNION ALL SELECT phone_id, line, device_id FROM phone_lines WHERE phone_id = ANY($1)) l
		JOIN devices d ON d.id = l.device_id JOIN extensions e ON e.id = d.extension_id
		ORDER BY l.phone_id, l.line`, ids)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			phone int64
			l     PhoneLine
		)
		if err := rows.Scan(&phone, &l.Line, &l.DeviceID, &l.ExtensionID, &l.ExtensionNumber, &l.ExtensionName, &l.SIPUsername); err != nil {
			return err
		}
		p := &ps[at[phone]]
		l.Provisioned = l.Line == 1 || prov.SupportsLineKeys(p.Vendor, p.Model)
		p.Lines = append(p.Lines, l)
	}
	return rows.Err()
}

// boundElsewhere reports a conflict when the device is already a line of a
// phone other than (phone, line): line 1 of any phone, or a further line of
// any phone. phone 0 is a phone being created.
func boundElsewhere(ctx context.Context, tx *sql.Tx, deviceID, phone int64, line int) error {
	var other int64
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM phones WHERE device_id = $1 AND NOT (id = $2 AND $3 = 1)`,
		deviceID, phone, line).Scan(&other); {
	case err == nil:
		return conflict("device %d is already line 1 of phone %d", deviceID, other)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	switch err := tx.QueryRowContext(ctx, `SELECT phone_id FROM phone_lines WHERE device_id = $1 AND NOT (phone_id = $2 AND line = $3)`,
		deviceID, phone, line).Scan(&other); {
	case err == nil:
		return conflict("device %d is already a line of phone %d", deviceID, other)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	return nil
}

// SetPhoneLine binds line (2 and up) of a phone to a device of the
// extension: the device in b, or one created for the extension when
// b.DeviceID is 0. The device gets a new sealed secret. It refuses a line
// beyond the model's line count, a phone with no line 1, an extension the
// phone already has a line on, and a device that is a line of any phone. It
// bumps the configuration revision.
func (s *Store) SetPhoneLine(ctx context.Context, actor string, phoneID int64, line int, b Binding, realm string) (p Phone, secretRotated bool, err error) {
	err = s.provChange(ctx, actor, "set-line", "phone", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(phoneID, 10)
		var (
			mac, model string
			vendor     prov.Vendor
			dev        sql.NullInt64
		)
		if err := tx.QueryRowContext(ctx, `SELECT mac, vendor, model, device_id FROM phones WHERE id = $1 FOR UPDATE`, phoneID).
			Scan(&mac, &vendor, &model, &dev); err != nil {
			return sid, false, err
		}
		if max := prov.MaxLines(vendor, model); line < 2 || line > max {
			return sid, false, fieldError("line", fmt.Sprintf("the %s %s has %d line(s); a line is 2 to %d", vendor, model, max, max))
		}
		if !dev.Valid {
			return sid, false, fieldError("line", "bind line 1 first")
		}
		// An extension is one line of a phone: a second line of it would be a
		// second account of the same number.
		var same int
		switch err := tx.QueryRowContext(ctx, `
			SELECT l.line FROM (SELECT 1 AS line, device_id FROM phones WHERE id = $1
			                    UNION ALL SELECT line, device_id FROM phone_lines WHERE phone_id = $1 AND line <> $3) l
			JOIN devices d ON d.id = l.device_id WHERE d.extension_id = $2`, phoneID, b.ExtensionID, line).Scan(&same); {
		case err == nil:
			return sid, false, fieldError("extensionId", fmt.Sprintf("line %d of this phone is already on this extension", same))
		case !errors.Is(err, sql.ErrNoRows):
			return sid, false, err
		}
		var cur sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT device_id FROM phone_lines WHERE phone_id = $1 AND line = $2 FOR UPDATE`, phoneID, line).Scan(&cur); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return sid, false, err
		}
		if cur.Valid {
			// The old device may be the one rebound by name below.
			if _, err := tx.ExecContext(ctx, `DELETE FROM phone_lines WHERE phone_id = $1 AND line = $2`, phoneID, line); err != nil {
				return sid, false, err
			}
			if err := unbindDevice(ctx, tx, cur.Int64); err != nil {
				return sid, false, err
			}
		}
		d, rotated, err := s.bindDeviceLine(ctx, tx, actor, b.ExtensionID, b.DeviceID, mac, realm, phoneID, line)
		if err != nil {
			return sid, false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO phone_lines (phone_id, line, device_id) VALUES ($1, $2, $3)`, phoneID, line, d); err != nil {
			return sid, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE phones SET updated_at = now() WHERE id = $1`, phoneID); err != nil {
			return sid, false, err
		}
		secretRotated = rotated
		return sid, true, nil
	})
	if err != nil {
		return p, false, err
	}
	p, err = s.GetPhone(ctx, phoneID)
	return p, secretRotated, err
}

// ClearPhoneLine unbinds line (2 and up) of a phone: the device keeps its
// HA1 values but loses its sealed secret. It reports ErrNotFound when the
// phone has no such line. It bumps the configuration revision.
func (s *Store) ClearPhoneLine(ctx context.Context, actor string, phoneID int64, line int) (Phone, error) {
	err := s.provChange(ctx, actor, "clear-line", "phone", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(phoneID, 10)
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phones WHERE id = $1 FOR UPDATE`, phoneID).Scan(&one); err != nil {
			return sid, false, err
		}
		var dev int64
		if err := tx.QueryRowContext(ctx, `DELETE FROM phone_lines WHERE phone_id = $1 AND line = $2 RETURNING device_id`, phoneID, line).Scan(&dev); err != nil {
			return sid, false, err
		}
		if err := unbindDevice(ctx, tx, dev); err != nil {
			return sid, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE phones SET updated_at = now() WHERE id = $1`, phoneID); err != nil {
			return sid, false, err
		}
		return sid, true, nil
	})
	if err != nil {
		return Phone{}, err
	}
	return s.GetPhone(ctx, phoneID)
}

// lineDeviceIDs lists the devices of a phone's lines 2 and up.
func lineDeviceIDs(ctx context.Context, tx *sql.Tx, phoneID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT device_id FROM phone_lines WHERE phone_id = $1 ORDER BY line`, phoneID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// renderLines reads lines 2 and up of a phone for the renderer, with each
// device's secret opened.
func (s *Store) renderLines(ctx context.Context, phoneID int64) ([]prov.Line, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.sip_username, d.secret_enc, e.name, e.number, COALESCE(e.call_waiting, TRUE), NOT COALESCE(e.voicemail_enabled, TRUE)
		FROM phone_lines l JOIN devices d ON d.id = l.device_id JOIN extensions e ON e.id = d.extension_id
		WHERE l.phone_id = $1 ORDER BY l.line`, phoneID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []prov.Line
	for rows.Next() {
		var (
			id  int64
			enc []byte
			l   prov.Line
		)
		if err := rows.Scan(&id, &l.Username, &enc, &l.DisplayName, &l.Label, &l.CallWaiting, &l.NoVoicemail); err != nil {
			return nil, err
		}
		if s.box == nil || enc == nil {
			return nil, prov.ErrSealed
		}
		pw, err := s.box.Open(enc, prov.DeviceSecretAAD(id))
		if err != nil {
			return nil, prov.ErrSealed
		}
		l.Password, l.AuthName, l.Domain = pw, l.Username, s.prov.Domain
		out = append(out, l)
	}
	return out, rows.Err()
}

// phoneSharing reads the account-sharing role of a phone: nil unless its
// group enabled account sharing and the model has the setting.
func (s *Store) phoneSharing(ctx context.Context, phoneID int64, v prov.Vendor, model string) (*prov.SharingInfo, error) {
	var (
		on   sql.NullBool
		role string
	)
	err := s.db.QueryRowContext(ctx, `SELECT g.account_sharing, p.sharing_role FROM phones p
		LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&on, &role)
	if err != nil {
		return nil, err
	}
	return prov.NewSharing(v, model, on.Valid && on.Bool, role), nil
}
