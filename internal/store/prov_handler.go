package store

// The provisioning handler's view of the database (internal/prov contract
// 4, prov.Store) and the redirect worker's queue (contract 5,
// redirect.Store).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
)

var _ prov.Store = (*Store)(nil)

const recordSelect = `
	SELECT p.id, p.mac, p.vendor, p.model,
	       p.enabled AND p.device_id IS NOT NULL AND COALESCE(d.enabled, FALSE),
	       p.prev_token_hash IS NOT NULL AND p.prev_token_expires > now(),
	       p.boot_armed, p.token_hash
	FROM phones p LEFT JOIN devices d ON d.id = p.device_id`

func scanRecord(r interface{ Scan(...any) error }) (prov.PhoneRecord, []byte, error) {
	var (
		rec  prov.PhoneRecord
		hash []byte
	)
	err := r.Scan(&rec.ID, &rec.MAC, &rec.Vendor, &rec.Model, &rec.Allowlisted, &rec.HasPrevious, &rec.BootArmed, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		err = prov.ErrNotFound
	}
	return rec, hash, err
}

// PhoneByToken implements prov.Store.
func (s *Store) PhoneByToken(ctx context.Context, hash []byte) (prov.PhoneRecord, error) {
	rec, cur, err := scanRecord(s.db.QueryRowContext(ctx, recordSelect+`
		WHERE p.token_hash = $1 OR (p.prev_token_hash = $1 AND p.prev_token_expires > now())`, hash))
	rec.ViaPrevious = err == nil && string(cur) != string(hash)
	return rec, err
}

// PhoneByMAC implements prov.Store.
func (s *Store) PhoneByMAC(ctx context.Context, mac string) (prov.PhoneRecord, error) {
	rec, _, err := scanRecord(s.db.QueryRowContext(ctx, recordSelect+` WHERE p.mac = $1`, mac))
	return rec, err
}

func ipArg(st prov.FetchState) any {
	if !st.IP.IsValid() {
		return nil
	}
	return st.IP.Unmap().String()
}

// MarkFetched implements prov.Store.
// The update is compare-and-set on hash: a fetch that matched a token
// since replaced (a re-arm or a rotation) changes nothing.
func (s *Store) MarkFetched(ctx context.Context, phoneID int64, hash []byte, st prov.FetchState) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE phones SET first_fetch_at = COALESCE(first_fetch_at, $2), last_fetch_at = $2,
		       last_fetch_ip = $3::inet, last_fetch_ua = $4, last_fetch_file = $5,
		       firmware_seen = COALESCE(NULLIF($6, ''), firmware_seen),
		       ua_mismatch = ua_mismatch OR $7, boot_armed = FALSE
		WHERE id = $1 AND (token_hash = $8 OR (prev_token_hash = $8 AND prev_token_expires > now()))`,
		phoneID, st.At, ipArg(st), st.UserAgent, st.File, st.FirmwareSeen, st.UAMismatch, hash)
	return err
}

// PromoteToken implements prov.Store, only while hash is still the current
// token.
func (s *Store) PromoteToken(ctx context.Context, phoneID int64, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE phones SET prev_token_hash = NULL, prev_token_expires = NULL
		WHERE id = $1 AND token_hash = $2`, phoneID, hash)
	return err
}

// FlagTokenExposed implements prov.Store.
func (s *Store) FlagTokenExposed(ctx context.Context, phoneID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE phones SET token_exposed = TRUE WHERE id = $1`, phoneID)
	return err
}

// ClaimBoot implements prov.Store. The row lock serialises concurrent
// claims: the second waits for the first to commit and then finds the
// phone disarmed.
func (s *Store) ClaimBoot(ctx context.Context, mac string) (prov.PhoneRecord, *prov.ProvInfo, error) {
	var (
		rec     prov.PhoneRecord
		handoff *prov.ProvInfo
	)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		rec, _, err = scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE p.mac = $1 FOR UPDATE OF p`, mac))
		if err != nil {
			return err
		}
		switch {
		case !rec.BootArmed:
			_, err = tx.ExecContext(ctx, `UPDATE phones SET boot_reclaimed = TRUE WHERE id = $1`, rec.ID)
			return err
		case !rec.Allowlisted:
			return nil
		}
		var enc []byte
		if err := tx.QueryRowContext(ctx, `UPDATE phones SET boot_armed = FALSE WHERE id = $1 AND boot_armed RETURNING token_enc`, rec.ID).Scan(&enc); err != nil {
			return err
		}
		token, err := s.openToken(rec.ID, enc)
		if err != nil {
			return err // rolls the disarm back
		}
		rec.BootArmed = false
		info := s.prov.info(token, rec.MAC)
		handoff = &info
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		err = prov.ErrNotFound
	}
	return rec, handoff, err
}

// phoneBase is the phone's own URL as templates see it (no {mac}).
func (p ProvSettings) phoneBase(token string) string {
	if p.PublicURL == "" {
		return ""
	}
	return p.PublicURL + "/p/" + token + "/"
}

func (s *Store) openToken(phoneID int64, enc []byte) (string, error) {
	if s.box == nil {
		return "", prov.ErrSealed
	}
	t, err := s.box.Open(enc, prov.PhoneTokenAAD(phoneID))
	if err != nil {
		return "", prov.ErrSealed
	}
	return t, nil
}

// globSpecificity ranks a model glob: more literal characters is more
// specific ("T54W" over "T5*" over "*").
func globSpecificity(g string) int {
	n := 0
	for _, r := range g {
		if !strings.ContainsRune("*?[]", r) {
			n++
		}
	}
	return n
}

// RenderInputs implements prov.Store.
func (s *Store) RenderInputs(ctx context.Context, phoneID int64) (prov.RenderData, prov.Template, error) {
	var (
		d                          prov.RenderData
		tokenEnc, adminEnc, secEnc []byte
		devID, tplID               sql.NullInt64
		blfJSON                    []byte
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT p.mac, p.vendor, p.model, p.label, p.token_enc, p.admin_password_enc, p.template_id, p.blf,
		       d.id, d.sip_username, d.secret_enc, e.name, e.number
		FROM phones p LEFT JOIN devices d ON d.id = p.device_id LEFT JOIN extensions e ON e.id = d.extension_id
		WHERE p.id = $1`, phoneID).Scan(&d.Phone.MAC, &d.Phone.Vendor, &d.Phone.Model, &d.Phone.Label, &tokenEnc, &adminEnc,
		&tplID, &blfJSON, &devID, nullStr(&d.Line.Username), &secEnc, nullStr(&d.Line.DisplayName), nullStr(&d.Line.Label))
	if errors.Is(err, sql.ErrNoRows) {
		return d, prov.Template{}, prov.ErrNotFound
	}
	if err != nil {
		return d, prov.Template{}, err
	}
	if !devID.Valid {
		return d, prov.Template{}, prov.ErrNotFound
	}
	if s.box == nil || secEnc == nil {
		return d, prov.Template{}, prov.ErrSealed
	}
	d.Phone.MACUpper = strings.ToUpper(d.Phone.MAC)
	token, err := s.openToken(phoneID, tokenEnc)
	if err != nil {
		return d, prov.Template{}, err
	}
	if d.Phone.AdminPassword, err = s.box.Open(adminEnc, prov.PhoneAdminAAD(phoneID)); err != nil {
		return d, prov.Template{}, prov.ErrSealed
	}
	if d.Line.Password, err = s.box.Open(secEnc, prov.DeviceSecretAAD(devID.Int64)); err != nil {
		return d, prov.Template{}, prov.ErrSealed
	}
	d.Line.AuthName, d.Line.Domain = d.Line.Username, s.prov.Domain
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(min(code), '') FROM feature_codes WHERE action = 'voicemail' AND argument = ''`).
		Scan(&d.Line.VoicemailCode); err != nil {
		return d, prov.Template{}, err
	}
	host, port := splitHostPort(s.prov.SIPServer)
	d.Server = prov.Server{Host: host, Port: port, Transport: "udp", Expiry: int(s.prov.Expiry / time.Second)}
	d.Prov = s.prov.info(token, d.Phone.MAC)
	d.Time = prov.TimeInfo{Zone: s.prov.Timezone, NTP: s.prov.NTP}
	if d.BLF, err = s.blfKeys(ctx, blfJSON); err != nil {
		return d, prov.Template{}, err
	}
	if d.Firmware, err = s.pinnedFirmware(ctx, d.Phone.Vendor, d.Phone.Model, d.Prov.URL); err != nil {
		return d, prov.Template{}, err
	}
	stored, err := s.ListProvTemplates(ctx)
	if err != nil {
		return d, prov.Template{}, err
	}
	var override *prov.Template
	all := prov.Builtins()
	for _, t := range stored {
		all = append(all, t.Template)
		if tplID.Valid && t.ID == tplID.Int64 {
			o := t.Template
			override = &o
		}
	}
	t, ok := prov.Resolve(d.Phone, override, all)
	if !ok {
		return d, prov.Template{}, prov.ErrNoTemplate
	}
	return d, t, nil
}

// nullStr scans a nullable text column into s ("" for NULL).
func nullStr(s *string) any { return &nullString{s} }

type nullString struct{ s *string }

func (n *nullString) Scan(v any) error {
	var ns sql.NullString
	if err := ns.Scan(v); err != nil {
		return err
	}
	*n.s = ns.String
	return nil
}

func (s *Store) blfKeys(ctx context.Context, raw []byte) ([]prov.BLFKey, error) {
	var numbers []string
	if err := json.Unmarshal(raw, &numbers); err != nil {
		return nil, err
	}
	keys := make([]prov.BLFKey, 0, len(numbers))
	for _, n := range numbers {
		var name string
		err := s.db.QueryRowContext(ctx, `SELECT name FROM extensions WHERE number = $1`, n).Scan(&name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		keys = append(keys, prov.BLFKey{Number: n, Label: name, URI: "sip:" + n + "@" + s.prov.Domain})
	}
	return keys, nil
}

// pinnedFirmware picks the pin matching the model: the more specific glob,
// then the lower firmware id; nil when none matches.
func (s *Store) pinnedFirmware(ctx context.Context, v prov.Vendor, model, base string) (*prov.FirmwareInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pn.model_glob, f.id, f.filename, f.version FROM prov_firmware_pins pn
		JOIN prov_firmware f ON f.id = pn.firmware_id WHERE pn.vendor = $1`, v)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var (
		best     *prov.FirmwareInfo
		bestSpec int
		bestID   int64
	)
	for rows.Next() {
		var (
			glob, file, version string
			id                  int64
		)
		if err := rows.Scan(&glob, &id, &file, &version); err != nil {
			return nil, err
		}
		if ok, _ := path.Match(glob, model); !ok {
			continue
		}
		spec := globSpecificity(glob)
		if best == nil || spec > bestSpec || (spec == bestSpec && id < bestID) {
			best, bestSpec, bestID = &prov.FirmwareInfo{URL: base + "fw/" + file, Version: version}, spec, id
		}
	}
	return best, rows.Err()
}

// FirmwareByName implements prov.Store.
func (s *Store) FirmwareByName(ctx context.Context, v prov.Vendor, filename string) (prov.Firmware, error) {
	f, err := scanFirmware(s.db.QueryRowContext(ctx, `SELECT `+firmwareCols+` FROM prov_firmware f WHERE vendor = $1 AND filename = $2`, v, filename))
	if errors.Is(err, sql.ErrNoRows) {
		err = prov.ErrNotFound
	}
	return f.Firmware, err
}

// InsertFetches implements prov.Store.
func (s *Store) InsertFetches(ctx context.Context, rows []prov.FetchRecord) error {
	if len(rows) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO prov_fetches (at, phone_id, mac_claimed, ip, user_agent, path_redacted, kind, result, status, bytes, ua_mismatch)
			VALUES ($1, NULLIF($2::bigint, 0), $3, $4::inet, $5, $6, $7, $8, $9, $10, $11)`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, r := range rows {
			var ip any
			if r.IP.IsValid() {
				ip = r.IP.Unmap().String()
			}
			if _, err := stmt.ExecContext(ctx, r.At, r.PhoneID, r.MACClaimed, ip, r.UserAgent, r.PathRedacted,
				r.Kind, r.Result, r.Status, r.Bytes, r.UAMismatch); err != nil {
				return err
			}
		}
		return nil
	})
}

// Redirect returns the redirect worker's view of the store.
func (s *Store) Redirect() redirect.Store { return redirectQueue{s} }

type redirectQueue struct{ s *Store }

func (q redirectQueue) DueJobs(ctx context.Context, limit int) ([]redirect.Job, error) {
	rows, err := q.s.db.QueryContext(ctx, `
		SELECT seq, vendor, mac, op, attempts, first_queued_at FROM prov_redirect_jobs
		WHERE next_attempt_at <= now() ORDER BY next_attempt_at, seq LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []redirect.Job
	for rows.Next() {
		var j redirect.Job
		if err := rows.Scan(&j.Seq, &j.Vendor, &j.MAC, &j.Op, &j.Attempts, &j.FirstQueuedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (q redirectQueue) target(id int64, mac, serial string, v prov.Vendor, enc []byte) (redirect.Target, error) {
	token, err := q.s.openToken(id, enc)
	if err != nil {
		return redirect.Target{}, fmt.Errorf("phone %d: %w", id, err)
	}
	return redirect.Target{MAC: mac, Serial: serial, URL: q.s.prov.PhoneURL(token, v)}, nil
}

func (q redirectQueue) Target(ctx context.Context, v prov.Vendor, mac string) (redirect.Target, error) {
	var (
		id     int64
		serial string
		enc    []byte
	)
	err := q.s.db.QueryRowContext(ctx, `SELECT id, COALESCE(serial, ''), token_enc FROM phones WHERE mac = $1 AND vendor = $2`, mac, v).
		Scan(&id, &serial, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return redirect.Target{}, prov.ErrNotFound
	}
	if err != nil {
		return redirect.Target{}, err
	}
	return q.target(id, mac, serial, v, enc)
}

func (q redirectQueue) Account(ctx context.Context, v prov.Vendor) (redirect.Account, error) {
	return q.s.RedirectAccountCredentials(ctx, v)
}

// jobDone runs update on the job row, only while it still has j.Seq, and
// then, when st is not nil, sets the phone's status (when the phone exists
// with that vendor).
func (q redirectQueue) jobDone(ctx context.Context, j redirect.Job, st *redirect.Status, update string, args ...any) error {
	var b []byte
	if st != nil {
		var err error
		if b, err = json.Marshal(*st); err != nil {
			return err
		}
	}
	return q.s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, update, append([]any{j.Vendor, j.MAC, j.Seq}, args...)...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err // replaced since it was read: its own status stands
		}
		if st == nil {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE phones SET redirect_status = $3 WHERE mac = $2 AND vendor = $1`, j.Vendor, j.MAC, string(b))
		return err
	})
}

func (q redirectQueue) FinishJob(ctx context.Context, j redirect.Job, st *redirect.Status) error {
	return q.jobDone(ctx, j, st, `DELETE FROM prov_redirect_jobs WHERE vendor = $1 AND mac = $2 AND seq = $3`)
}

func (q redirectQueue) RetryJob(ctx context.Context, j redirect.Job, next time.Time, st *redirect.Status) error {
	var reason sql.NullString
	if st != nil {
		reason = sql.NullString{String: st.Reason, Valid: true}
	}
	return q.jobDone(ctx, j, st, `
		UPDATE prov_redirect_jobs SET attempts = attempts + 1, next_attempt_at = $4,
			last_error = COALESCE($5, last_error)
		WHERE vendor = $1 AND mac = $2 AND seq = $3`, next, reason)
}

func (q redirectQueue) LastDriftCheck(ctx context.Context) (time.Time, error) {
	var t sql.NullTime
	if err := q.s.db.QueryRowContext(ctx, `SELECT prov_drift_checked_at FROM schema_info`).Scan(&t); err != nil {
		return time.Time{}, err
	}
	return t.Time, nil
}

func (q redirectQueue) SetLastDriftCheck(ctx context.Context, t time.Time) error {
	_, err := q.s.db.ExecContext(ctx, `UPDATE schema_info SET prov_drift_checked_at = $1`, t)
	return err
}

func (q redirectQueue) Registered(ctx context.Context, v prov.Vendor) ([]redirect.Target, error) {
	rows, err := q.s.db.QueryContext(ctx, `
		SELECT id, mac, COALESCE(serial, ''), token_enc FROM phones
		WHERE vendor = $1 AND redirect_status->>'state' = 'registered' ORDER BY mac`, v)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []redirect.Target
	for rows.Next() {
		var (
			id          int64
			mac, serial string
			enc         []byte
		)
		if err := rows.Scan(&id, &mac, &serial, &enc); err != nil {
			return nil, err
		}
		t, err := q.target(id, mac, serial, v, enc)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (q redirectQueue) SetStatus(ctx context.Context, mac string, st redirect.Status) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = q.s.db.ExecContext(ctx, `UPDATE phones SET redirect_status = $2 WHERE mac = $1`, mac, string(b))
	return err
}
