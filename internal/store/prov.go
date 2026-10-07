package store

// Phone auto-provisioning (spec phone-auto-provisioning-service, plan Task
// 3): the phone inventory, its device binding with the sealed secret, the
// provisioning tokens and the admin password. Every change runs in one
// transaction with its audit row; only a change of device binding bumps
// the configuration revision, because only it changes HA1 values hello-sip
// reads (contract 8).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/big"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
)

// ProvSettings is what the store needs to render configs, hand out URLs and
// rotate tokens (config.Prov, with the SIP domain).
type ProvSettings struct {
	// PublicURL is the https:// scheme-and-host phones reach the listener
	// at, without a trailing slash; empty while provisioning is off (no URL
	// is then handed out).
	PublicURL string
	// SIPServer is the host:port rendered configs register with.
	SIPServer string
	// Domain is HELLO_SIP_DOMAIN.
	Domain     string
	Expiry     time.Duration
	Resync     time.Duration
	Timezone   string
	NTP        string
	TokenGrace time.Duration
	// CACertFile is HELLO_PROV_CA_CERT, read per render into
	// ProvInfo.CACertPEM.
	CACertFile string
	// Deployment lists the vendors whose redirect credentials the
	// deployment sets (they count as configured whatever is stored).
	Deployment map[prov.Vendor]bool
}

// WithProv sets the provisioning settings and returns s.
func (s *Store) WithProv(p ProvSettings) *Store {
	p.PublicURL = strings.TrimSuffix(p.PublicURL, "/")
	s.prov = p
	return s
}

// PhoneURL is a phone's own provisioning URL as handed out (create, rotate,
// re-arm and the redirect services): https://…/p/<token>/, followed for
// Snom by the {mac} placeholder the phone expands to its MAC (spec S-7).
// Empty while provisioning is off.
func (p ProvSettings) PhoneURL(token string, v prov.Vendor) string {
	if p.PublicURL == "" {
		return ""
	}
	u := p.PublicURL + "/p/" + token + "/"
	if v == prov.Snom {
		u += "{mac}"
	}
	return u
}

// plainBase is the plain-HTTP form of the public URL for the boot path and
// the CA download: the same host on the default port.
func (p ProvSettings) plainBase() string {
	u, err := url.Parse(p.PublicURL)
	if err != nil || p.PublicURL == "" {
		return ""
	}
	return "http://" + u.Hostname()
}

// info is the ProvInfo of a phone whose current token is token.
func (p ProvSettings) info(token, mac string) prov.ProvInfo {
	return prov.ProvInfo{URL: p.phoneBase(token), CAURL: p.CAURL(), ResyncSeconds: p.resyncSeconds(mac),
		CACertPEM: prov.CACertPEM(p.CACertFile)}
}

// CAURL is the plain-HTTP URL of Hello's provisioning CA.
func (p ProvSettings) CAURL() string {
	if b := p.plainBase(); b != "" {
		return b + "/p/ca.crt"
	}
	return ""
}

// BootURL is the DHCP option 66 value (spec S-10).
func (p ProvSettings) BootURL() string {
	if b := p.plainBase(); b != "" {
		return b + "/p/boot/"
	}
	return ""
}

// resyncSeconds is the re-check interval minus up to a tenth of it derived
// from the MAC, so a site's phones do not all re-check at once and each
// phone keeps a stable value (deterministic rendering).
func (p ProvSettings) resyncSeconds(mac string) int {
	base := int(p.Resync / time.Second)
	spread := base / 10
	if spread < 1 {
		return base
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(mac))
	return base - int(h.Sum32()%uint32(spread)) //nolint:gosec // spread is positive and below 2^31
}

// Phone is a provisioned phone as the API returns it (contract 7). Its
// token, admin password and the bound device's secret are never part of
// it.
type Phone struct {
	ID              int64           `json:"id"`
	MAC             string          `json:"mac"`
	Serial          string          `json:"serial"`
	Vendor          prov.Vendor     `json:"vendor"`
	Model           string          `json:"model"`
	Label           string          `json:"label"`
	DeviceID        *int64          `json:"deviceId"`
	ExtensionID     *int64          `json:"extensionId"`
	ExtensionNumber string          `json:"extensionNumber"`
	TemplateID      *int64          `json:"templateId"`
	BLF             []string        `json:"blf"`
	Enabled         bool            `json:"enabled"`
	TokenExposed    bool            `json:"tokenExposed"`
	UAMismatch      bool            `json:"uaMismatch"`
	BootArmed       bool            `json:"bootArmed"`
	BootReclaimed   bool            `json:"bootReclaimed"`
	RedirectStatus  redirect.Status `json:"redirectStatus"`
	FirstFetchAt    *time.Time      `json:"firstFetchAt"`
	LastFetchAt     *time.Time      `json:"lastFetchAt"`
	LastFetchIP     string          `json:"lastFetchIp"`
	LastFetchUA     string          `json:"lastFetchUa"`
	LastFetchFile   string          `json:"lastFetchFile"`
	FirmwareSeen    string          `json:"firmwareSeen"`
	RenderError     bool            `json:"renderError"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

const phoneSelect = `
	SELECT p.id, p.mac, COALESCE(p.serial, ''), p.vendor, p.model, p.label, p.device_id, d.extension_id,
	       COALESCE(e.number, ''), p.template_id, p.blf, p.enabled, p.token_exposed, p.ua_mismatch,
	       p.boot_armed, p.boot_reclaimed, p.redirect_status, p.first_fetch_at, p.last_fetch_at,
	       COALESCE(host(p.last_fetch_ip), ''), COALESCE(p.last_fetch_ua, ''), COALESCE(p.last_fetch_file, ''),
	       COALESCE(p.firmware_seen, ''),
	       COALESCE((SELECT f.result = 'render_error' FROM prov_fetches f WHERE f.phone_id = p.id
	                 ORDER BY f.at DESC, f.id DESC LIMIT 1), FALSE),
	       p.created_at, p.updated_at
	FROM phones p LEFT JOIN devices d ON d.id = p.device_id LEFT JOIN extensions e ON e.id = d.extension_id`

func scanPhone(r interface{ Scan(...any) error }) (Phone, error) {
	var (
		p             Phone
		dev, ext, tpl sql.NullInt64
		blf, status   []byte
		first, last   sql.NullTime
	)
	err := r.Scan(&p.ID, &p.MAC, &p.Serial, &p.Vendor, &p.Model, &p.Label, &dev, &ext, &p.ExtensionNumber, &tpl,
		&blf, &p.Enabled, &p.TokenExposed, &p.UAMismatch, &p.BootArmed, &p.BootReclaimed, &status, &first, &last,
		&p.LastFetchIP, &p.LastFetchUA, &p.LastFetchFile, &p.FirmwareSeen, &p.RenderError, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.DeviceID, p.ExtensionID, p.TemplateID = nullInt(dev), nullInt(ext), nullInt(tpl)
	p.FirstFetchAt, p.LastFetchAt = nullTime(first), nullTime(last)
	if err := json.Unmarshal(blf, &p.BLF); err != nil {
		return p, fmt.Errorf("phone %d blf: %w", p.ID, err)
	}
	if p.BLF == nil {
		p.BLF = []string{}
	}
	if err := json.Unmarshal(status, &p.RedirectStatus); err != nil {
		return p, fmt.Errorf("phone %d redirect status: %w", p.ID, err)
	}
	return p, nil
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// ListPhones returns every phone ordered by MAC.
func (s *Store) ListPhones(ctx context.Context) ([]Phone, error) {
	return listPhones(ctx, s.db, ` ORDER BY p.mac`)
}

func listPhones(ctx context.Context, q querier, suffix string, args ...any) ([]Phone, error) {
	rows, err := q.QueryContext(ctx, phoneSelect+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Phone{}
	for rows.Next() {
		p, err := scanPhone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPhone returns one phone.
func (s *Store) GetPhone(ctx context.Context, id int64) (Phone, error) {
	p, err := scanPhone(s.db.QueryRowContext(ctx, phoneSelect+` WHERE p.id = $1`, id))
	return p, mapErr(err)
}

// PhoneInput creates a phone. DeviceID 0 creates a device for the
// extension; otherwise that device is bound and its secret rotated. Realm
// is the SIP domain the device's HA1 values are computed for.
type PhoneInput struct {
	MAC         string // already normalised
	Serial      string
	Vendor      prov.Vendor
	Model       string
	Label       string
	ExtensionID int64
	DeviceID    int64
	BLF         []string
	Enabled     bool
	TemplateID  *int64
	Realm       string
}

// CreatedPhone is a new phone with the one thing shown once: its token.
type CreatedPhone struct {
	Phone Phone
	// Token is the plaintext provisioning token, for the provisioningUrl
	// of the create response only.
	Token string
	// SecretRotated: an existing device was bound and got a new secret.
	SecretRotated bool
}

// CreatePhone inserts a phone and binds its device in one transaction with
// its audit rows; the binding bumps the configuration revision.
func (s *Store) CreatePhone(ctx context.Context, actor string, in PhoneInput) (CreatedPhone, error) {
	var out CreatedPhone
	err := s.provChange(ctx, actor, "create", "phone", func(tx *sql.Tx) (string, bool, error) {
		var err error
		out, err = s.createPhone(ctx, tx, actor, in)
		return strconv.FormatInt(out.Phone.ID, 10), true, err
	})
	return out, err
}

// ImportPhones creates every phone in one transaction, or none.
func (s *Store) ImportPhones(ctx context.Context, actor string, in []PhoneInput) ([]Phone, error) {
	out := make([]Phone, 0, len(in))
	err := s.provChange(ctx, actor, "import", "phone", func(tx *sql.Tx) (string, bool, error) {
		for i, p := range in {
			c, err := s.createPhone(ctx, tx, actor, p)
			if err != nil {
				return "", false, fmt.Errorf("row %d: %w", i+1, err)
			}
			if err := insertAudit(ctx, tx, actor, "create", "phone", strconv.FormatInt(c.Phone.ID, 10)); err != nil {
				return "", false, err
			}
			out = append(out, c.Phone)
		}
		return strconv.Itoa(len(in)), len(in) > 0, nil
	})
	return out, err
}

// provChange runs fn in one transaction under the configuration lock and
// writes the audit row; when fn reports bump, it also bumps the
// configuration revision and notifies hello-sip, as Phase 1 device changes
// do. fn returns the resource id for the audit row.
func (s *Store) provChange(ctx context.Context, actor, action, resource string, fn func(*sql.Tx) (string, bool, error)) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(configLockKey)); err != nil {
			return err
		}
		id, bump, err := fn(tx)
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, actor, action, resource, id); err != nil {
			return err
		}
		if !bump {
			return nil
		}
		return bumpRevision(ctx, tx)
	})
}

func bumpRevision(ctx context.Context, tx *sql.Tx) error {
	var rev int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision`).Scan(&rev); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, NotifyChannel, strconv.FormatInt(rev, 10))
	return err
}

// conflict is a 409 whose message names what is in the way.
func conflict(format string, args ...any) error {
	return &InUseError{Msg: fmt.Sprintf(format, args...)}
}

// FormatMAC writes a stored MAC with colons, as administrators read it.
func FormatMAC(mac string) string {
	if len(mac) != 12 {
		return mac
	}
	parts := make([]string, 6)
	for i := range parts {
		parts[i] = mac[2*i : 2*i+2]
	}
	return strings.Join(parts, ":")
}

func (s *Store) createPhone(ctx context.Context, tx *sql.Tx, actor string, in PhoneInput) (CreatedPhone, error) {
	var out CreatedPhone
	if s.box == nil {
		return out, errors.New("store: no secret box to seal provisioning secrets")
	}
	var existing int64
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM phones WHERE mac = $1`, in.MAC).Scan(&existing); {
	case err == nil:
		return out, conflict("MAC %s is already phone %d", FormatMAC(in.MAC), existing)
	case !errors.Is(err, sql.ErrNoRows):
		return out, err
	}
	if err := checkPhoneRefs(ctx, tx, in.BLF, in.TemplateID); err != nil {
		return out, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT nextval('phones_id_seq')`).Scan(&id); err != nil {
		return out, err
	}
	devID, rotated, err := s.bindDevice(ctx, tx, actor, in.ExtensionID, in.DeviceID, in.MAC, in.Realm, 0)
	if err != nil {
		return out, err
	}
	token, hash := prov.NewToken()
	tokenEnc, err := s.box.Seal(token, prov.PhoneTokenAAD(id))
	if err != nil {
		return out, err
	}
	adminEnc, err := s.box.Seal(newAdminPassword(), prov.PhoneAdminAAD(id))
	if err != nil {
		return out, err
	}
	status, err := s.queueRedirect(ctx, tx, in.Vendor, in.MAC, redirect.OpRegister)
	if err != nil {
		return out, err
	}
	statusJSON, _ := json.Marshal(status)
	blf, _ := json.Marshal(nonNil(in.BLF))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO phones (id, mac, serial, vendor, model, label, device_id, template_id, blf, enabled,
		                    token_hash, token_enc, admin_password_enc, redirect_status)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		id, in.MAC, in.Serial, in.Vendor, in.Model, in.Label, devID, in.TemplateID, string(blf), in.Enabled,
		hash, tokenEnc, adminEnc, string(statusJSON)); err != nil {
		return out, err
	}
	p, err := scanPhone(tx.QueryRowContext(ctx, phoneSelect+` WHERE p.id = $1`, id))
	return CreatedPhone{Phone: p, Token: token, SecretRotated: rotated}, err
}

// checkPhoneRefs refuses BLF numbers that are not extensions and an
// override template that does not exist, naming the field.
func checkPhoneRefs(ctx context.Context, tx *sql.Tx, blf []string, templateID *int64) error {
	if len(blf) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT number FROM extensions WHERE number = ANY($1)`, blf)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return err
			}
			known[n] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, n := range blf {
			if !known[n] {
				return fieldError(fmt.Sprintf("blf[%d]", i), "no extension has number "+strconv.Quote(n))
			}
		}
	}
	if templateID != nil {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM prov_templates WHERE id = $1`, *templateID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fieldError("templateId", "no such template")
			}
			return err
		}
	}
	return nil
}

// bindDevice binds a device to phone (0 while the phone is being created)
// and gives it a new secret, sealed, with its HA1 values updated. deviceID
// 0 creates the device <extension>-<last 6 MAC hex> on extensionID, or
// reuses an unbound one of that name on it (a phone deleted and added
// again). It reports whether an existing device's secret was rotated.
func (s *Store) bindDevice(ctx context.Context, tx *sql.Tx, actor string, extensionID, deviceID int64, mac, realm string, phone int64) (int64, bool, error) {
	var number string
	if err := tx.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1`, extensionID).Scan(&number); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, fieldError("extensionId", "no such extension")
		}
		return 0, false, err
	}
	rotated := true
	if deviceID == 0 {
		username := number + "-" + mac[6:]
		var ext int64
		err := tx.QueryRowContext(ctx, `SELECT id, extension_id FROM devices WHERE sip_username = $1 FOR UPDATE`, username).Scan(&deviceID, &ext)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256, enabled)
				VALUES ($1, $2, $3, '', '', TRUE) RETURNING id`, extensionID, username, realm).Scan(&deviceID); err != nil {
				return 0, false, err
			}
			if err := insertAudit(ctx, tx, actor, "create", "device", strconv.FormatInt(deviceID, 10)); err != nil {
				return 0, false, err
			}
			rotated = false
		case err != nil:
			return 0, false, err
		case ext != extensionID:
			return 0, false, conflict("device %s belongs to another extension", username)
		}
	} else {
		var ext int64
		if err := tx.QueryRowContext(ctx, `SELECT extension_id FROM devices WHERE id = $1 FOR UPDATE`, deviceID).Scan(&ext); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, fieldError("deviceId", "no such device")
			}
			return 0, false, err
		}
		if ext != extensionID {
			return 0, false, fieldError("deviceId", "the device belongs to another extension")
		}
	}
	var other int64
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM phones WHERE device_id = $1 AND id <> $2`, deviceID, phone).Scan(&other); {
	case err == nil:
		return 0, false, conflict("device %d is already bound to phone %d", deviceID, other)
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	if err := s.sealDeviceSecret(ctx, tx, deviceID, realm, auth.NewDeviceSecret()); err != nil {
		return 0, false, err
	}
	if rotated {
		if err := insertAudit(ctx, tx, actor, "bind-rotate-secret", "device", strconv.FormatInt(deviceID, 10)); err != nil {
			return 0, false, err
		}
	}
	return deviceID, rotated, nil
}

// sealDeviceSecret stores secret as the device's HA1 values for realm and,
// sealed, in secret_enc.
func (s *Store) sealDeviceSecret(ctx context.Context, tx *sql.Tx, deviceID int64, realm, secret string) error {
	var username string
	if err := tx.QueryRowContext(ctx, `SELECT sip_username FROM devices WHERE id = $1`, deviceID).Scan(&username); err != nil {
		return err
	}
	sealed, err := s.box.Seal(secret, prov.DeviceSecretAAD(deviceID))
	if err != nil {
		return err
	}
	md5Hex, shaHex := auth.HA1(username, realm, secret)
	_, err = tx.ExecContext(ctx, `
		UPDATE devices SET realm = $2, ha1_md5 = $3, ha1_sha256 = $4, secret_enc = $5, updated_at = now()
		WHERE id = $1`, deviceID, realm, md5Hex, shaHex, sealed)
	return err
}

// unbindDevice drops a device's sealed secret; it keeps its HA1 values.
func unbindDevice(ctx context.Context, tx *sql.Tx, deviceID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE devices SET secret_enc = NULL, updated_at = now() WHERE id = $1`, deviceID)
	return err
}

// adminAlphabet avoids look-alike characters, since the password is read
// off a screen and typed into a phone's web UI.
const adminAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// newAdminPassword is 20 random characters (spec S-20).
func newAdminPassword() string {
	b := make([]byte, 20)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(adminAlphabet))))
		if err != nil {
			panic(err) // crypto/rand does not fail on supported platforms
		}
		b[i] = adminAlphabet[n.Int64()]
	}
	return string(b)
}

// PhoneChange holds the fields of a phone update; nil keeps one.
// TemplateID set with a nil inner pointer clears the override. Bind moves
// the phone to another device (rotating that device's secret); Unbind
// leaves it without one, which also disables it.
type PhoneChange struct {
	Serial     *string
	Vendor     *prov.Vendor
	Model      *string
	Label      *string
	BLF        *[]string
	Enabled    *bool
	TemplateID **int64
	Bind       *Binding
	Unbind     bool
	Realm      string
}

// Binding names the device a phone provisions: an existing one, or 0 to
// create one on the extension.
type Binding struct {
	ExtensionID int64
	DeviceID    int64
}

// UpdatePhone changes the fields that are not nil. Binding another device
// rotates and seals its secret and drops the old device's sealed secret;
// it alone bumps the configuration revision. secretRotated reports a bound
// existing device.
func (s *Store) UpdatePhone(ctx context.Context, actor string, id int64, c PhoneChange) (p Phone, secretRotated bool, err error) {
	err = s.provChange(ctx, actor, "update", "phone", func(tx *sql.Tx) (string, bool, error) {
		cur, err := scanPhone(tx.QueryRowContext(ctx, phoneSelect+` WHERE p.id = $1 FOR UPDATE OF p`, id))
		if err != nil {
			return "", false, err
		}
		sid := strconv.FormatInt(id, 10)
		var tpl *int64
		if c.TemplateID != nil {
			tpl = *c.TemplateID
		}
		var blf []string
		if c.BLF != nil {
			blf = *c.BLF
		}
		if err := checkPhoneRefs(ctx, tx, blf, tpl); err != nil {
			return sid, false, err
		}
		device, bumped := cur.DeviceID, false
		switch {
		case c.Unbind:
			if device != nil {
				if err := unbindDevice(ctx, tx, *device); err != nil {
					return sid, false, err
				}
				device = nil
			}
		case c.Bind != nil && (cur.DeviceID == nil || c.Bind.DeviceID != *cur.DeviceID ||
			cur.ExtensionID == nil || c.Bind.ExtensionID != *cur.ExtensionID):
			if device != nil {
				if err := unbindDevice(ctx, tx, *device); err != nil {
					return sid, false, err
				}
				// The old device may be the one being rebound by name.
				if _, err := tx.ExecContext(ctx, `UPDATE phones SET device_id = NULL, enabled = FALSE WHERE id = $1`, id); err != nil {
					return sid, false, err
				}
			}
			d, rotated, err := s.bindDevice(ctx, tx, actor, c.Bind.ExtensionID, c.Bind.DeviceID, cur.MAC, c.Realm, id)
			if err != nil {
				return sid, false, err
			}
			device, bumped, secretRotated = &d, true, rotated
		}
		enabled := cur.Enabled
		if c.Enabled != nil {
			enabled = *c.Enabled
		}
		if device == nil {
			if c.Enabled != nil && *c.Enabled {
				return sid, false, fieldError("enabled", "a phone without a device cannot be enabled")
			}
			enabled = false
		}
		vendor := cur.Vendor
		if c.Vendor != nil && *c.Vendor != cur.Vendor {
			vendor = *c.Vendor
			if _, err := s.queueRedirect(ctx, tx, cur.Vendor, cur.MAC, redirect.OpUnregister); err != nil {
				return sid, false, err
			}
			st, err := s.queueRedirect(ctx, tx, vendor, cur.MAC, redirect.OpRegister)
			if err != nil {
				return sid, false, err
			}
			if err := setRedirectStatus(ctx, tx, id, st); err != nil {
				return sid, false, err
			}
		}
		var blfArg any
		if c.BLF != nil {
			b, _ := json.Marshal(nonNil(*c.BLF))
			blfArg = string(b)
		}
		setTpl, tplArg := c.TemplateID != nil, any(nil)
		if tpl != nil {
			tplArg = *tpl
		}
		var serial any
		if c.Serial != nil {
			serial = *c.Serial
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE phones SET serial = CASE WHEN $2::text IS NULL THEN serial ELSE NULLIF($2, '') END,
			       vendor = $3, model = COALESCE($4, model), label = COALESCE($5, label),
			       blf = COALESCE($6::jsonb, blf), enabled = $7,
			       template_id = CASE WHEN $8 THEN $9::bigint ELSE template_id END,
			       device_id = $10, updated_at = now()
			WHERE id = $1`, id, serial, vendor, c.Model, c.Label, blfArg, enabled, setTpl, tplArg, device); err != nil {
			return sid, false, err
		}
		p, err = scanPhone(tx.QueryRowContext(ctx, phoneSelect+` WHERE p.id = $1`, id))
		return sid, bumped, err
	})
	return p, secretRotated, err
}

// DeletePhone removes a phone. Its device stays (with its HA1 values) but
// loses the sealed secret; the vendor redirect registration is withdrawn.
func (s *Store) DeletePhone(ctx context.Context, actor string, id int64) error {
	return s.provChange(ctx, actor, "delete", "phone", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		var (
			mac    string
			vendor prov.Vendor
			device sql.NullInt64
		)
		if err := tx.QueryRowContext(ctx, `SELECT mac, vendor, device_id FROM phones WHERE id = $1 FOR UPDATE`, id).
			Scan(&mac, &vendor, &device); err != nil {
			return sid, false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM phones WHERE id = $1`, id); err != nil {
			return sid, false, err
		}
		if device.Valid {
			if err := unbindDevice(ctx, tx, device.Int64); err != nil {
				return sid, false, err
			}
		}
		_, err := s.queueRedirect(ctx, tx, vendor, mac, redirect.OpUnregister)
		return sid, false, err
	})
}

// RotatePhoneToken gives a phone a new token. The old one stays valid as
// the previous token for the grace period (or until the phone fetches with
// the new one), unless immediate revokes it at once (spec S-3). The
// redirect registration follows the new URL.
func (s *Store) RotatePhoneToken(ctx context.Context, actor string, id int64, immediate bool) (CreatedPhone, error) {
	return s.newToken(ctx, actor, "rotate-token", id, immediate, false)
}

// RearmPhone rotates the token at once and arms the phone for one more
// boot hand-off (spec S-10).
func (s *Store) RearmPhone(ctx context.Context, actor string, id int64) (CreatedPhone, error) {
	return s.newToken(ctx, actor, "rearm", id, true, true)
}

func (s *Store) newToken(ctx context.Context, actor, action string, id int64, immediate, rearm bool) (CreatedPhone, error) {
	var out CreatedPhone
	if s.box == nil {
		return out, errors.New("store: no secret box to seal provisioning secrets")
	}
	err := s.provChange(ctx, actor, action, "phone", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		var (
			mac    string
			vendor prov.Vendor
		)
		if err := tx.QueryRowContext(ctx, `SELECT mac, vendor FROM phones WHERE id = $1 FOR UPDATE`, id).Scan(&mac, &vendor); err != nil {
			return sid, false, err
		}
		token, hash := prov.NewToken()
		enc, err := s.box.Seal(token, prov.PhoneTokenAAD(id))
		if err != nil {
			return sid, false, err
		}
		// The grace is computed by the database clock, the one
		// PhoneByToken compares it with.
		grace := s.prov.TokenGrace
		if grace <= 0 {
			grace = 7 * 24 * time.Hour
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE phones SET
			       prev_token_hash = CASE WHEN $4 THEN NULL ELSE token_hash END,
			       prev_token_expires = CASE WHEN $4 THEN NULL ELSE now() + make_interval(secs => $5) END,
			       token_hash = $2, token_enc = $3,
			       token_exposed = FALSE,
			       boot_armed = CASE WHEN $6 THEN TRUE ELSE boot_armed END,
			       boot_reclaimed = CASE WHEN $6 THEN FALSE ELSE boot_reclaimed END,
			       updated_at = now()
			WHERE id = $1`, id, hash, enc, immediate, grace.Seconds(), rearm); err != nil {
			return sid, false, err
		}
		st, err := s.queueRedirect(ctx, tx, vendor, mac, redirect.OpRegister)
		if err != nil {
			return sid, false, err
		}
		if st.State == redirect.StatePending {
			if err := setRedirectStatus(ctx, tx, id, st); err != nil {
				return sid, false, err
			}
		}
		p, err := scanPhone(tx.QueryRowContext(ctx, phoneSelect+` WHERE p.id = $1`, id))
		out = CreatedPhone{Phone: p, Token: token}
		return sid, false, err
	})
	return out, err
}

// RevealAdminPassword opens a phone's admin password and records who read
// it, in one transaction: no reveal without its audit row (spec S-20).
func (s *Store) RevealAdminPassword(ctx context.Context, actor string, id int64) (string, error) {
	var pw string
	if s.box == nil {
		return "", errors.New("store: no secret box to open provisioning secrets")
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var enc []byte
		if err := tx.QueryRowContext(ctx, `SELECT admin_password_enc FROM phones WHERE id = $1`, id).Scan(&enc); err != nil {
			return err
		}
		var err error
		if pw, err = s.box.Open(enc, prov.PhoneAdminAAD(id)); err != nil {
			return prov.ErrSealed
		}
		return insertAudit(ctx, tx, actor, "reveal-admin-password", "phone", strconv.FormatInt(id, 10))
	})
	return pw, err
}

// RotateAdminPassword gives a phone a new random admin password; the phone
// takes it at its next fetch.
func (s *Store) RotateAdminPassword(ctx context.Context, actor string, id int64) error {
	if s.box == nil {
		return errors.New("store: no secret box to seal provisioning secrets")
	}
	return s.provChange(ctx, actor, "rotate-admin-password", "phone", func(tx *sql.Tx) (string, bool, error) {
		sid := strconv.FormatInt(id, 10)
		enc, err := s.box.Seal(newAdminPassword(), prov.PhoneAdminAAD(id))
		if err != nil {
			return sid, false, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE phones SET admin_password_enc = $2, updated_at = now() WHERE id = $1`, id, enc)
		if err != nil {
			return sid, false, err
		}
		return sid, false, requireRow(res)
	})
}

// ExistingMACs returns the phone id of each listed MAC that is taken.
func (s *Store) ExistingMACs(ctx context.Context, macs []string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mac, id FROM phones WHERE mac = ANY($1)`, macs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var (
			mac string
			id  int64
		)
		if err := rows.Scan(&mac, &id); err != nil {
			return nil, err
		}
		out[mac] = id
	}
	return out, rows.Err()
}

// Fetch is one provisioning request as the API returns it (contract 7).
type Fetch struct {
	At        time.Time     `json:"at"`
	IP        string        `json:"ip"`
	UserAgent string        `json:"userAgent"`
	Path      string        `json:"path"`
	Kind      prov.FileKind `json:"kind"`
	Result    prov.Result   `json:"result"`
	Status    int           `json:"status"`
	Bytes     int64         `json:"bytes"`
}

// ListPhoneFetches returns a phone's fetches newest first, limit at a time
// before the fetch id before (0: from the newest); next is the cursor of
// the following page, "" at the end.
func (s *Store) ListPhoneFetches(ctx context.Context, phoneID, before int64, limit int) ([]Fetch, string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, at, COALESCE(host(ip), ''), user_agent, path_redacted, kind, result, status, bytes
		FROM prov_fetches WHERE phone_id = $1 AND ($2::bigint = 0 OR id < $2::bigint)
		ORDER BY id DESC LIMIT $3`, phoneID, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := []Fetch{}
	var ids []int64
	for rows.Next() {
		var (
			f  Fetch
			id int64
		)
		if err := rows.Scan(&id, &f.At, &f.IP, &f.UserAgent, &f.Path, &f.Kind, &f.Result, &f.Status, &f.Bytes); err != nil {
			return nil, "", err
		}
		out, ids = append(out, f), append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(ids[limit-1], 10)
	}
	return out, next, nil
}

// PruneFetches deletes fetch audit rows older than before.
func (s *Store) PruneFetches(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM prov_fetches WHERE at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// boundPhone names the phone bound to one of a set of devices, for the
// 409 that refuses deleting them (contract 8).
func boundPhone(ctx context.Context, tx *sql.Tx, where string, arg int64) error {
	var (
		mac string
		id  int64
	)
	err := tx.QueryRowContext(ctx, `SELECT p.id, p.mac FROM phones p JOIN devices d ON d.id = p.device_id WHERE `+where+` LIMIT 1`, arg).Scan(&id, &mac) //nolint:gosec // G202: where is a caller's constant condition
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return conflict("phone %d (%s) provisions this device; delete or rebind the phone first", id, FormatMAC(mac))
}

// splitHostPort splits the registrar address; the port defaults to 5060.
func splitHostPort(hp string) (string, int) {
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return hp, 5060
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return host, 5060
	}
	return host, n
}

var redirectSupported = []prov.Vendor{prov.Yealink, prov.Snom, prov.Grandstream}

// RedirectSupported reports whether Hello drives v's redirect service
// (spec S-11: Snom SRAPS, Yealink RPS/YMCS, Grandstream GDMS).
func RedirectSupported(v prov.Vendor) bool { return slices.Contains(redirectSupported, v) }

// queueRedirect queues op for the vendor's redirect service when the
// vendor is configured (deployment credentials, or a stored account that
// is enabled with credentials) and returns the status the phone shows:
// pending when queued, manual for the vendors Hello cannot drive (Poly,
// Fanvil; their redirect clients report Supported false), not_configured
// otherwise. A newer operation for the same vendor and MAC replaces an
// older one with a new seq.
func (s *Store) queueRedirect(ctx context.Context, tx *sql.Tx, v prov.Vendor, mac string, op redirect.Op) (redirect.Status, error) {
	switch {
	case v == prov.Poly || v == prov.Fanvil:
		return redirect.Status{State: redirect.StateManual}, nil
	case !slices.Contains(redirectSupported, v):
		return redirect.Status{State: redirect.StateNotConfigured}, nil
	}
	configured := s.prov.Deployment[v]
	if !configured {
		err := tx.QueryRowContext(ctx, `SELECT enabled AND credentials_enc IS NOT NULL FROM prov_redirect_accounts WHERE vendor = $1`, v).Scan(&configured)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return redirect.Status{}, err
		}
	}
	if !configured {
		return redirect.Status{State: redirect.StateNotConfigured}, nil
	}
	if err := upsertJob(ctx, tx, `VALUES ($1, $2, $3)`, v, mac, op); err != nil {
		return redirect.Status{}, err
	}
	return redirect.Status{State: redirect.StatePending, At: time.Now().UTC()}, nil
}

// upsertJob inserts jobs from source (a VALUES list or a SELECT of vendor,
// mac, op). A replacement takes a new seq and starts over; the 24-hour
// give-up keeps counting while the operation stays the same.
func upsertJob(ctx context.Context, tx *sql.Tx, source string, args ...any) error {
	//nolint:gosec // G202: source is a caller's constant VALUES list or SELECT.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO prov_redirect_jobs (vendor, mac, op) `+source+`
		ON CONFLICT (vendor, mac) DO UPDATE SET
		       seq = nextval('prov_redirect_jobs_seq'), op = EXCLUDED.op, attempts = 0,
		       next_attempt_at = now(), last_error = '',
		       first_queued_at = CASE WHEN prov_redirect_jobs.op = EXCLUDED.op
		                              THEN prov_redirect_jobs.first_queued_at ELSE now() END`, args...)
	return err
}

func setRedirectStatus(ctx context.Context, tx *sql.Tx, id int64, st redirect.Status) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE phones SET redirect_status = $2 WHERE id = $1`, id, string(b))
	return err
}
