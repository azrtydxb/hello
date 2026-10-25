package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// ErrLastAdmin rejects a change that would leave no enabled admin.
var ErrLastAdmin = errors.New("store: last admin")

// VoicemailEmailPrefs are a user's voicemail-to-email options.
type VoicemailEmailPrefs struct {
	// Notify turns the email on or off for this user.
	Notify bool `json:"notify"`
	// Attach adds the recording to the email.
	Attach bool `json:"attach"`
	// After is what happens to the message once the email is sent: "none",
	// "mark_read" or "delete".
	After string `json:"after"`
}

// User is a management or self-service user without its password hash.
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	Email       string    `json:"email"`
	AuthMethod  string    `json:"authMethod"`
	Role        auth.Role `json:"role"`
	Enabled     bool      `json:"enabled"`
	// MustChangePassword is set while the password was chosen by an admin.
	MustChangePassword bool `json:"mustChangePassword"`
	// ExtensionID is the primary extension (spec P-3), whose phones get the
	// user's personal book; nil when the user has none.
	ExtensionID *int64 `json:"extensionId"`
	// ExtensionIDs are all the extensions the user manages.
	ExtensionIDs   []int64             `json:"extensionIds"`
	VoicemailEmail VoicemailEmailPrefs `json:"voicemailEmail"`
	CreatedAt      time.Time           `json:"createdAt"`
}

const userCols = `id, username, display_name, email, auth_method, role, enabled, must_change_password, extension_id,
	coalesce((SELECT json_agg(extension_id ORDER BY position, extension_id) FROM user_extensions WHERE user_id = users.id), '[]'::json)::text,
	vm_email_notify, vm_email_attach, vm_email_after, created_at`

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var (
		u    User
		exts string
	)
	err := r.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AuthMethod, &u.Role, &u.Enabled, &u.MustChangePassword,
		&u.ExtensionID, &exts, &u.VoicemailEmail.Notify, &u.VoicemailEmail.Attach, &u.VoicemailEmail.After, &u.CreatedAt)
	if err != nil {
		return u, err
	}
	if err := json.Unmarshal([]byte(exts), &u.ExtensionIDs); err != nil {
		return u, err
	}
	if u.ExtensionIDs == nil {
		u.ExtensionIDs = []int64{}
	}
	return u, nil
}

// CreateUser inserts a user with role, except that the first user is always
// admin (spec S-23). It serves the CLI and bootstrap; the admin API uses
// CreateAccount.
func (s *Store) CreateUser(ctx context.Context, actor, username, passwordHash string, role auth.Role) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO users (username, password_hash, role)
			VALUES ($1, $2, CASE WHEN EXISTS (SELECT 1 FROM users) THEN $3 ELSE 'admin' END)
			RETURNING id`,
			username, passwordHash, string(role)).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "user", strconv.FormatInt(id, 10))
	})
	return id, err
}

// NewAccount is a user an admin creates.
type NewAccount struct {
	Username     string
	DisplayName  string
	Email        string
	Role         auth.Role
	PasswordHash string
	// MustChange makes the user choose a new password at first login.
	MustChange   bool
	ExtensionIDs []int64
	Voicemail    VoicemailEmailPrefs
}

// CreateAccount inserts a user and links its extensions, auditing the event
// (never the password or its hash).
func (s *Store) CreateAccount(ctx context.Context, actor string, in NewAccount) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO users (username, password_hash, role, display_name, email, must_change_password,
				vm_email_notify, vm_email_attach, vm_email_after)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			in.Username, in.PasswordHash, string(in.Role), in.DisplayName, in.Email, in.MustChange,
			in.Voicemail.Notify, in.Voicemail.Attach, in.Voicemail.After).Scan(&id); err != nil {
			return err
		}
		if err := setUserExtensions(ctx, tx, id, in.ExtensionIDs); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, actor, "create", "user", strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		var err error
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
		return err
	})
	return u, err
}

// GetUser returns one user.
func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
	return u, mapErr(err)
}

// ListUsers returns every user, by id.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AccountChange is an admin's update of a user; nil keeps a field.
type AccountChange struct {
	DisplayName  *string
	Email        *string
	Role         *auth.Role
	Enabled      *bool
	ExtensionIDs *[]int64
	// PasswordHash resets the password; MustChange then says whether the
	// user has to choose a new one at next login.
	PasswordHash *string
	MustChange   bool
	Voicemail    *VoicemailEmailPrefs
}

// UpdateAccount changes a user. Demoting or disabling the last enabled admin
// returns ErrLastAdmin; concurrent changes serialise on the admin rows.
// Disabling a user, and resetting a password, end their sessions.
func (s *Store) UpdateAccount(ctx context.Context, actor string, id int64, c AccountChange) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		admins, err := lockAdmins(ctx, tx)
		if err != nil {
			return err
		}
		cur, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return mapErr(err)
		}
		role, enabled := cur.Role, cur.Enabled
		if c.Role != nil {
			role = *c.Role
		}
		if c.Enabled != nil {
			enabled = *c.Enabled
		}
		wasAdmin := cur.Role == auth.RoleAdmin && cur.Enabled
		if wasAdmin && (role != auth.RoleAdmin || !enabled) && admins <= 1 {
			return ErrLastAdmin
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET
				display_name = COALESCE($2, display_name),
				email = COALESCE($3, email),
				role = $4,
				enabled = $5,
				password_hash = COALESCE($6, password_hash),
				must_change_password = CASE WHEN $6::text IS NULL THEN must_change_password ELSE $7 END,
				vm_email_notify = COALESCE($8, vm_email_notify),
				vm_email_attach = COALESCE($9, vm_email_attach),
				vm_email_after = COALESCE($10, vm_email_after)
			WHERE id = $1`,
			id, c.DisplayName, c.Email, string(role), enabled, c.PasswordHash, c.MustChange,
			prefNotify(c.Voicemail), prefAttach(c.Voicemail), prefAfter(c.Voicemail)); err != nil {
			return err
		}
		if c.ExtensionIDs != nil {
			if err := setUserExtensions(ctx, tx, id, *c.ExtensionIDs); err != nil {
				return err
			}
		}
		if !enabled || c.PasswordHash != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
				return err
			}
		}
		// The audit action names what changed, never a value.
		action := "update"
		switch {
		case c.PasswordHash != nil:
			action = "reset-password"
		case c.Enabled != nil && cur.Enabled != enabled && !enabled:
			action = "disable"
		case c.Enabled != nil && cur.Enabled != enabled:
			action = "enable"
		case c.Role != nil && cur.Role != role:
			action = "set-role:" + string(role)
		}
		if err := insertAudit(ctx, tx, actor, action, "user", strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
		return err
	})
	return u, err
}

func prefNotify(p *VoicemailEmailPrefs) *bool {
	if p == nil {
		return nil
	}
	return &p.Notify
}

func prefAttach(p *VoicemailEmailPrefs) *bool {
	if p == nil {
		return nil
	}
	return &p.Attach
}

func prefAfter(p *VoicemailEmailPrefs) *string {
	if p == nil {
		return nil
	}
	return &p.After
}

// DeleteUser removes a user with its sessions, tokens and personal book.
// Deleting the last enabled admin returns ErrLastAdmin.
func (s *Store) DeleteUser(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		admins, err := lockAdmins(ctx, tx)
		if err != nil {
			return err
		}
		var role auth.Role
		var enabled bool
		if err := tx.QueryRowContext(ctx, `SELECT role, enabled FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&role, &enabled); err != nil {
			return mapErr(err)
		}
		if role == auth.RoleAdmin && enabled && admins <= 1 {
			return ErrLastAdmin
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "user", strconv.FormatInt(id, 10))
	})
}

// SetUserRole changes user id's role and records the audit event. Demoting
// the last admin returns ErrLastAdmin.
func (s *Store) SetUserRole(ctx context.Context, actor string, id int64, role auth.Role) (User, error) {
	return s.UpdateAccount(ctx, actor, id, AccountChange{Role: &role})
}

// SetUserExtension links user id to one extension (replacing any others), or
// unlinks it with nil. An extension is linked to at most one user; one
// already taken is a conflict naming the user.
func (s *Store) SetUserExtension(ctx context.Context, actor string, id int64, extension *int64) (User, error) {
	ids := []int64{}
	if extension != nil {
		ids = append(ids, *extension)
	}
	return s.UpdateAccount(ctx, actor, id, AccountChange{ExtensionIDs: &ids})
}

// lockAdmins locks every enabled admin row and counts them.
func lockAdmins(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE role = 'admin' AND enabled FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// setUserExtensions replaces the extensions of user id with ids, in order;
// the first is the primary one. An unknown extension is a field error and
// one linked to another user a conflict naming that user.
func setUserExtensions(ctx context.Context, tx *sql.Tx, id int64, ids []int64) error {
	seen := map[int64]bool{}
	var clean []int64
	for _, e := range ids {
		if !seen[e] {
			seen[e] = true
			clean = append(clean, e)
		}
	}
	for _, e := range clean {
		var number string
		switch err := tx.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1 FOR SHARE`, e).Scan(&number); {
		case errors.Is(err, sql.ErrNoRows):
			return fieldError("extensionIds", "no such extension")
		case err != nil:
			return err
		}
		var other string
		switch err := tx.QueryRowContext(ctx,
			`SELECT u.username FROM user_extensions ue JOIN users u ON u.id = ue.user_id WHERE ue.extension_id = $1 AND ue.user_id <> $2`,
			e, id).Scan(&other); {
		case err == nil:
			return conflict("extension %s is already linked to user %q", number, other)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_extensions WHERE user_id = $1`, id); err != nil {
		return err
	}
	for i, e := range clean {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_extensions (user_id, extension_id, position) VALUES ($1, $2, $3)`, id, e, i); err != nil {
			return err
		}
	}
	var primary *int64
	if len(clean) > 0 {
		primary = &clean[0]
	}
	_, err := tx.ExecContext(ctx, `UPDATE users SET extension_id = $2 WHERE id = $1`, id, primary)
	return err
}

// Login is what a login needs of a user.
type Login struct {
	ID      int64
	Hash    string
	Enabled bool
	Role    auth.Role
}

// UserForLogin returns the user named username with its password hash; the
// hash never leaves the login path.
func (s *Store) UserForLogin(ctx context.Context, username string) (Login, error) {
	var l Login
	err := s.db.QueryRowContext(ctx,
		`SELECT id, password_hash, enabled, role FROM users WHERE username = $1 AND auth_method = 'local'`, username).
		Scan(&l.ID, &l.Hash, &l.Enabled, &l.Role)
	return l, mapErr(err)
}

// UserPasswordHash returns a user's password hash for re-verification at a
// password change.
func (s *Store) UserPasswordHash(ctx context.Context, id int64) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, id).Scan(&h)
	return h, mapErr(err)
}

// UpgradePasswordHash replaces a verified legacy hash with a stronger one;
// it does nothing if the password changed meanwhile.
func (s *Store) UpgradePasswordHash(ctx context.Context, id int64, oldHash, newHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = $3 WHERE id = $1 AND password_hash = $2`, id, oldHash, newHash)
	return err
}

// ChangeOwnPassword sets a user's own new password, clears the forced-change
// flag and ends every session but keep (the one making the change).
func (s *Store) ChangeOwnPassword(ctx context.Context, id int64, newHash string, keep []byte) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = $2, must_change_password = FALSE WHERE id = $1`, id, newHash); err != nil {
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, id, keep); err != nil {
			return err
		}
		return insertAudit(ctx, tx, "user:"+strconv.FormatInt(id, 10), "change-password", "user", strconv.FormatInt(id, 10))
	})
}

// ProfileChange is a user's edit of their own profile; nil keeps a field.
type ProfileChange struct {
	DisplayName *string
	Email       *string
	Voicemail   *VoicemailEmailPrefs
}

// UpdateProfile changes the caller's own profile.
func (s *Store) UpdateProfile(ctx context.Context, actor string, id int64, c ProfileChange) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE users SET
				display_name = COALESCE($2, display_name),
				email = COALESCE($3, email),
				vm_email_notify = COALESCE($4, vm_email_notify),
				vm_email_attach = COALESCE($5, vm_email_attach),
				vm_email_after = COALESCE($6, vm_email_after)
			WHERE id = $1`, id, c.DisplayName, c.Email, prefNotify(c.Voicemail), prefAttach(c.Voicemail), prefAfter(c.Voicemail))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, actor, "update-profile", "user", strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
		return err
	})
	return u, err
}

// Self-service reads. Each takes the caller's user id and reaches a row only
// through user_extensions, so a guessed id of another user's extension or
// message finds nothing (ErrNotFound), never a row it must then refuse.

// UserExtensions returns the extensions user id manages, primary first.
func (s *Store) UserExtensions(ctx context.Context, userID int64) ([]Extension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("e.", extensionCols)+`
		FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id
		WHERE ue.user_id = $1 ORDER BY ue.position, e.number`, userID)
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

// UserExtension returns extension id if user userID manages it, else
// ErrNotFound.
func (s *Store) UserExtension(ctx context.Context, userID, id int64) (Extension, error) {
	e, err := scanExtension(s.db.QueryRowContext(ctx, `SELECT `+prefixed("e.", extensionCols)+`
		FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id
		WHERE ue.user_id = $1 AND e.id = $2`, userID, id))
	return e, mapErr(err)
}

// UserVoicemailMessage returns message id if it is in a box of an extension
// user userID manages, else ErrNotFound.
func (s *Store) UserVoicemailMessage(ctx context.Context, userID, id int64) (VoicemailMessage, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, `SELECT `+prefixed("m.", messageCols)+`
		FROM voicemail_messages m
		JOIN voicemail_boxes b ON b.id = m.box_id
		JOIN user_extensions ue ON ue.extension_id = b.extension_id
		WHERE ue.user_id = $1 AND m.id = $2`, userID, id))
	return m, mapErr(err)
}

// UserCDRs returns the call records in which one of user userID's extension
// numbers is the source or the destination, newest first, with the cursor
// for the next page.
func (s *Store) UserCDRs(ctx context.Context, userID, before int64, limit int) ([]CDR, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("c.", cdrCols)+`
		FROM cdrs c
		WHERE ($2::bigint = 0 OR c.id < $2::bigint) AND EXISTS (
			SELECT 1 FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id
			WHERE ue.user_id = $1 AND (c.source = e.number OR c.destination = e.number OR c.original_destination = e.number))
		ORDER BY c.id DESC LIMIT $3`, userID, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []CDR{}
	for rows.Next() {
		c, err := scanCDR(rows)
		if err != nil {
			return nil, "", err
		}
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

// prefixed qualifies each column of a comma-separated column list with
// prefix (plain column names only).
func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
