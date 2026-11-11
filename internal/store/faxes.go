package store

// Faxes (migration 00072, pbx-gaps S-15 to S-17): the fax destinations an
// administrator configures, and the fax rows hello-sip writes as calls come
// and go. Destinations are configuration (audited, they bump the revision so
// hello-sip sees them); fax rows change at call rate and are not.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
)

// MailToOwner is the mail_to value of a received fax whose destination names
// no address: the PDF goes to the users linked to the owner extension.
const MailToOwner = "@owner"

// FaxDestinationInput is the writable shape of a fax destination.
type FaxDestinationInput struct {
	Name      string
	Extension string
	Email     string
}

const faxDestCols = `d.id, d.name, e.number, d.email, d.created_at, d.updated_at`
const faxDestFrom = ` FROM fax_destinations d JOIN extensions e ON e.id = d.extension_id`

func scanFaxDest(r interface{ Scan(...any) error }) (fax.Destination, error) {
	var d fax.Destination
	err := r.Scan(&d.ID, &d.Name, &d.Extension, &d.Email, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// ListFaxDestinations returns every destination by name.
func (s *Store) ListFaxDestinations(ctx context.Context) ([]fax.Destination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+faxDestCols+faxDestFrom+` ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []fax.Destination{}
	for rows.Next() {
		d, err := scanFaxDest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetFaxDestination returns one destination.
func (s *Store) GetFaxDestination(ctx context.Context, id int64) (fax.Destination, error) {
	d, err := scanFaxDest(s.db.QueryRowContext(ctx, `SELECT `+faxDestCols+faxDestFrom+` WHERE d.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

func extensionID(ctx context.Context, tx *sql.Tx, number string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM extensions WHERE number = $1`, number).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fieldError("extension", "extension "+number+" does not exist")
	}
	return id, err
}

// CreateFaxDestination adds a destination; a taken name is ErrConflict.
func (s *Store) CreateFaxDestination(ctx context.Context, actor string, in FaxDestinationInput) (fax.Destination, error) {
	var id int64
	err := s.configChange(ctx, actor, "create", "fax_destination", nil, func(tx *sql.Tx) (int64, error) {
		ext, err := extensionID(ctx, tx, in.Extension)
		if err != nil {
			return 0, err
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO fax_destinations (name, extension_id, email) VALUES ($1, $2, $3) RETURNING id`,
			in.Name, ext, in.Email).Scan(&id)
		return id, err
	})
	if err != nil {
		return fax.Destination{}, err
	}
	return s.GetFaxDestination(ctx, id)
}

// UpdateFaxDestination changes the owner and the address; the name never
// changes, because routes and IVR keys refer to it.
func (s *Store) UpdateFaxDestination(ctx context.Context, actor string, id int64, in FaxDestinationInput) (fax.Destination, error) {
	err := s.configChange(ctx, actor, "update", "fax_destination", nil, func(tx *sql.Tx) (int64, error) {
		ext, err := extensionID(ctx, tx, in.Extension)
		if err != nil {
			return id, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE fax_destinations SET extension_id = $2, email = $3, updated_at = now() WHERE id = $1`, id, ext, in.Email)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
	if err != nil {
		return fax.Destination{}, err
	}
	return s.GetFaxDestination(ctx, id)
}

// DeleteFaxDestination removes a destination no route and no IVR key names.
func (s *Store) DeleteFaxDestination(ctx context.Context, actor string, id int64) error {
	return s.configChange(ctx, actor, "delete", "fax_destination", nil, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM fax_destinations WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return id, ErrNotFound
			}
			return id, err
		}
		var users []string
		for _, q := range []struct{ what, sql string }{
			{"inbound route", `SELECT name FROM inbound_routes WHERE destination_kind = 'fax' AND destination = $1`},
			{"IVR menu", `SELECT m.name FROM ivr_menus m WHERE (m.timeout_kind = 'fax' AND m.timeout_target = $1) OR (m.invalid_kind = 'fax' AND m.invalid_target = $1)
				OR EXISTS (SELECT 1 FROM ivr_menu_options o WHERE o.menu_id = m.id AND o.dest_kind = 'fax' AND o.dest_target = $1)`},
		} {
			rows, err := tx.QueryContext(ctx, q.sql, name)
			if err != nil {
				return id, err
			}
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					_ = rows.Close()
					return id, err
				}
				users = append(users, q.what+" "+n)
			}
			if err := rows.Close(); err != nil {
				return id, err
			}
		}
		if len(users) > 0 {
			return id, &InUseError{Msg: "fax destination " + name + " is used by " + strings.Join(users, ", ")}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM fax_destinations WHERE id = $1`, id)
		return id, err
	})
}

// --- faxes ----------------------------------------------------------------

const faxCols = `f.id, f.direction, f.status, f.peer_number, f.local_number, COALESCE(d.name, ''), f.owner_extension,
	f.pages, f.mode, f.bit_rate, f.result_code, f.result_text, f.object_key, f.correlation_id, f.created_by, f.created_at,
	f.finished_at, f.attempts, CASE WHEN f.status = 'queued' THEN f.next_attempt_at END, f.mail_to, f.mail_status, f.trunk_name`
const faxFrom = ` FROM faxes f LEFT JOIN fax_destinations d ON d.id = f.destination_id`

func scanFax(r interface{ Scan(...any) error }) (fax.Fax, error) {
	var f fax.Fax
	var code sql.NullInt32
	var fin, next sql.NullTime
	if err := r.Scan(&f.ID, &f.Direction, &f.Status, &f.PeerNumber, &f.LocalNumber, &f.Destination, &f.OwnerExtension,
		&f.Pages, &f.Mode, &f.BitRate, &code, &f.ResultText, &f.ObjectKey, &f.CorrelationID, &f.CreatedBy, &f.CreatedAt,
		&fin, &f.Attempts, &next, &f.MailTo, &f.MailStatus, &f.TrunkName); err != nil {
		return f, err
	}
	if code.Valid {
		c := int(code.Int32)
		f.ResultCode = &c
	}
	if fin.Valid {
		f.FinishedAt = &fin.Time
	}
	if next.Valid {
		f.NextAttemptAt = &next.Time
	}
	f.HasPDF = f.ObjectKey != ""
	if f.MailTo == MailToOwner {
		f.MailTo = "" // the owner's own address; the list does not expose it
	}
	return f, nil
}

// ListFaxes returns faxes newest first.
func (s *Store) ListFaxes(ctx context.Context, fl fax.ListFilter) ([]fax.Fax, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if fl.Direction != "" {
		add("f.direction = $%d", fl.Direction)
	}
	if fl.Status != "" {
		add("f.status = $%d", fl.Status)
	}
	if fl.Before > 0 {
		add("f.id < $%d", fl.Before)
	}
	if fl.Owners != nil {
		add("f.owner_extension = ANY($%d::text[])", fl.Owners)
	}
	limit := fl.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + faxCols + faxFrom
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY f.id DESC LIMIT $%d`, len(args)) //nolint:gosec // G202: only a placeholder number is formatted in; every value is an argument
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []fax.Fax{}
	for rows.Next() {
		f, err := scanFax(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFax returns one fax.
func (s *Store) GetFax(ctx context.Context, id int64) (fax.Fax, error) {
	f, err := scanFax(s.db.QueryRowContext(ctx, `SELECT `+faxCols+faxFrom+` WHERE f.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// OutboundFax is a fax the API queues.
type OutboundFax struct {
	Peer     string
	CallerID string
	Actor    string
	Pages    int
}

// CreateOutboundFax inserts the row of a fax to send. It is not claimable
// until SetFaxObject stores the document, so a node never sees a row whose
// PDF is not in place.
func (s *Store) CreateOutboundFax(ctx context.Context, o OutboundFax) (fax.Fax, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO faxes (direction, status, peer_number, local_number, pages, created_by)
		VALUES ('outbound', 'queued', $1, $2, $3, $4) RETURNING id`, o.Peer, o.CallerID, o.Pages, o.Actor).Scan(&id); err != nil {
		return fax.Fax{}, err
	}
	if err := insertAudit(ctx, s.db, o.Actor, "create", "fax", fmt.Sprint(id)); err != nil {
		return fax.Fax{}, err
	}
	return s.GetFax(ctx, id)
}

// SetFaxObject records where a fax's PDF is stored.
func (s *Store) SetFaxObject(ctx context.Context, id int64, key string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE faxes SET object_key = $2 WHERE id = $1`, id, key)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DeleteFax removes a fax that is not in progress and returns its object key,
// which the caller deletes from storage. A queued fax is cancelled and removed.
func (s *Store) DeleteFax(ctx context.Context, actor string, id int64) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `DELETE FROM faxes WHERE id = $1 AND status NOT IN ('sending', 'receiving') RETURNING object_key`, id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		if _, gerr := s.GetFax(ctx, id); gerr != nil {
			return "", gerr
		}
		return "", fax.ErrNotCancellable
	}
	if err != nil {
		return "", err
	}
	return key, insertAudit(ctx, s.db, actor, "delete", "fax", fmt.Sprint(id))
}

// CancelFax stops a queued or sending outbound fax; a sending one is dropped
// at its node's next lease renewal.
func (s *Store) CancelFax(ctx context.Context, actor string, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE faxes SET status = 'cancelled', result_text = 'cancelled', finished_at = now(),
		claimed_by = '', lease_until = NULL WHERE id = $1 AND direction = 'outbound' AND status IN ('queued', 'sending')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, gerr := s.GetFax(ctx, id); gerr != nil {
			return gerr
		}
		return fax.ErrNotCancellable
	}
	return insertAudit(ctx, s.db, actor, "cancel", "fax", fmt.Sprint(id))
}

// FaxOwnerExtensions returns the extension numbers a user account is linked
// to: the faxes whose owner is among them are the user's.
func (s *Store) FaxOwnerExtensions(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.number FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id WHERE ue.user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// --- the hello-sip side ------------------------------------------------------

// Faxes is the fax.Store over PostgreSQL for hello-sip. Times are the
// database's clock, so nodes with skewed clocks agree on leases.
type Faxes struct{ db *sql.DB }

var _ fax.Store = (*Faxes)(nil)

// Faxes returns the fax store hello-sip uses.
func (s *Store) Faxes() *Faxes { return &Faxes{db: s.db} }

// BeginReceive implements fax.Store.
func (f *Faxes) BeginReceive(ctx context.Context, in fax.Receive) (int64, error) {
	var dest any
	if in.DestinationID > 0 {
		dest = in.DestinationID
	}
	var id int64
	err := f.db.QueryRowContext(ctx, `INSERT INTO faxes (direction, status, peer_number, local_number, destination_id, owner_extension, correlation_id)
		VALUES ('inbound', 'receiving', $1, $2, $3, $4, $5) RETURNING id`,
		trunc(in.PeerNumber, 40), trunc(in.LocalNumber, 40), dest, in.OwnerExtension, in.CorrelationID).Scan(&id)
	return id, err
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Finish implements fax.Store.
func (f *Faxes) Finish(ctx context.Context, id int64, fin fax.Finish) error {
	_, err := f.db.ExecContext(ctx, `UPDATE faxes SET status = $2, pages = $3, mode = $4, bit_rate = $5, result_code = $6,
			result_text = $7, object_key = COALESCE(NULLIF($8, ''), object_key), mail_to = $9,
			mail_status = CASE WHEN $9 <> '' AND $8 <> '' THEN 'pending' ELSE 'none' END, mail_next_at = now(),
			finished_at = now(), claimed_by = '', lease_until = NULL
		WHERE id = $1 AND status IN ('receiving', 'sending', 'queued')`,
		id, fin.Status, fin.Pages, fin.Mode, fin.BitRate, fin.ResultCode, trunc(fin.ResultText, 200), fin.ObjectKey, fin.MailTo)
	return err
}

// ClaimOutbound implements fax.Store.
func (f *Faxes) ClaimOutbound(ctx context.Context, node string, lease time.Duration, limit int) ([]fax.Fax, error) {
	// A send whose node vanished, on its last attempt, is over.
	if _, err := f.db.ExecContext(ctx, `UPDATE faxes SET status = 'failed', result_text = 'the sending node was lost',
			finished_at = now(), claimed_by = '', lease_until = NULL
		WHERE direction = 'outbound' AND status = 'sending' AND lease_until < now() AND attempts >= $1`, fax.MaxAttempts); err != nil {
		return nil, err
	}
	// The claim is one UPDATE over rows locked with SKIP LOCKED; the rows are
	// read afterwards, because a statement does not see its own changes.
	idrows, err := f.db.QueryContext(ctx, `UPDATE faxes SET status = 'sending', claimed_by = $1,
			lease_until = now() + make_interval(secs => $2), attempts = attempts + 1
		WHERE id IN (SELECT id FROM faxes WHERE direction = 'outbound' AND object_key <> ''
				AND ((status = 'queued' AND next_attempt_at <= now()) OR (status = 'sending' AND lease_until < now()))
			ORDER BY next_attempt_at, id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING id`, node, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for idrows.Next() {
		var id int64
		if err := idrows.Scan(&id); err != nil {
			_ = idrows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := idrows.Close(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := f.db.QueryContext(ctx, `SELECT `+faxCols+faxFrom+` WHERE f.id = ANY($1::bigint[]) ORDER BY f.id`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []fax.Fax
	for rows.Next() {
		x, err := scanFax(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Renew implements fax.Store.
func (f *Faxes) Renew(ctx context.Context, id int64, node string, lease time.Duration) (bool, error) {
	res, err := f.db.ExecContext(ctx, `UPDATE faxes SET lease_until = now() + make_interval(secs => $3)
		WHERE id = $1 AND claimed_by = $2 AND status = 'sending'`, id, node, lease.Seconds())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Retry implements fax.Store.
func (f *Faxes) Retry(ctx context.Context, id int64, after time.Duration, text string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE faxes SET status = 'queued', next_attempt_at = now() + make_interval(secs => $2),
			result_text = $3, claimed_by = '', lease_until = NULL
		WHERE id = $1 AND status = 'sending'`, id, after.Seconds(), trunc(text, 200))
	return err
}

// Link implements fax.Store.
func (f *Faxes) Link(ctx context.Context, id int64, correlationID string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE faxes SET correlation_id = $2 WHERE id = $1`, id, correlationID)
	return err
}

// --- email -------------------------------------------------------------------

// ClaimFaxMail takes up to limit faxes whose email is due, or whose sender
// went away (claimed longer than stale ago), and resolves their recipients: the
// destination's address, or the enabled users linked to the owner extension.
func (s *Store) ClaimFaxMail(ctx context.Context, limit int, stale time.Duration) ([]fax.MailJob, error) {
	rows, err := s.db.QueryContext(ctx, `WITH c AS (
			UPDATE faxes SET mail_status = 'sending', mail_attempts = mail_attempts + 1, mail_next_at = now() + make_interval(secs => $2)
			WHERE id IN (SELECT id FROM faxes WHERE (mail_status = 'pending' AND mail_next_at <= now())
					OR (mail_status = 'sending' AND mail_next_at <= now())
				ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED)
			RETURNING id, mail_to, peer_number, pages, created_at, object_key, mail_attempts, owner_extension)
		SELECT id, mail_to, peer_number, pages, created_at, object_key, mail_attempts, owner_extension FROM c ORDER BY id`, limit, stale.Seconds())
	if err != nil {
		return nil, err
	}
	var out []fax.MailJob
	for rows.Next() {
		var m fax.MailJob
		var to string
		if err := rows.Scan(&m.ID, &to, &m.Peer, &m.Pages, &m.CreatedAt, &m.Object, &m.Attempts, &m.Owner); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if to != MailToOwner {
			m.To = []string{to}
		}
		out = append(out, m)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].To != nil {
			continue
		}
		urows, err := s.db.QueryContext(ctx, `SELECT DISTINCT u.email FROM users u
			JOIN user_extensions ue ON ue.user_id = u.id JOIN extensions e ON e.id = ue.extension_id
			WHERE e.number = $1 AND u.enabled AND u.email <> '' ORDER BY u.email`, out[i].Owner)
		if err != nil {
			return nil, err
		}
		for urows.Next() {
			var a string
			if err := urows.Scan(&a); err != nil {
				_ = urows.Close()
				return nil, err
			}
			out[i].To = append(out[i].To, a)
		}
		if err := urows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FinishFaxMail records the outcome of one send: sent, retry at retryAt, or
// failed for good (final), with the error text.
func (s *Store) FinishFaxMail(ctx context.Context, id int64, errText string, final bool, retryAt time.Time) error {
	switch {
	case errText == "":
		_, err := s.db.ExecContext(ctx, `UPDATE faxes SET mail_status = 'sent', mail_error = '' WHERE id = $1`, id)
		return err
	case final:
		_, err := s.db.ExecContext(ctx, `UPDATE faxes SET mail_status = 'failed', mail_error = $2 WHERE id = $1`, id, trunc(errText, 200))
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE faxes SET mail_status = 'pending', mail_next_at = $3, mail_error = $2 WHERE id = $1`, id, trunc(errText, 200), retryAt)
	return err
}

// --- retention ------------------------------------------------------------------

// ExpiredFaxes returns up to limit finished faxes older than days, with their
// object keys, oldest first. The caller deletes the objects, then the rows.
func (s *Store) ExpiredFaxes(ctx context.Context, days, limit int) (ids []int64, keys []string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, object_key FROM faxes
		WHERE status NOT IN ('queued', 'sending', 'receiving') AND created_at < now() - make_interval(days => $1)
		ORDER BY id LIMIT $2`, days, limit)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, nil, err
		}
		ids, keys = append(ids, id), append(keys, k)
	}
	return ids, keys, rows.Err()
}

// DeleteFaxRows removes fax rows by id.
func (s *Store) DeleteFaxRows(ctx context.Context, ids []int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM faxes WHERE id = ANY($1::bigint[])`, ids)
	return err
}

// ExpireStuckFaxes fails inbound faxes still "receiving" after a node loss,
// and sends nobody's lease will ever finish; it returns how many it closed.
func (s *Store) ExpireStuckFaxes(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE faxes SET status = 'failed', result_text = 'the call ended without a result',
			finished_at = now() WHERE direction = 'inbound' AND status = 'receiving' AND created_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
