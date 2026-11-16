package store

// Range auto-provisioning for directory connectors (spec integrations I-11,
// P12). The directory engine applies a user's change in its own transaction,
// which already holds the configuration lock; these two functions run inside
// it so that the numbering rules (the dial space of dialspace.go) and the
// whole-configuration check are the same ones every console edit runs.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/routing"
)

// ErrNoFreeNumber is a range with no number left.
var ErrNoFreeNumber = errors.New("store: no free number in the range")

var provNumberRe = regexp.MustCompile(`^[0-9]{2,10}$`)

// DirectoryExtension asks for one auto-provisioned extension.
type DirectoryExtension struct {
	// ConnectorID is the directory connector the extension is marked with.
	ConnectorID int64
	// Preferred is tried first (the user's earlier number, or the one the
	// preview showed); then RangeStart to RangeEnd in order. Both bounds have
	// the same number of digits.
	Preferred, RangeStart, RangeEnd string
	// Name is the extension's name ("" names it after its number).
	Name string
	// Voicemail turns the extension's voicemail on and mails it to Email.
	Voicemail bool
	Email     string
	// Device creates one SIP device on the extension with a generated
	// secret for Realm that is never shown or kept (only its HA1 values).
	Device bool
	Realm  string
}

// DirectoryExtensionResult is what ProvisionDirectoryExtension created.
type DirectoryExtensionResult struct {
	ExtensionID int64
	Number      string
	DeviceID    int64
}

// rangeNumbers calls fn with every number from lo to hi (same width).
func rangeNumbers(lo, hi string, fn func(string) bool) {
	if !provNumberRe.MatchString(lo) || len(lo) != len(hi) {
		return
	}
	a, _ := strconv.ParseInt(lo, 10, 64)
	b, _ := strconv.ParseInt(hi, 10, 64)
	for n := a; n <= b; n++ {
		if !fn(fmt.Sprintf("%0*d", len(lo), n)) {
			return
		}
	}
}

// DialNumbersTaken lists the numbers from lo to hi that the dial space
// already claims: an extension, ring group, IVR menu, conference room, queue
// or voicemail group number, a park slot, or a number that a feature or
// paging code is a prefix of (or that is a prefix of such a code: a phone
// dials the code and its argument as one string).
func (s *Store) DialNumbersTaken(ctx context.Context, tx *sql.Tx, lo, hi string) (map[string]bool, error) {
	return dialNumbersTaken(ctx, s.db.adopt(tx), lo, hi)
}

func dialNumbersTaken(ctx context.Context, tx *dbTx, lo, hi string) (map[string]bool, error) {
	items, names, err := loadDialItems(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	rangeNumbers(lo, hi, func(n string) bool {
		if _, ok := names[n]; ok {
			out[n] = true
			return true
		}
		v, _ := strconv.Atoi(n)
		for _, it := range items {
			if it.kind == "park slots" {
				// A slot is dialled as an integer: 7, never 007.
				if v >= it.lo && v <= it.hi && strconv.Itoa(v) == n {
					out[n] = true
					break
				}
				continue
			}
			if it.code != "" && codesCollide(it.code, n) {
				out[n] = true
				break
			}
		}
		return true
	})
	return out, nil
}

// ProvisionDirectoryExtension allocates and creates an extension for a
// directory user inside tx, which must hold the configuration lock (the
// directory store takes it first). The number is the preferred one when it
// is free, else the first free one of the range; none free is
// ErrNoFreeNumber. It creates the extension marked with the connector, its
// voicemail box and, when asked, one device, runs check like every
// configuration change (only errors the change introduces count) and audits
// each object as actor. The caller bumps the configuration revision.
func (s *Store) ProvisionDirectoryExtension(ctx context.Context, sqlTx *sql.Tx, actor string, in DirectoryExtension, check Check) (DirectoryExtensionResult, error) {
	tx := s.db.adopt(sqlTx)
	var res DirectoryExtensionResult
	var baseline map[string]bool
	if check != nil {
		before, err := s.loadRouting(ctx, tx)
		if err != nil {
			return res, err
		}
		baseline = errorKeys(before.Config, check(before.Config))
	}
	taken, err := dialNumbersTaken(ctx, tx, in.RangeStart, in.RangeEnd)
	if err != nil {
		return res, err
	}
	number := ""
	if provNumberRe.MatchString(in.Preferred) {
		// The preferred number may lie outside the range (the range changed
		// since it was allocated): it is checked on its own.
		own, err := dialNumbersTaken(ctx, tx, in.Preferred, in.Preferred)
		if err != nil {
			return res, err
		}
		if !own[in.Preferred] {
			number = in.Preferred
		}
	}
	if number == "" {
		rangeNumbers(in.RangeStart, in.RangeEnd, func(n string) bool {
			if !taken[n] {
				number = n
				return false
			}
			return true
		})
	}
	if number == "" {
		return res, ErrNoFreeNumber
	}
	name := in.Name
	if name == "" {
		name = number
	}
	email := ""
	if in.Voicemail {
		email = in.Email
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO extensions (number, name, external_number, kind, door_targets, voicemail_enabled, directory_hidden, provisioned_by)
		VALUES (@number, @name, '', @kind, @door_targets, @voicemail_enabled, FALSE, @provisioned_by) RETURNING id`,
		momo.Named("number", number), momo.Named("name", name), momo.Named("kind", ExtensionUser),
		momo.Named("door_targets", DoorTargets{}.JSON()), momo.Named("voicemail_enabled", in.Voicemail),
		momo.Named("provisioned_by", in.ConnectorID)).Scan(&res.ExtensionID); err != nil {
		return res, err
	}
	res.Number = number
	if _, err := tx.ExecContext(ctx, `INSERT INTO voicemail_boxes (extension_id, email) VALUES (@extension_id, @email)
		ON CONFLICT (extension_id) DO UPDATE SET email = EXCLUDED.email`, momo.Named("extension_id", res.ExtensionID), momo.Named("email", email)); err != nil {
		return res, err
	}
	if err := insertAudit(ctx, tx, actor, "create", "extension", strconv.FormatInt(res.ExtensionID, 10)); err != nil {
		return res, err
	}
	if in.Device {
		username, err := freeSIPUsername(ctx, tx, number)
		if err != nil {
			return res, err
		}
		// The secret is generated and dropped: nobody sees it; an
		// administrator rotates it from the device page to set up a phone.
		md5Hex, shaHex := auth.HA1(username, in.Realm, auth.NewDeviceSecret())
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256, enabled)
			VALUES (@extension_id, @sip_username, @realm, @ha1_md5, @ha1_sha256, TRUE) RETURNING id`,
			momo.Named("extension_id", res.ExtensionID), momo.Named("sip_username", username), momo.Named("realm", in.Realm),
			momo.Named("ha1_md5", md5Hex), momo.Named("ha1_sha256", shaHex)).Scan(&res.DeviceID); err != nil {
			return res, err
		}
		if err := insertAudit(ctx, tx, actor, "create", "device", strconv.FormatInt(res.DeviceID, 10)); err != nil {
			return res, err
		}
	}
	if check != nil {
		snap, err := s.loadRouting(ctx, tx)
		if err != nil {
			return res, err
		}
		var fresh []routing.FieldError
		for _, f := range check(snap.Config) {
			if !baseline[errorKey(snap.Config, f)] {
				fresh = append(fresh, f)
			}
		}
		if len(fresh) > 0 {
			return res, &ValidationError{Fields: fresh}
		}
	}
	return res, nil
}

// freeSIPUsername is base, or base-2, base-3, ... when a device already has
// the name.
func freeSIPUsername(ctx context.Context, tx *dbTx, base string) (string, error) {
	for i := 1; i <= 100; i++ {
		name := base
		if i > 1 {
			name = base + "-" + strconv.Itoa(i)
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM devices WHERE sip_username = @sip_username)`, momo.Named("sip_username", name)).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
	return "", fieldError("device", "no free SIP username for "+base)
}
