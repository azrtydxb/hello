package store

// The phone LDAP directory (spec pbx-gaps S-24 to S-26): the per-phone bind
// password, the group switch and what the LDAP server reads. The password is
// sealed, not hashed: the rendered configuration has to carry it.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
	provbook "github.com/azrtydxb/hello/internal/prov/phonebook"
)

const (
	// ldapPasswordLen is the length of a generated LDAP password (S-24).
	ldapPasswordLen = 32
	// ldapPrevGrace is the longest a previous password stays valid after a
	// rotation when the phone does not fetch its new configuration.
	ldapPrevGrace = 7 * 24 * time.Hour
	// ldapBindTouch keeps ldap_last_bind_at from being written on every bind.
	ldapBindTouch = time.Minute
)

// ldapAlphabet is letters and digits, so the password survives every
// vendor's config syntax and an LDAP client's quoting unchanged.
const ldapAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func newLDAPPassword() string {
	b := make([]byte, ldapPasswordLen)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(ldapAlphabet))))
		if err != nil {
			panic(err) // crypto/rand does not fail on supported platforms
		}
		b[i] = ldapAlphabet[n.Int64()]
	}
	return string(b)
}

// ldapHost is the host of HELLO_PROV_PUBLIC_URL, where phones reach LDAPS.
func (p ProvSettings) ldapHost() string {
	u, err := url.Parse(p.PublicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ldapOn reports whether phones are given LDAP keys at all.
func (p ProvSettings) ldapOn() bool { return p.LDAPBase != "" && p.PublicURL != "" }

// ldapInfo is RenderData.LDAP for a phone: nil when LDAP is not served, the
// model has no LDAP or the phone's group has it off. A phone of an enabled
// group gets its password here the first time it is needed.
func (s *Store) ldapInfo(ctx context.Context, phoneID int64, v prov.Vendor, model string) (*prov.LDAPInfo, error) {
	if !s.prov.ldapOn() || !prov.SupportsLDAP(v, model) {
		return nil, nil
	}
	var on bool
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(g.ldap_enabled, FALSE) FROM phones p
		LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&on)
	if err != nil || !on {
		return nil, err
	}
	pw, err := s.ensureLDAPPassword(ctx, phoneID)
	if err != nil {
		return nil, err
	}
	return prov.NewLDAPInfo(s.prov.ldapHost(), s.prov.LDAPPort, s.prov.LDAPBase, prov.LDAPBindDN(s.prov.LDAPBase, phoneID), pw), nil
}

// ensureLDAPPassword opens the phone's LDAP password, generating and sealing
// one when it has none. Two replicas racing agree: the conditional update
// lets one write and the other reads what was written.
func (s *Store) ensureLDAPPassword(ctx context.Context, phoneID int64) (string, error) {
	if s.box == nil {
		return "", prov.ErrSealed
	}
	for range 3 {
		var enc []byte
		err := s.db.QueryRowContext(ctx, `SELECT ldap_password_enc FROM phones WHERE id = $1`, phoneID).Scan(&enc)
		if errors.Is(err, sql.ErrNoRows) {
			return "", prov.ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if enc != nil {
			pw, err := s.box.Open(enc, prov.PhoneLDAPAAD(phoneID))
			if err != nil {
				return "", prov.ErrSealed
			}
			return pw, nil
		}
		pw := newLDAPPassword()
		sealed, err := s.box.Seal(pw, prov.PhoneLDAPAAD(phoneID))
		if err != nil {
			return "", err
		}
		res, err := s.db.ExecContext(ctx, `UPDATE phones SET ldap_password_enc = $2, ldap_dn = $3
			WHERE id = $1 AND ldap_password_enc IS NULL`, phoneID, sealed, prov.LDAPBindDN(s.prov.LDAPBase, phoneID))
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return pw, nil
		}
	}
	return "", errors.New("store: ldap password not settled")
}

// RotateLDAPPassword gives a phone a new LDAP password and returns it, to be
// shown once. The old one keeps working until the phone has fetched its new
// configuration (MarkFetched) or ldapPrevGrace passes.
func (s *Store) RotateLDAPPassword(ctx context.Context, actor string, phoneID int64) (string, error) {
	if s.box == nil {
		return "", errors.New("store: no secret box to seal provisioning secrets")
	}
	if !s.prov.ldapOn() {
		return "", conflict("LDAP is not enabled on this server")
	}
	pw := newLDAPPassword()
	err := s.provChange(ctx, actor, "ldap-password-rotate", "phone", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(phoneID, 10)
		var cur []byte
		err := tx.QueryRowContext(ctx, `SELECT ldap_password_enc FROM phones WHERE id = $1 FOR UPDATE`, phoneID).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return sid, false, ErrNotFound
		}
		if err != nil {
			return sid, false, err
		}
		sealed, err := s.box.Seal(pw, prov.PhoneLDAPAAD(phoneID))
		if err != nil {
			return sid, false, err
		}
		var prev []byte
		if cur != nil {
			old, err := s.box.Open(cur, prov.PhoneLDAPAAD(phoneID))
			if err == nil {
				if prev, err = s.box.Seal(old, prov.PhoneLDAPPrevAAD(phoneID)); err != nil {
					return sid, false, err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE phones SET ldap_password_enc = $2, ldap_prev_password_enc = $3,
			ldap_rotated_at = now(), ldap_dn = $4, updated_at = now() WHERE id = $1`,
			phoneID, sealed, prev, prov.LDAPBindDN(s.prov.LDAPBase, phoneID))
		return sid, false, err
	})
	if err != nil {
		return "", err
	}
	return pw, nil
}

// PhoneLDAP is the LDAP state of a phone as the API shows it; it never
// carries the password.
type PhoneLDAP struct {
	// Served: hello-control serves LDAP at all. Supported: the phone's model
	// has an LDAP client the template writes keys for. GroupEnabled: the
	// phone's group has LDAP on.
	Served       bool       `json:"served"`
	Supported    bool       `json:"supported"`
	GroupEnabled bool       `json:"groupEnabled"`
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	Base         string     `json:"base"`
	BindDN       string     `json:"bindDn"`
	PasswordSet  bool       `json:"passwordSet"`
	RotatedAt    *time.Time `json:"rotatedAt"`
	// PreviousValid: the password before the last rotation still binds,
	// because the phone has not fetched its new configuration yet.
	PreviousValid bool       `json:"previousValid"`
	LastBindAt    *time.Time `json:"lastBindAt"`
}

// GetPhoneLDAP returns the phone's LDAP state.
func (s *Store) GetPhoneLDAP(ctx context.Context, phoneID int64) (PhoneLDAP, error) {
	var (
		out            PhoneLDAP
		vendor, model  string
		enc, prev      []byte
		rotated, lastB sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `SELECT p.vendor, p.model, COALESCE(g.ldap_enabled, FALSE), p.ldap_password_enc,
		p.ldap_prev_password_enc, p.ldap_rotated_at, p.ldap_last_bind_at
		FROM phones p LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).
		Scan(&vendor, &model, &out.GroupEnabled, &enc, &prev, &rotated, &lastB)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.Served = s.prov.ldapOn()
	out.Supported = prov.SupportsLDAP(prov.Vendor(vendor), model)
	if out.Served {
		out.Host, out.Port, out.Base = s.prov.ldapHost(), s.prov.LDAPPort, "ou=phonebook,"+s.prov.LDAPBase
		out.BindDN = prov.LDAPBindDN(s.prov.LDAPBase, phoneID)
	}
	out.PasswordSet = enc != nil
	if rotated.Valid {
		out.RotatedAt = &rotated.Time
		out.PreviousValid = prev != nil && time.Since(rotated.Time) < ldapPrevGrace
	}
	if lastB.Valid {
		out.LastBindAt = &lastB.Time
	}
	return out, nil
}

// SetPhoneGroupLDAP turns the LDAP directory on or off for a group's phones.
func (s *Store) SetPhoneGroupLDAP(ctx context.Context, actor string, id int64, enabled bool) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE phone_groups SET ldap_enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		action := "ldap-off"
		if enabled {
			action = "ldap-on"
		}
		return insertAudit(ctx, tx, actor, action, "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}

// Authenticate implements ldapdir.Backend: whether password is the phone's
// current LDAP password, or its previous one until the phone has fetched the
// new configuration. A phone that is disabled, or whose group has LDAP off,
// does not bind. Both passwords are compared in constant time whatever the
// outcome, and an unknown phone costs the same as a known one.
func (s *Store) Authenticate(ctx context.Context, phoneID int64, password string) (bool, error) {
	if s.box == nil {
		return false, prov.ErrSealed
	}
	var (
		cur, prev     []byte
		rotated, last sql.NullTime
		enabled, on   bool
	)
	err := s.db.QueryRowContext(ctx, `SELECT p.ldap_password_enc, p.ldap_prev_password_enc, p.ldap_rotated_at, p.ldap_last_bind_at,
		p.enabled, COALESCE(g.ldap_enabled, FALSE)
		FROM phones p LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).
		Scan(&cur, &prev, &rotated, &last, &enabled, &on)
	if errors.Is(err, sql.ErrNoRows) {
		subtle.ConstantTimeCompare([]byte(password), []byte(password))
		return false, nil
	}
	if err != nil {
		return false, err
	}
	match := func(enc []byte, aad string) bool {
		if enc == nil {
			subtle.ConstantTimeCompare([]byte(password), []byte(password))
			return false
		}
		pw, err := s.box.Open(enc, aad)
		return err == nil && subtle.ConstantTimeCompare([]byte(pw), []byte(password)) == 1
	}
	ok := match(cur, prov.PhoneLDAPAAD(phoneID))
	if !ok && rotated.Valid && time.Since(rotated.Time) < ldapPrevGrace {
		ok = match(prev, prov.PhoneLDAPPrevAAD(phoneID))
	}
	if !ok || !enabled || !on {
		return false, nil
	}
	if !last.Valid || time.Since(last.Time) > ldapBindTouch {
		// Best effort: a phone is not refused because the stamp failed.
		_, _ = s.db.ExecContext(ctx, `UPDATE phones SET ldap_last_bind_at = now() WHERE id = $1`, phoneID)
	}
	return true, nil
}

// publicBookRefs is the set an anonymous bind sees: the extensions and the
// company book.
func publicBookRefs(ctx context.Context, q querier) ([]bookRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, version, kind FROM phonebooks
		WHERE kind IN ('extensions', 'company')
		ORDER BY CASE kind WHEN 'extensions' THEN 0 ELSE 1 END, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []bookRef
	for rows.Next() {
		var r bookRef
		if err := rows.Scan(&r.id, &r.name, &r.version, &r.kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ldapRefs is the delivered set of a phone that may use LDAP, or the public
// set for phone id 0.
func (s *Store) ldapRefs(ctx context.Context, phoneID int64) ([]bookRef, error) {
	if phoneID == 0 {
		return publicBookRefs(ctx, s.db)
	}
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT p.enabled AND COALESCE(g.ldap_enabled, FALSE) FROM phones p
		LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !ok) {
		return nil, prov.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return phoneBookRefs(ctx, s.db, phoneID)
}

// LDAPVersion implements ldapdir.Backend: the version of the set the phone's
// LDAP search sees, the one its XML directory is rendered from.
func (s *Store) LDAPVersion(ctx context.Context, phoneID int64) (prov.BooksVersion, error) {
	refs, err := s.ldapRefs(ctx, phoneID)
	if err != nil {
		return prov.BooksVersion{}, err
	}
	v, err := s.versionOf(ctx, refs)
	if err != nil {
		return v, err
	}
	if phoneID == 0 {
		v.Key = "public;" + v.Key
	}
	return v, nil
}

// LDAPBooks implements ldapdir.Backend.
func (s *Store) LDAPBooks(ctx context.Context, phoneID int64) ([]provbook.Book, error) {
	refs, err := s.ldapRefs(ctx, phoneID)
	if err != nil {
		return nil, err
	}
	return s.booksOf(ctx, refs)
}
