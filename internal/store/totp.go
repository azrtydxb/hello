package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/secret"
)

// Limits of the second-factor step (spec pbx-gaps S-11).
const (
	// MFAChallengeTTL is how long a password-verified login waits for its code.
	MFAChallengeTTL = 5 * time.Minute
	// mfaAttemptsPerChallenge wrong codes end the challenge.
	mfaAttemptsPerChallenge = 5
	// mfaFailuresPerUser wrong codes per mfaFailureWindow refuse every
	// challenge of the user.
	mfaFailuresPerUser = 10
	mfaFailureWindow   = 10 * time.Minute
)

var (
	// ErrMFAInvalid is every refusal of the second factor: an unknown, expired
	// or spent challenge, a wrong or replayed code, an exhausted limit. One
	// error so a caller cannot tell them apart.
	ErrMFAInvalid = errors.New("store: invalid second factor")
	// ErrTwoFactorRequired refuses a policy change by a setter without active
	// 2FA (spec pbx-gaps S-12).
	ErrTwoFactorRequired = errors.New("store: the setter has no active 2FA")
	// ErrLastEnrolledAdmin refuses the reset of the only admin with 2FA.
	ErrLastEnrolledAdmin = errors.New("store: last enrolled admin")
	// ErrTOTPEnrolled refuses a new setup while 2FA is active.
	ErrTOTPEnrolled = errors.New("store: 2FA is already active")
	// ErrNoTOTPSetup is returned when there is no pending secret to enable.
	ErrNoTOTPSetup = errors.New("store: no 2FA setup in progress")
	// ErrNoSecretBox is returned when HELLO_SECRET_KEY is not configured.
	ErrNoSecretBox = errors.New("store: no secret box configured for 2FA")
)

func totpAAD(userID int64) string { return secret.TOTPAAD(userID) }

// TwoFactorStatus is a user's own 2FA state.
type TwoFactorStatus struct {
	Enabled bool `json:"enabled"`
	// Pending is true while a secret has been issued but not confirmed.
	Pending bool `json:"pending"`
	// Required is whether the user's account or role demands 2FA.
	Required     bool `json:"required"`
	RecoveryLeft int  `json:"recoveryCodesLeft"`
}

// TwoFactorStatus reads the 2FA state of user id.
func (s *Store) TwoFactorStatus(ctx context.Context, id int64) (TwoFactorStatus, error) {
	var (
		st      TwoFactorStatus
		enabled sql.NullTime
		hasSec  bool
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.totp_enabled_at, u.totp_secret_enc IS NOT NULL,
			u.totp_required OR u.role = ANY(a.require_2fa_roles),
			(SELECT count(*) FROM user_recovery_codes WHERE user_id = u.id)
		FROM users u CROSS JOIN auth_settings a WHERE u.id = $1`, id).
		Scan(&enabled, &hasSec, &st.Required, &st.RecoveryLeft)
	if err != nil {
		return st, mapErr(err)
	}
	st.Enabled = enabled.Valid
	st.Pending = hasSec && !enabled.Valid
	if !st.Enabled {
		st.RecoveryLeft = 0
	}
	return st, nil
}

// BeginTOTPSetup stores a new sealed, inactive secret for user id, replacing
// an earlier unconfirmed one. It is refused while 2FA is active.
func (s *Store) BeginTOTPSetup(ctx context.Context, id int64, secretB32 string) error {
	if s.box == nil {
		return ErrNoSecretBox
	}
	sealed, err := s.box.Seal(secretB32, totpAAD(id))
	if err != nil {
		return fmt.Errorf("store: seal totp secret: %w", err)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var enabled sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT totp_enabled_at FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&enabled); err != nil {
			return mapErr(err)
		}
		if enabled.Valid {
			return ErrTOTPEnrolled
		}
		_, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret_enc = $2, totp_last_step = 0 WHERE id = $1`, id, sealed)
		return err
	})
}

// openTOTP reads and opens user id's secret under the row lock of tx.
func (s *Store) openTOTP(ctx context.Context, tx *sql.Tx, id int64) (secretB32 string, enabled bool, last int64, err error) {
	var (
		enc []byte
		at  sql.NullTime
	)
	if err = tx.QueryRowContext(ctx,
		`SELECT totp_secret_enc, totp_enabled_at, totp_last_step FROM users WHERE id = $1 FOR UPDATE`, id).
		Scan(&enc, &at, &last); err != nil {
		return "", false, 0, mapErr(err)
	}
	if enc == nil {
		return "", false, 0, ErrNoTOTPSetup
	}
	if s.box == nil {
		return "", false, 0, ErrNoSecretBox
	}
	if secretB32, err = s.box.Open(enc, totpAAD(id)); err != nil {
		return "", false, 0, err
	}
	return secretB32, at.Valid, last, nil
}

// EnableTOTP confirms the pending secret with a valid code, activates it and
// returns the ten recovery codes, once. A wrong code is ErrMFAInvalid.
func (s *Store) EnableTOTP(ctx context.Context, id int64, code string, now time.Time) ([]string, error) {
	var plain []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		sec, enabled, last, err := s.openTOTP(ctx, tx, id)
		if err != nil {
			return err
		}
		if enabled {
			return ErrTOTPEnrolled
		}
		step, ok := auth.VerifyTOTP(sec, code, now, last)
		if !ok {
			return ErrMFAInvalid
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET totp_enabled_at = now(), totp_last_step = $2 WHERE id = $1`, id, step); err != nil {
			return err
		}
		if plain, err = replaceRecoveryCodes(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, "user:"+strconv.FormatInt(id, 10), "enable-2fa", "user", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return nil, err
	}
	return plain, nil
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, id int64) ([]string, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id = $1`, id); err != nil {
		return nil, err
	}
	plain, hashes := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2)`, id, h); err != nil {
			return nil, err
		}
	}
	return plain, nil
}

// checkSecondFactor verifies code (a TOTP code or a recovery code) for user
// id inside tx, which must hold the user's row lock. A TOTP code advances
// totp_last_step; a recovery code is deleted. It reports whether it passed
// and which kind it was.
func (s *Store) checkSecondFactor(ctx context.Context, tx *sql.Tx, id int64, code string, now time.Time) (ok, recovery bool, err error) {
	if auth.LooksLikeRecoveryCode(code) {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM user_recovery_codes WHERE user_id = $1 AND code_hash = $2`, id, auth.HashRecoveryCode(code))
		if err != nil {
			return false, true, err
		}
		n, err := res.RowsAffected()
		return n == 1, true, err
	}
	sec, enabled, last, err := s.openTOTP(ctx, tx, id)
	if errors.Is(err, ErrNoTOTPSetup) || (err == nil && !enabled) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	step, ok := auth.VerifyTOTP(sec, code, now, last)
	if !ok {
		return false, false, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE users SET totp_last_step = $2 WHERE id = $1`, id, step)
	return err == nil, false, err
}

// RegenerateRecoveryCodes replaces the user's recovery codes after a valid
// second factor and returns the new ones, once.
func (s *Store) RegenerateRecoveryCodes(ctx context.Context, id int64, code string, now time.Time) ([]string, error) {
	return s.withSecondFactor(ctx, id, code, now, "regenerate-2fa-recovery", func(tx *sql.Tx) ([]string, error) {
		return replaceRecoveryCodes(ctx, tx, id)
	})
}

// DisableTOTP turns the user's own 2FA off after a valid second factor. The
// caller has checked the policy and the password.
func (s *Store) DisableTOTP(ctx context.Context, id int64, code string, now time.Time) error {
	_, err := s.withSecondFactor(ctx, id, code, now, "disable-2fa", func(tx *sql.Tx) ([]string, error) {
		return nil, clearTOTP(ctx, tx, id)
	})
	return err
}

func (s *Store) withSecondFactor(ctx context.Context, id int64, code string, now time.Time, action string,
	fn func(*sql.Tx) ([]string, error)) ([]string, error) {
	var out []string
	var refused bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ok, _, err := s.checkSecondFactor(ctx, tx, id, code, now)
		if err != nil {
			return err
		}
		if !ok {
			refused = true
			return recordMFAFailure(ctx, tx, id)
		}
		if out, err = fn(tx); err != nil {
			return err
		}
		return insertAudit(ctx, tx, "user:"+strconv.FormatInt(id, 10), action, "user", strconv.FormatInt(id, 10))
	})
	if err != nil {
		return nil, err
	}
	if refused {
		return nil, ErrMFAInvalid
	}
	return out, nil
}

func clearTOTP(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET totp_secret_enc = NULL, totp_enabled_at = NULL, totp_last_step = 0 WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id = $1`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM auth_mfa_challenges WHERE user_id = $1`, id)
	return err
}

func recordMFAFailure(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO auth_mfa_failures (user_id) VALUES ($1)`, id)
	return err
}

// CreateMFAChallenge stores the hash of a login's challenge token.
func (s *Store) CreateMFAChallenge(ctx context.Context, userID int64, hash []byte, expires time.Time) error {
	// Expired rows of any user go with the next insert.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_mfa_challenges WHERE expires_at <= now()`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_mfa_challenges (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hash, userID, expires)
	return err
}

// MFAResult is a passed second factor.
type MFAResult struct {
	UserID   int64
	Username string
	// Recovery is true when a recovery code, not a TOTP code, was used.
	Recovery     bool
	RecoveryLeft int
}

// CompleteMFAChallenge verifies code for the challenge named by token hash
// and consumes the challenge on success. Every refusal is ErrMFAInvalid. A
// wrong code counts against the challenge (5) and the user (10 per 10
// minutes); the challenge is spent when either limit is reached.
func (s *Store) CompleteMFAChallenge(ctx context.Context, hash []byte, code string, now time.Time) (MFAResult, error) {
	var (
		res     MFAResult
		invalid bool
	)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var (
			uid      int64
			expires  time.Time
			attempts int
		)
		err := tx.QueryRowContext(ctx, `
			SELECT c.user_id, c.expires_at, c.attempts FROM auth_mfa_challenges c
			JOIN users u ON u.id = c.user_id AND u.enabled
			WHERE c.token_hash = $1 FOR UPDATE OF c`, hash).Scan(&uid, &expires, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			invalid = true
			return nil
		}
		if err != nil {
			return err
		}
		// Serialise every attempt for this user, so one TOTP step cannot be
		// accepted twice by two challenges at once.
		if err := tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id = $1 FOR UPDATE`, uid).Scan(&res.Username); err != nil {
			return err
		}
		spend := func() error {
			_, err := tx.ExecContext(ctx, `DELETE FROM auth_mfa_challenges WHERE token_hash = $1`, hash)
			return err
		}
		if !expires.After(now) {
			invalid = true
			return spend()
		}
		var failures int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM auth_mfa_failures WHERE user_id = $1 AND at > $2`, uid, now.Add(-mfaFailureWindow)).
			Scan(&failures); err != nil {
			return err
		}
		if failures >= mfaFailuresPerUser {
			invalid = true
			return spend()
		}
		ok, recovery, err := s.checkSecondFactor(ctx, tx, uid, code, now)
		if err != nil {
			return err
		}
		if !ok {
			invalid = true
			if err := recordMFAFailure(ctx, tx, uid); err != nil {
				return err
			}
			if attempts+1 >= mfaAttemptsPerChallenge {
				return spend()
			}
			_, err := tx.ExecContext(ctx, `UPDATE auth_mfa_challenges SET attempts = attempts + 1 WHERE token_hash = $1`, hash)
			return err
		}
		if err := spend(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_mfa_failures WHERE user_id = $1`, uid); err != nil {
			return err
		}
		res.UserID, res.Recovery = uid, recovery
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_recovery_codes WHERE user_id = $1`, uid).
			Scan(&res.RecoveryLeft); err != nil {
			return err
		}
		if recovery {
			return insertAudit(ctx, tx, "user:"+res.Username, "use-2fa-recovery", "user", strconv.FormatInt(uid, 10))
		}
		return nil
	})
	if err != nil {
		return MFAResult{}, err
	}
	if invalid {
		return MFAResult{}, ErrMFAInvalid
	}
	return res, nil
}

// PruneMFA deletes expired challenges and old failure records.
func (s *Store) PruneMFA(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_mfa_challenges WHERE expires_at <= now()`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_mfa_failures WHERE at < now() - interval '1 hour'`)
	return err
}

// ResetTOTP clears user id's 2FA (spec S-12). With guard set (the API), the
// reset of the only enabled admin who has 2FA is refused; the CLI passes
// guard false as the break-glass form. It ends the user's sessions.
func (s *Store) ResetTOTP(ctx context.Context, actor string, id int64, guard bool) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := lockAdmins(ctx, tx); err != nil {
			return err
		}
		var (
			role    auth.Role
			enabled bool
			at      sql.NullTime
		)
		if err := tx.QueryRowContext(ctx, `SELECT role, enabled, totp_enabled_at FROM users WHERE id = $1 FOR UPDATE`, id).
			Scan(&role, &enabled, &at); err != nil {
			return mapErr(err)
		}
		if guard && at.Valid && role == auth.RoleAdmin && enabled {
			var others int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FROM users WHERE role = 'admin' AND enabled AND totp_enabled_at IS NOT NULL AND id <> $1`, id).
				Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastEnrolledAdmin
			}
		}
		if err := clearTOTP(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "reset-2fa", "user", strconv.FormatInt(id, 10))
	})
}

// ResetTOTPByName is the CLI's break-glass reset by login name.
func (s *Store) ResetTOTPByName(ctx context.Context, actor, username string) error {
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, username).Scan(&id); err != nil {
		return mapErr(err)
	}
	return s.ResetTOTP(ctx, actor, id, false)
}

// AuthSettings is the global 2FA policy.
type AuthSettings struct {
	Require2FARoles []string `json:"require2faRoles"`
}

// GetAuthSettings reads the policy.
func (s *Store) GetAuthSettings(ctx context.Context) (AuthSettings, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT to_json(require_2fa_roles)::text FROM auth_settings`).Scan(&raw); err != nil {
		return AuthSettings{}, err
	}
	var out AuthSettings
	if err := json.Unmarshal([]byte(raw), &out.Require2FARoles); err != nil {
		return AuthSettings{}, err
	}
	if out.Require2FARoles == nil {
		out.Require2FARoles = []string{}
	}
	return out, nil
}

// SetRequire2FARoles replaces the roles that must use 2FA. Naming admin is
// refused (ErrTwoFactorRequired) unless setter has active 2FA, so a policy
// change cannot lock every admin out.
func (s *Store) SetRequire2FARoles(ctx context.Context, actor string, setter int64, roles []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, r := range roles {
			if r == string(auth.RoleAdmin) {
				if err := requireActive2FA(ctx, tx, setter); err != nil {
					return err
				}
			}
		}
		if roles == nil {
			roles = []string{}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_settings SET require_2fa_roles = $1::text[]`, roles); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "auth_settings", "2fa")
	})
}

// requireActive2FA is ErrTwoFactorRequired unless user id has active 2FA.
func requireActive2FA(ctx context.Context, tx *sql.Tx, id int64) error {
	var on bool
	err := tx.QueryRowContext(ctx, `SELECT totp_enabled_at IS NOT NULL FROM users WHERE id = $1`, id).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !on) {
		return ErrTwoFactorRequired
	}
	return err
}
