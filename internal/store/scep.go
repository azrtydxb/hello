package store

// SCEP and 802.1X (migration 00076, spec phone-feature-parity S-25): the
// Hello device CA with its sealed key, the per-phone one-time challenge
// passwords, the issued certificates and their revocation, the reusable
// 802.1X profiles and each phone group's SCEP switch and wired profile.
// Provisioning reads them (RenderData.SCEP and Dot1X), so a change does not
// bump the configuration revision: the automatic resync notices the phone's
// render data changed and queues a check-sync.

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/routing"
)

var _ prov.SCEPStore = (*Store)(nil)

// SCEPCAView is the active device CA as the API shows it: never the key.
type SCEPCAView struct {
	ID          int64     `json:"id"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Fingerprint string    `json:"fingerprintSha256"`
	Source      string    `json:"source"`
	CertPEM     string    `json:"certPem"`
	CreatedAt   time.Time `json:"createdAt"`
}

// SCEPCertificate is one issued certificate.
type SCEPCertificate struct {
	ID           int64      `json:"id"`
	CAID         int64      `json:"caId"`
	PhoneID      *int64     `json:"phoneId"`
	MAC          string     `json:"mac"`
	Serial       string     `json:"serial"`
	Subject      string     `json:"subject"`
	Kind         string     `json:"kind"`
	NotBefore    time.Time  `json:"notBefore"`
	NotAfter     time.Time  `json:"notAfter"`
	IssuedAt     time.Time  `json:"issuedAt"`
	RevokedAt    *time.Time `json:"revokedAt"`
	RevokeReason *string    `json:"revokeReason"`
	Status       string     `json:"status"` // active, expired or revoked
}

// SCEPCertFilter narrows ListSCEPCertificates.
type SCEPCertFilter struct {
	PhoneID *int64
	Status  string // "", active, expired or revoked
}

// RevokeReasons are the reasons an administrator may give.
var RevokeReasons = []string{"unspecified", "key_compromise", "superseded", "cessation_of_operation"}

// Dot1XProfile is an 802.1X profile as the API shows it: the password is
// write-only (HasPassword says whether one is stored).
type Dot1XProfile struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	Method            string  `json:"method"`
	Identity          string  `json:"identity"`
	AnonymousIdentity string  `json:"anonymousIdentity"`
	HasPassword       bool    `json:"hasPassword"`
	CACertPEM         string  `json:"caCertPem"`
	ServerDomain      string  `json:"serverDomain"`
	UsedBy            []int64 `json:"usedByGroups"`
	// UsedByWifi are the enterprise Wi-Fi profiles (S-23) that authenticate with it.
	UsedByWifi []int64   `json:"usedByWifiProfiles"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Dot1XInput is a profile create or replace. Password nil keeps the stored
// one (on create: none); "" removes it.
type Dot1XInput struct {
	Name              string
	Method            string
	Identity          string
	AnonymousIdentity string
	Password          *string
	CACertPEM         string
	ServerDomain      string
}

// GroupSecurity is a phone group's SCEP switch and wired 802.1X profile.
type GroupSecurity struct {
	GroupID          int64  `json:"groupId"`
	SCEPEnabled      bool   `json:"scepEnabled"`
	CertValidityDays int    `json:"certValidityDays"`
	WiredDot1XID     *int64 `json:"wiredDot1xProfileId"`
}

func fieldErrs(fs []prov.Dot1XField) error {
	out := make([]routing.FieldError, len(fs))
	for i, f := range fs {
		out[i] = routing.FieldError{Path: f.Path, Message: f.Message}
	}
	return &ValidationError{Fields: out}
}

// --- the device CA ---

func (s *Store) openCA(id int64, certPEM string, keyEnc []byte) (*prov.SCEPCA, error) {
	if s.box == nil {
		return nil, prov.ErrSealed
	}
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil {
		return nil, errors.New("store: the SCEP CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, err
	}
	der, err := s.box.Open(keyEnc, prov.SCEPCAKeyAAD(id))
	if err != nil {
		return nil, prov.ErrSealed
	}
	key, err := prov.UnmarshalRSAKey([]byte(der))
	if err != nil {
		return nil, prov.ErrSealed
	}
	return &prov.SCEPCA{ID: id, Cert: cert, Key: key}, nil
}

// SCEPActiveCA implements prov.SCEPStore.
func (s *Store) SCEPActiveCA(ctx context.Context) (*prov.SCEPCA, error) {
	var (
		id      int64
		certPEM string
		keyEnc  []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, cert_pem, key_enc FROM scep_ca WHERE active`).Scan(&id, &certPEM, &keyEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, prov.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.openCA(id, certPEM, keyEnc)
}

// GetSCEPCA returns the active CA, nil when none has been created.
func (s *Store) GetSCEPCA(ctx context.Context) (*SCEPCAView, error) {
	var v SCEPCAView
	err := s.db.QueryRowContext(ctx, `SELECT id, cert_pem, source, created_at FROM scep_ca WHERE active`).
		Scan(&v.ID, &v.CertPEM, &v.Source, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // none yet is not an error
	}
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode([]byte(v.CertPEM))
	if b == nil {
		return nil, errors.New("store: the SCEP CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, err
	}
	v.Subject, v.NotBefore, v.NotAfter, v.Fingerprint = cert.Subject.String(), cert.NotBefore, cert.NotAfter, prov.CertFingerprint(cert.Raw)
	return &v, nil
}

// SetSCEPCA makes a CA the active one, its key sealed. With a CA already
// active it is a conflict unless replace is set; the replaced CA stays (its
// certificates keep their rows, and it is no longer used to issue or renew).
func (s *Store) SetSCEPCA(ctx context.Context, actor, certPEM string, key *rsa.PrivateKey, source string, replace bool) (*SCEPCAView, error) {
	if s.box == nil {
		return nil, prov.ErrSealed
	}
	der, err := prov.MarshalRSAKey(key)
	if err != nil {
		return nil, err
	}
	err = s.provChange(ctx, actor, "create", "scep_ca", func(tx *sql.Tx) (string, bool, error) {
		var active sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM scep_ca WHERE active FOR UPDATE`).Scan(&active); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if active.Valid && !replace {
			return "", false, conflict("a device CA exists; set replace to replace it (phones then enrol again with their challenge)")
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT nextval('scep_ca_id_seq')`).Scan(&id); err != nil {
			return "", false, err
		}
		enc, err := s.box.Seal(string(der), prov.SCEPCAKeyAAD(id))
		if err != nil {
			return "", false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scep_ca SET active = FALSE WHERE active`); err != nil {
			return "", false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO scep_ca (id, cert_pem, key_enc, source) VALUES ($1, $2, $3, $4)`,
			id, strings.TrimSpace(certPEM)+"\n", enc, source); err != nil {
			return "", false, err
		}
		return strconv.FormatInt(id, 10), false, nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetSCEPCA(ctx)
}

// --- the SCEP endpoint ---

// SCEPState implements prov.SCEPStore.
func (s *Store) SCEPState(ctx context.Context, phoneID int64) (prov.SCEPPhone, error) {
	var (
		on   sql.NullBool
		days sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT g.scep_enabled, g.scep_validity_days FROM phones p
		LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&on, &days)
	if errors.Is(err, sql.ErrNoRows) {
		return prov.SCEPPhone{}, prov.ErrNotFound
	}
	if err != nil {
		return prov.SCEPPhone{}, err
	}
	st := prov.SCEPPhone{Enabled: on.Bool, ValidityDays: int(days.Int64)}
	if !st.Enabled {
		return st, nil
	}
	ca, err := s.SCEPActiveCA(ctx)
	switch {
	case errors.Is(err, prov.ErrNotFound):
		return st, nil
	case err != nil:
		return st, err
	}
	st.CA = ca
	return st, nil
}

// SCEPIssue implements prov.SCEPStore.
func (s *Store) SCEPIssue(ctx context.Context, in prov.SCEPIssue) (*x509.Certificate, error) {
	var out *x509.Certificate
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phones WHERE id = $1 FOR UPDATE`, in.PhoneID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return prov.ErrNotFound
			}
			return err
		}
		// A retried transaction: the same key got a live certificate of
		// this phone and CA within the retry window. A renewal months later
		// that keeps its key is a new issue, not a retry.
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT cert_pem FROM scep_certificates
			WHERE phone_id = $1 AND ca_id = $2 AND key_sha256 = $3 AND revoked_at IS NULL AND not_after > now()
			  AND issued_at > now() - make_interval(secs => $4)
			ORDER BY issued_at DESC LIMIT 1`, in.PhoneID, in.CAID, in.KeySHA256, prov.SCEPRetryWindow.Seconds()).Scan(&existing)
		switch {
		case err == nil:
			out, err = parseCertPEM(existing)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		kind := "enrol"
		switch {
		case in.Challenge != "" && in.RenewalSerial == "":
			var enc []byte
			err := tx.QueryRowContext(ctx, `SELECT challenge_enc FROM scep_challenges WHERE phone_id = $1 FOR UPDATE`, in.PhoneID).Scan(&enc)
			if errors.Is(err, sql.ErrNoRows) {
				return prov.ErrSCEPRefused
			}
			if err != nil {
				return err
			}
			if s.box == nil {
				return prov.ErrSealed
			}
			want, err := s.box.Open(enc, prov.SCEPChallengeAAD(in.PhoneID))
			if err != nil {
				return prov.ErrSealed
			}
			if !prov.ChallengeEqual(want, in.Challenge) {
				return prov.ErrSCEPRefused
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM scep_challenges WHERE phone_id = $1`, in.PhoneID); err != nil {
				return err
			}
		case in.RenewalSerial != "" && in.Challenge == "":
			kind = "renewal"
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM scep_certificates
				WHERE serial = $1 AND phone_id = $2 AND ca_id = $3 AND revoked_at IS NULL AND not_after > now()`,
				strings.ToLower(in.RenewalSerial), in.PhoneID, in.CAID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return prov.ErrSCEPRefused
			}
			if err != nil {
				return err
			}
		default:
			return prov.ErrSCEPRefused
		}
		cert, err := in.Sign()
		if err != nil {
			return err
		}
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
		if _, err := tx.ExecContext(ctx, `INSERT INTO scep_certificates
			(ca_id, phone_id, mac, serial, subject, key_sha256, cert_pem, kind, not_before, not_after)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			in.CAID, in.PhoneID, in.MAC, prov.SerialHex(cert.SerialNumber), cert.Subject.String(), in.KeySHA256,
			certPEM, kind, cert.NotBefore, cert.NotAfter); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, "phone:"+in.MAC, "scep_"+kind, "scep_certificate", prov.SerialHex(cert.SerialNumber)); err != nil {
			return err
		}
		out = cert
		return nil
	})
	return out, err
}

func parseCertPEM(s string) (*x509.Certificate, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, errors.New("store: a stored certificate is not PEM")
	}
	return x509.ParseCertificate(b.Bytes)
}

// SCEPRevoked implements prov.SCEPStore.
func (s *Store) SCEPRevoked(ctx context.Context, caID int64) ([]x509.RevocationListEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT serial, revoked_at FROM scep_certificates
		WHERE ca_id = $1 AND revoked_at IS NOT NULL AND not_after > now() - interval '1 day' ORDER BY revoked_at`, caID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []x509.RevocationListEntry
	for rows.Next() {
		var (
			serial string
			at     time.Time
		)
		if err := rows.Scan(&serial, &at); err != nil {
			return nil, err
		}
		n, ok := new(big.Int).SetString(serial, 16)
		if !ok {
			return nil, fmt.Errorf("store: certificate serial %q is not hex", serial)
		}
		out = append(out, x509.RevocationListEntry{SerialNumber: n, RevocationTime: at})
	}
	return out, rows.Err()
}

// --- issued certificates ---

const scepCertCols = `id, ca_id, phone_id, mac, serial, subject, kind, not_before, not_after, issued_at, revoked_at, revoke_reason`

func scanSCEPCert(sc interface{ Scan(...any) error }) (SCEPCertificate, error) {
	var (
		c      SCEPCertificate
		phone  sql.NullInt64
		rev    sql.NullTime
		reason sql.NullString
	)
	if err := sc.Scan(&c.ID, &c.CAID, &phone, &c.MAC, &c.Serial, &c.Subject, &c.Kind, &c.NotBefore, &c.NotAfter, &c.IssuedAt, &rev, &reason); err != nil {
		return c, err
	}
	if phone.Valid {
		c.PhoneID = &phone.Int64
	}
	switch {
	case rev.Valid:
		c.RevokedAt, c.RevokeReason, c.Status = &rev.Time, &reason.String, "revoked"
	case !time.Now().Before(c.NotAfter):
		c.Status = "expired"
	default:
		c.Status = "active"
	}
	return c, nil
}

// ListSCEPCertificates lists issued certificates, newest first, at most 1000.
func (s *Store) ListSCEPCertificates(ctx context.Context, f SCEPCertFilter) ([]SCEPCertificate, error) {
	q := `SELECT ` + scepCertCols + ` FROM scep_certificates WHERE ($1::bigint IS NULL OR phone_id = $1)`
	switch f.Status {
	case "active":
		q += ` AND revoked_at IS NULL AND not_after > now()`
	case "expired":
		q += ` AND revoked_at IS NULL AND not_after <= now()`
	case "revoked":
		q += ` AND revoked_at IS NOT NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY issued_at DESC, id DESC LIMIT 1000`, f.PhoneID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SCEPCertificate{}
	for rows.Next() {
		c, err := scanSCEPCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeSCEPCertificate revokes a certificate: it can no longer renew and
// is on the CRL. ErrNotFound for an unknown id; a conflict when it is
// already revoked.
func (s *Store) RevokeSCEPCertificate(ctx context.Context, actor string, id int64, reason string) (SCEPCertificate, error) {
	err := s.provChange(ctx, actor, "revoke", "scep_certificate", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		var revoked sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM scep_certificates WHERE id = $1 FOR UPDATE`, id).Scan(&revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return rid, false, ErrNotFound
			}
			return rid, false, err
		}
		if revoked.Valid {
			return rid, false, conflict("the certificate is already revoked")
		}
		_, err := tx.ExecContext(ctx, `UPDATE scep_certificates SET revoked_at = now(), revoke_reason = $2 WHERE id = $1`, id, reason)
		return rid, false, err
	})
	if err != nil {
		return SCEPCertificate{}, err
	}
	return scanSCEPCert(s.db.QueryRowContext(ctx, `SELECT `+scepCertCols+` FROM scep_certificates WHERE id = $1`, id))
}

// RotateSCEPChallenge discards the phone's challenge password, used or not;
// its next configuration carries a new one. ErrNotFound for an unknown phone.
func (s *Store) RotateSCEPChallenge(ctx context.Context, actor string, phoneID int64) error {
	return s.provChange(ctx, actor, "rotate", "scep_challenge", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(phoneID, 10)
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phones WHERE id = $1`, phoneID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return rid, false, ErrNotFound
			}
			return rid, false, err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM scep_challenges WHERE phone_id = $1`, phoneID)
		return rid, false, err
	})
}

// scepChallenge returns the phone's current challenge, making one when it
// has none (the first render after SCEP is switched on, or after an
// enrolment used the last one).
func (s *Store) scepChallenge(ctx context.Context, phoneID int64) (string, error) {
	if s.box == nil {
		return "", prov.ErrSealed
	}
	fresh, err := s.box.Seal(prov.NewSCEPChallenge(), prov.SCEPChallengeAAD(phoneID))
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO scep_challenges (phone_id, challenge_enc) VALUES ($1, $2)
		ON CONFLICT (phone_id) DO NOTHING`, phoneID, fresh); err != nil {
		return "", err
	}
	var enc []byte
	if err := s.db.QueryRowContext(ctx, `SELECT challenge_enc FROM scep_challenges WHERE phone_id = $1`, phoneID).Scan(&enc); err != nil {
		return "", err
	}
	c, err := s.box.Open(enc, prov.SCEPChallengeAAD(phoneID))
	if err != nil {
		return "", prov.ErrSealed
	}
	return c, nil
}

// --- 802.1X profiles ---

const dot1xCols = `d.id, d.name, d.method, d.identity, d.anonymous_identity, d.password_enc IS NOT NULL, d.ca_cert_pem, d.server_domain, d.updated_at,
	COALESCE((SELECT json_agg(g.id ORDER BY g.id) FROM phone_groups g WHERE g.wired_dot1x_id = d.id), '[]'),
	COALESCE((SELECT json_agg(w.id ORDER BY w.id) FROM wifi_profiles w WHERE w.dot1x_id = d.id), '[]')`

func scanDot1X(sc interface{ Scan(...any) error }) (Dot1XProfile, error) {
	var (
		p        Dot1XProfile
		used, wf []byte
	)
	if err := sc.Scan(&p.ID, &p.Name, &p.Method, &p.Identity, &p.AnonymousIdentity, &p.HasPassword, &p.CACertPEM, &p.ServerDomain, &p.UpdatedAt, &used, &wf); err != nil {
		return p, err
	}
	p.UsedBy, p.UsedByWifi = []int64{}, []int64{}
	if err := json.Unmarshal(used, &p.UsedBy); err != nil {
		return p, err
	}
	return p, json.Unmarshal(wf, &p.UsedByWifi)
}

// ListDot1XProfiles lists the profiles by name.
func (s *Store) ListDot1XProfiles(ctx context.Context) ([]Dot1XProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+dot1xCols+` FROM dot1x_profiles d ORDER BY lower(d.name), d.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Dot1XProfile{}
	for rows.Next() {
		p, err := scanDot1X(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetDot1XProfile returns one profile; ErrNotFound when there is none.
func (s *Store) GetDot1XProfile(ctx context.Context, id int64) (Dot1XProfile, error) {
	p, err := scanDot1X(s.db.QueryRowContext(ctx, `SELECT `+dot1xCols+` FROM dot1x_profiles d WHERE d.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// CreateDot1XProfile adds a profile.
func (s *Store) CreateDot1XProfile(ctx context.Context, actor string, in Dot1XInput) (Dot1XProfile, error) {
	var id int64
	err := s.provChange(ctx, actor, "create", "dot1x_profile", func(tx *sql.Tx) (string, bool, error) {
		if err := s.checkDot1X(in, false); err != nil {
			return "", false, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT nextval('dot1x_profiles_id_seq')`).Scan(&id); err != nil {
			return "", false, err
		}
		enc, err := s.sealDot1X(id, in.Password)
		if err != nil {
			return "", false, err
		}
		if err := dot1xNameFree(ctx, tx, in.Name, 0); err != nil {
			return "", false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dot1x_profiles (id, name, method, identity, anonymous_identity, password_enc, ca_cert_pem, server_domain)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, strings.TrimSpace(in.Name), in.Method, in.Identity, in.AnonymousIdentity, enc,
			normPEM(in.CACertPEM), in.ServerDomain)
		return strconv.FormatInt(id, 10), false, err
	})
	if err != nil {
		return Dot1XProfile{}, err
	}
	return s.GetDot1XProfile(ctx, id)
}

// UpdateDot1XProfile replaces a profile; a nil password keeps the stored
// one. ErrNotFound for an unknown id.
func (s *Store) UpdateDot1XProfile(ctx context.Context, actor string, id int64, in Dot1XInput) (Dot1XProfile, error) {
	err := s.provChange(ctx, actor, "update", "dot1x_profile", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		var has bool
		if err := tx.QueryRowContext(ctx, `SELECT password_enc IS NOT NULL FROM dot1x_profiles WHERE id = $1 FOR UPDATE`, id).Scan(&has); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return rid, false, ErrNotFound
			}
			return rid, false, err
		}
		if err := s.checkDot1X(in, has); err != nil {
			return rid, false, err
		}
		if in.Method == prov.Dot1XEAPMD5 {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM wifi_profiles WHERE dot1x_id = $1`, id).Scan(&n); err != nil {
				return rid, false, err
			}
			if n > 0 {
				return rid, false, conflict("the profile authenticates %d Wi-Fi profiles, and EAP-MD5 is a wired method", n)
			}
		}
		if err := dot1xNameFree(ctx, tx, in.Name, id); err != nil {
			return rid, false, err
		}
		q := `UPDATE dot1x_profiles SET name = $2, method = $3, identity = $4, anonymous_identity = $5, ca_cert_pem = $6,
			server_domain = $7, updated_at = now()`
		args := []any{id, strings.TrimSpace(in.Name), in.Method, in.Identity, in.AnonymousIdentity, normPEM(in.CACertPEM), in.ServerDomain}
		switch {
		case in.Method == prov.Dot1XEAPTLS:
			q += `, password_enc = NULL`
		case in.Password != nil:
			enc, err := s.sealDot1X(id, in.Password)
			if err != nil {
				return rid, false, err
			}
			q += `, password_enc = $8`
			args = append(args, enc)
		}
		_, err := tx.ExecContext(ctx, q+` WHERE id = $1`, args...)
		return rid, false, err
	})
	if err != nil {
		return Dot1XProfile{}, err
	}
	return s.GetDot1XProfile(ctx, id)
}

// DeleteDot1XProfile removes a profile no phone group or Wi-Fi profile uses. ErrNotFound for
// an unknown id; a conflict naming the groups otherwise.
func (s *Store) DeleteDot1XProfile(ctx context.Context, actor string, id int64) error {
	return s.provChange(ctx, actor, "delete", "dot1x_profile", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM dot1x_profiles WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return rid, false, ErrNotFound
			}
			return rid, false, err
		}
		var names []string
		rows, err := tx.QueryContext(ctx, `SELECT name FROM phone_groups WHERE wired_dot1x_id = $1 ORDER BY lower(name)`, id)
		if err != nil {
			return rid, false, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return rid, false, err
			}
			names = append(names, n)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return rid, false, err
		}
		if len(names) > 0 {
			return rid, false, conflict("the profile is the wired 802.1X profile of phone group %s", strings.Join(names, ", "))
		}
		var wifi []string
		wrows, err := tx.QueryContext(ctx, `SELECT name FROM wifi_profiles WHERE dot1x_id = $1 ORDER BY lower(name)`, id)
		if err != nil {
			return rid, false, err
		}
		for wrows.Next() {
			var n string
			if err := wrows.Scan(&n); err != nil {
				_ = wrows.Close()
				return rid, false, err
			}
			wifi = append(wifi, n)
		}
		_ = wrows.Close()
		if err := wrows.Err(); err != nil {
			return rid, false, err
		}
		if len(wifi) > 0 {
			return rid, false, conflict("the profile authenticates the Wi-Fi profile %s", strings.Join(wifi, ", "))
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM dot1x_profiles WHERE id = $1`, id)
		return rid, false, err
	})
}

func (s *Store) checkDot1X(in Dot1XInput, stored bool) error {
	has := stored
	if in.Password != nil {
		has = *in.Password != ""
	}
	set := in.Password != nil && *in.Password != ""
	if f := prov.ValidateDot1X(in.Name, in.Method, in.Identity, in.AnonymousIdentity, in.CACertPEM, in.ServerDomain, has, set); len(f) > 0 {
		return fieldErrs(f)
	}
	if in.Password != nil && len(*in.Password) > 128 {
		return fieldError("password", "must be at most 128 characters")
	}
	return nil
}

func (s *Store) sealDot1X(id int64, pw *string) ([]byte, error) {
	if pw == nil || *pw == "" {
		return nil, nil
	}
	if s.box == nil {
		return nil, prov.ErrSealed
	}
	return s.box.Seal(*pw, prov.Dot1XPasswordAAD(id))
}

func dot1xNameFree(ctx context.Context, tx *sql.Tx, name string, self int64) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM dot1x_profiles WHERE lower(name) = lower($1) AND id <> $2`, strings.TrimSpace(name), self).Scan(&one)
	switch {
	case err == nil:
		return conflict("an 802.1X profile named %q exists", strings.TrimSpace(name))
	case errors.Is(err, sql.ErrNoRows):
		return nil
	}
	return err
}

func normPEM(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n"
}

// --- phone group security ---

// GetGroupSecurity returns a phone group's SCEP switch and wired profile;
// ErrNotFound for an unknown group.
func (s *Store) GetGroupSecurity(ctx context.Context, groupID int64) (GroupSecurity, error) {
	g := GroupSecurity{GroupID: groupID}
	var wired sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT scep_enabled, scep_validity_days, wired_dot1x_id FROM phone_groups WHERE id = $1`, groupID).
		Scan(&g.SCEPEnabled, &g.CertValidityDays, &wired)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if wired.Valid {
		g.WiredDot1XID = &wired.Int64
	}
	return g, err
}

// SetGroupSecurity replaces a phone group's SCEP switch, certificate
// lifetime and wired profile. ErrNotFound for an unknown group; a field
// error for an unknown profile or a lifetime outside 30 to 1825 days.
func (s *Store) SetGroupSecurity(ctx context.Context, actor string, in GroupSecurity) (GroupSecurity, error) {
	err := s.provChange(ctx, actor, "update", "phone_group_security", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(in.GroupID, 10)
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = $1 FOR UPDATE`, in.GroupID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return rid, false, ErrNotFound
			}
			return rid, false, err
		}
		if in.CertValidityDays < 30 || in.CertValidityDays > 1825 {
			return rid, false, fieldError("certValidityDays", "must be 30 to 1825")
		}
		if in.WiredDot1XID != nil {
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM dot1x_profiles WHERE id = $1`, *in.WiredDot1XID).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return rid, false, fieldError("wiredDot1xProfileId", "no such 802.1X profile")
				}
				return rid, false, err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE phone_groups SET scep_enabled = $2, scep_validity_days = $3, wired_dot1x_id = $4, updated_at = now() WHERE id = $1`,
			in.GroupID, in.SCEPEnabled, in.CertValidityDays, in.WiredDot1XID)
		return rid, false, err
	})
	if err != nil {
		return GroupSecurity{}, err
	}
	return s.GetGroupSecurity(ctx, in.GroupID)
}

// --- render data ---

// securityInfo is RenderData.SCEP and RenderData.Dot1X for a phone. SCEP is
// set when HELLO_SCEP is on, the phone's group has SCEP on, a CA exists and
// the model has SCEP keys; Dot1X when any profile exists.
func (s *Store) securityInfo(ctx context.Context, phoneID int64, v prov.Vendor, model, mac, token string) (*prov.SCEPInfo, *prov.Dot1XInfo, error) {
	var (
		on    sql.NullBool
		wired sql.NullInt64
	)
	if err := s.db.QueryRowContext(ctx, `SELECT g.scep_enabled, g.wired_dot1x_id FROM phones p
		LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&on, &wired); err != nil {
		return nil, nil, err
	}
	var info *prov.SCEPInfo
	if s.prov.SCEP && on.Bool && prov.SupportsSCEP(v, model) && s.prov.PublicURL != "" {
		var certPEM string
		err := s.db.QueryRowContext(ctx, `SELECT cert_pem FROM scep_ca WHERE active`).Scan(&certPEM)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, nil, err
		default:
			cert, err := parseCertPEM(certPEM)
			if err != nil {
				return nil, nil, err
			}
			ch, err := s.scepChallenge(ctx, phoneID)
			if err != nil {
				return nil, nil, err
			}
			info = &prov.SCEPInfo{URL: prov.SCEPURL(s.prov.phoneBase(token)), Challenge: ch,
				CAFingerprint: prov.CertFingerprint(cert.Raw), KeyBits: 2048}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, method, identity, anonymous_identity, password_enc, ca_cert_pem, server_domain
		FROM dot1x_profiles ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var d *prov.Dot1XInfo
	for rows.Next() {
		var (
			id                               int64
			name, method, identity, anon, ca string
			domain                           string
			enc                              []byte
		)
		if err := rows.Scan(&id, &name, &method, &identity, &anon, &enc, &ca, &domain); err != nil {
			return nil, nil, err
		}
		pw := ""
		if enc != nil {
			if s.box == nil {
				return nil, nil, prov.ErrSealed
			}
			if pw, err = s.box.Open(enc, prov.Dot1XPasswordAAD(id)); err != nil {
				return nil, nil, prov.ErrSealed
			}
		}
		if d == nil {
			d = &prov.Dot1XInfo{}
		}
		p := prov.NewDot1XProfile(id, name, method, identity, anon, pw, ca, domain, mac, info != nil)
		d.Profiles = append(d.Profiles, p)
		if wired.Valid && wired.Int64 == id && prov.SupportsWired8021X(v, model) {
			w := p
			d.Wired = &w
		}
	}
	return info, d, rows.Err()
}
