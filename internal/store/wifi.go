package store

// Wi-Fi profiles and roaming (migration 00052, spec phone-feature-parity S-23
// and S-24). Profiles are sealed with the secret box and never leave the
// store as plain text except into the provisioning render data. A change does
// not bump the configuration revision: only provisioning reads it, so the
// automatic resync notices the phone's changed render data.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// WifiProfile is a profile as the API shows it: the secrets only as flags.
type WifiProfile struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	SSID              string    `json:"ssid"`
	Security          string    `json:"security"`
	PSKSet            bool      `json:"pskSet"`
	EAPMethod         string    `json:"eapMethod"`
	Identity          string    `json:"identity"`
	AnonymousIdentity string    `json:"anonymousIdentity"`
	PasswordSet       bool      `json:"passwordSet"`
	CACert            string    `json:"caCert"`
	Priority          int       `json:"priority"`
	Hidden            bool      `json:"hidden"`
	PreferAccount     int       `json:"preferAccount"`
	Verified          bool      `json:"verified"`
	Groups            int       `json:"groups"`
	Phones            int       `json:"phones"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// WifiProfileInput is a profile to store. PSK and Password are nil to keep
// the stored secret, "" to clear it, else the new value.
type WifiProfileInput struct {
	Name, SSID, Security                   string
	EAPMethod, Identity, AnonymousIdentity string
	CACert                                 string
	Priority                               int
	Hidden                                 bool
	PreferAccount                          int
	Verified                               bool
	PSK, Password                          *string
}

// WifiScope is the Wi-Fi setup of a phone group or a phone.
type WifiScope struct {
	// ProfileIDs are the profiles assigned to this group or phone.
	ProfileIDs []int64 `json:"profileIds"`
	// InheritedIDs are, for a phone, the profiles of its group.
	InheritedIDs []int64 `json:"inheritedProfileIds"`
	// Pushing is set when a verified profile is among the phone's (or the
	// group's) profiles, so provisioning writes the networks.
	Pushing bool `json:"pushing"`
	// Radio is a group's roaming settings; nil for a phone.
	Radio *prov.WifiRadio `json:"radio,omitempty"`
}

const (
	wifiSetPSK       = `UPDATE wifi_profiles SET psk_enc = $2 WHERE id = $1`
	wifiSetEAPSecret = `UPDATE wifi_profiles SET password_enc = $2 WHERE id = $1` //nolint:gosec // G101: SQL text, not a credential
)

const wifiProfileCols = `p.id, p.name, p.ssid, p.security, p.psk_enc IS NOT NULL, p.eap_method, p.identity, p.anonymous_identity,
	p.password_enc IS NOT NULL, p.ca_cert, p.priority, p.hidden, p.prefer_account, p.verified,
	(SELECT count(*) FROM wifi_assignments a WHERE a.profile_id = p.id AND a.group_id IS NOT NULL),
	(SELECT count(*) FROM wifi_assignments a WHERE a.profile_id = p.id AND a.phone_id IS NOT NULL), p.updated_at`

func scanWifiProfile(sc interface{ Scan(...any) error }) (WifiProfile, error) {
	var p WifiProfile
	err := sc.Scan(&p.ID, &p.Name, &p.SSID, &p.Security, &p.PSKSet, &p.EAPMethod, &p.Identity, &p.AnonymousIdentity,
		&p.PasswordSet, &p.CACert, &p.Priority, &p.Hidden, &p.PreferAccount, &p.Verified, &p.Groups, &p.Phones, &p.UpdatedAt)
	return p, err
}

// ListWifiProfiles returns every profile by name.
func (s *Store) ListWifiProfiles(ctx context.Context) ([]WifiProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+wifiProfileCols+` FROM wifi_profiles p ORDER BY lower(p.name)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []WifiProfile
	for rows.Next() {
		p, err := scanWifiProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetWifiProfile returns one profile; ErrNotFound when there is none.
func (s *Store) GetWifiProfile(ctx context.Context, id int64) (WifiProfile, error) {
	p, err := scanWifiProfile(s.db.QueryRowContext(ctx, `SELECT `+wifiProfileCols+` FROM wifi_profiles p WHERE p.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func wifiNameError(err error, name string) error {
	if errors.Is(mapErr(err), ErrConflict) {
		return conflict("a Wi-Fi profile named %q already exists", name)
	}
	return err
}

// sealWifi stores the sealed secret of a profile column with the update
// statement (wifiSetPSK or wifiSetEAPSecret): nil keeps it, "" clears it.
func (s *Store) sealWifi(ctx context.Context, tx *sql.Tx, id int64, update, aad string, v *string) error {
	if v == nil {
		return nil
	}
	var enc []byte
	if *v != "" {
		if s.box == nil {
			return errors.New("store: no secret box configured for Wi-Fi secrets")
		}
		var err error
		if enc, err = s.box.Seal(*v, aad); err != nil {
			return fmt.Errorf("store: seal Wi-Fi profile %d: %w", id, err)
		}
	}
	_, err := tx.ExecContext(ctx, update, id, enc)
	return err
}

// CreateWifiProfile stores a profile and seals its secrets with the new id.
func (s *Store) CreateWifiProfile(ctx context.Context, actor string, in WifiProfileInput) (WifiProfile, error) {
	var id int64
	err := s.provChange(ctx, actor, "create", "wifi_profile", func(tx *sql.Tx) (string, bool, error) {
		if err := tx.QueryRowContext(ctx, `SELECT nextval('wifi_profiles_id_seq')`).Scan(&id); err != nil {
			return "", false, err
		}
		rid := strconv.FormatInt(id, 10)
		if _, err := tx.ExecContext(ctx, `INSERT INTO wifi_profiles (id, name, ssid, security, eap_method, identity, anonymous_identity,
				ca_cert, priority, hidden, prefer_account, verified)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, in.Name, in.SSID, in.Security, in.EAPMethod, in.Identity, in.AnonymousIdentity,
			in.CACert, in.Priority, in.Hidden, in.PreferAccount, in.Verified); err != nil {
			return rid, false, wifiNameError(err, in.Name)
		}
		if err := s.sealWifi(ctx, tx, id, wifiSetPSK, prov.WifiPSKAAD(id), in.PSK); err != nil {
			return rid, false, err
		}
		return rid, false, s.sealWifi(ctx, tx, id, wifiSetEAPSecret, prov.WifiPasswordAAD(id), in.Password)
	})
	if err != nil {
		return WifiProfile{}, err
	}
	return s.GetWifiProfile(ctx, id)
}

// UpdateWifiProfile replaces a profile's settings; a nil PSK or Password keeps
// the stored secret.
func (s *Store) UpdateWifiProfile(ctx context.Context, actor string, id int64, in WifiProfileInput) (WifiProfile, error) {
	err := s.provChange(ctx, actor, "update", "wifi_profile", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		res, err := tx.ExecContext(ctx, `UPDATE wifi_profiles SET name = $2, ssid = $3, security = $4, eap_method = $5, identity = $6,
				anonymous_identity = $7, ca_cert = $8, priority = $9, hidden = $10, prefer_account = $11, verified = $12, updated_at = now()
			WHERE id = $1`,
			id, in.Name, in.SSID, in.Security, in.EAPMethod, in.Identity, in.AnonymousIdentity,
			in.CACert, in.Priority, in.Hidden, in.PreferAccount, in.Verified)
		if err != nil {
			return rid, false, wifiNameError(err, in.Name)
		}
		if err := requireRow(res); err != nil {
			return rid, false, err
		}
		if err := s.sealWifi(ctx, tx, id, wifiSetPSK, prov.WifiPSKAAD(id), in.PSK); err != nil {
			return rid, false, err
		}
		return rid, false, s.sealWifi(ctx, tx, id, wifiSetEAPSecret, prov.WifiPasswordAAD(id), in.Password)
	})
	if err != nil {
		return WifiProfile{}, err
	}
	return s.GetWifiProfile(ctx, id)
}

// DeleteWifiProfile removes a profile no group or phone uses.
func (s *Store) DeleteWifiProfile(ctx context.Context, actor string, id int64) error {
	return s.provChange(ctx, actor, "delete", "wifi_profile", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(id, 10)
		var groups, phones int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM wifi_profiles WHERE id = $1 FOR UPDATE`, id).Scan(new(int)); err != nil {
			return rid, false, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE group_id IS NOT NULL), count(*) FILTER (WHERE phone_id IS NOT NULL)
			FROM wifi_assignments WHERE profile_id = $1`, id).Scan(&groups, &phones); err != nil {
			return rid, false, err
		}
		if groups+phones > 0 {
			return rid, false, conflict("the Wi-Fi profile is assigned to %d phone groups and %d phones; remove it from them first", groups, phones)
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM wifi_profiles WHERE id = $1`, id)
		return rid, false, err
	})
}

func (s *Store) wifiIDs(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetWifiGroup returns a phone group's profiles and radio settings.
func (s *Store) GetWifiGroup(ctx context.Context, groupID int64) (WifiScope, error) {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = $1`, groupID).Scan(&one); err != nil {
		return WifiScope{}, mapErr(err)
	}
	sc := WifiScope{InheritedIDs: []int64{}}
	var err error
	if sc.ProfileIDs, err = s.wifiIDs(ctx, `SELECT profile_id FROM wifi_assignments WHERE group_id = $1 ORDER BY profile_id`, groupID); err != nil {
		return sc, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(bool_or(p.verified), FALSE) FROM wifi_assignments a JOIN wifi_profiles p ON p.id = a.profile_id
		WHERE a.group_id = $1`, groupID).Scan(&sc.Pushing); err != nil {
		return sc, err
	}
	r := prov.WifiRadio{}
	err = s.db.QueryRowContext(ctx, `SELECT band, roam_mode, threshold, poor_scan, good_scan, gain, dot11r, dot11v, prompt FROM wifi_radio WHERE group_id = $1`, groupID).
		Scan(nullable(&r.Band), nullable(&r.RoamMode), nullable(&r.Threshold), nullable(&r.PoorScan), nullable(&r.GoodScan), nullable(&r.Gain),
			nullable(&r.Dot11r), nullable(&r.Dot11v), nullable(&r.Prompt))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sc, err
	}
	sc.Radio = &r
	return sc, nil
}

// nullable scans a nullable column into a pointer field.
type nullScan[T any] struct{ dst **T }

func nullable[T any](dst **T) *nullScan[T] { return &nullScan[T]{dst} }

func (n *nullScan[T]) Scan(src any) error {
	if src == nil {
		*n.dst = nil
		return nil
	}
	var v T
	switch p := any(&v).(type) {
	case *string:
		switch x := src.(type) {
		case string:
			*p = x
		case []byte:
			*p = string(x)
		default:
			return fmt.Errorf("store: cannot scan %T into a string", src)
		}
	case *int:
		x, ok := src.(int64)
		if !ok {
			return fmt.Errorf("store: cannot scan %T into an int", src)
		}
		*p = int(x)
	case *bool:
		x, ok := src.(bool)
		if !ok {
			return fmt.Errorf("store: cannot scan %T into a bool", src)
		}
		*p = x
	}
	*n.dst = &v
	return nil
}

// GetWifiPhone returns a phone's own profiles and those it inherits.
func (s *Store) GetWifiPhone(ctx context.Context, phoneID int64) (WifiScope, error) {
	var group sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT group_id FROM phones WHERE id = $1`, phoneID).Scan(&group); err != nil {
		return WifiScope{}, mapErr(err)
	}
	sc := WifiScope{InheritedIDs: []int64{}}
	var err error
	if sc.ProfileIDs, err = s.wifiIDs(ctx, `SELECT profile_id FROM wifi_assignments WHERE phone_id = $1 ORDER BY profile_id`, phoneID); err != nil {
		return sc, err
	}
	if group.Valid {
		if sc.InheritedIDs, err = s.wifiIDs(ctx, `SELECT profile_id FROM wifi_assignments WHERE group_id = $1 ORDER BY profile_id`, group.Int64); err != nil {
			return sc, err
		}
	}
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(bool_or(p.verified), FALSE) FROM wifi_assignments a JOIN wifi_profiles p ON p.id = a.profile_id
		WHERE a.phone_id = $1 OR a.group_id = $2`, phoneID, group).Scan(&sc.Pushing)
	return sc, err
}

// checkWifiIDs refuses a list that repeats a profile, names one that does not
// exist or is longer than prov.MaxWifiPerScope.
func checkWifiIDs(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) > prov.MaxWifiPerScope {
		return fieldError("profileIds", fmt.Sprintf("at most %d profiles", prov.MaxWifiPerScope))
	}
	for i, id := range ids {
		if slices.Index(ids, id) != i {
			return fieldError(fmt.Sprintf("profileIds[%d]", i), "names the same profile as an earlier entry")
		}
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM wifi_profiles WHERE id = $1`, id).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fieldError(fmt.Sprintf("profileIds[%d]", i), "no such Wi-Fi profile")
			}
			return err
		}
	}
	return nil
}

func replaceWifiAssignments(ctx context.Context, tx *sql.Tx, group bool, owner int64, ids []int64) error {
	del, ins := `DELETE FROM wifi_assignments WHERE phone_id = $1`, `INSERT INTO wifi_assignments (profile_id, phone_id) VALUES ($1, $2)`
	if group {
		del, ins = `DELETE FROM wifi_assignments WHERE group_id = $1`, `INSERT INTO wifi_assignments (profile_id, group_id) VALUES ($1, $2)`
	}
	if _, err := tx.ExecContext(ctx, del, owner); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, ins, id, owner); err != nil {
			return err
		}
	}
	return nil
}

// PutWifiGroup replaces a phone group's profiles and radio settings as a
// whole; an all-empty radio removes the settings. ErrNotFound for a group that
// does not exist, a field error for a bad profile list.
func (s *Store) PutWifiGroup(ctx context.Context, actor string, groupID int64, ids []int64, radio prov.WifiRadio) (WifiScope, error) {
	err := s.provChange(ctx, actor, "update", "wifi_group", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(groupID, 10)
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = $1 FOR UPDATE`, groupID).Scan(new(int)); err != nil {
			return rid, false, err
		}
		if err := checkWifiIDs(ctx, tx, ids); err != nil {
			return rid, false, err
		}
		if err := replaceWifiAssignments(ctx, tx, true, groupID, ids); err != nil {
			return rid, false, err
		}
		if radio == (prov.WifiRadio{}) {
			_, err := tx.ExecContext(ctx, `DELETE FROM wifi_radio WHERE group_id = $1`, groupID)
			return rid, false, err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO wifi_radio (group_id, band, roam_mode, threshold, poor_scan, good_scan, gain, dot11r, dot11v, prompt)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (group_id) DO UPDATE SET band = EXCLUDED.band, roam_mode = EXCLUDED.roam_mode, threshold = EXCLUDED.threshold,
				poor_scan = EXCLUDED.poor_scan, good_scan = EXCLUDED.good_scan, gain = EXCLUDED.gain, dot11r = EXCLUDED.dot11r,
				dot11v = EXCLUDED.dot11v, prompt = EXCLUDED.prompt, updated_at = now()`,
			groupID, radio.Band, radio.RoamMode, radio.Threshold, radio.PoorScan, radio.GoodScan, radio.Gain, radio.Dot11r, radio.Dot11v, radio.Prompt)
		return rid, false, err
	})
	if err != nil {
		return WifiScope{}, err
	}
	return s.GetWifiGroup(ctx, groupID)
}

// PutWifiPhone replaces the profiles assigned to one phone.
func (s *Store) PutWifiPhone(ctx context.Context, actor string, phoneID int64, ids []int64) (WifiScope, error) {
	err := s.provChange(ctx, actor, "update", "wifi_phone", func(tx *sql.Tx) (string, bool, error) {
		rid := strconv.FormatInt(phoneID, 10)
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phones WHERE id = $1 FOR UPDATE`, phoneID).Scan(new(int)); err != nil {
			return rid, false, err
		}
		if err := checkWifiIDs(ctx, tx, ids); err != nil {
			return rid, false, err
		}
		return rid, false, replaceWifiAssignments(ctx, tx, false, phoneID, ids)
	})
	if err != nil {
		return WifiScope{}, err
	}
	return s.GetWifiPhone(ctx, phoneID)
}

// wifiInfo is RenderData.Wifi for a phone: its group's profiles then its own
// (opened), and its group's radio settings. Nil for a model with no Wi-Fi keys
// and for a phone with neither.
func (s *Store) wifiInfo(ctx context.Context, phoneID int64, v prov.Vendor, model string) (*prov.WifiInfo, error) {
	if prov.WifiFamily(v, model) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.ssid, p.security, p.psk_enc, p.eap_method, p.identity, p.anonymous_identity, p.password_enc,
		       p.ca_cert, p.priority, p.hidden, p.prefer_account, p.verified, a.phone_id IS NOT NULL
		FROM wifi_assignments a JOIN wifi_profiles p ON p.id = a.profile_id
		WHERE a.phone_id = $1 OR a.group_id = (SELECT group_id FROM phones WHERE id = $1)
		ORDER BY p.id`, phoneID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var group, own []prov.WifiProfile
	for rows.Next() {
		var (
			p        prov.WifiProfile
			pskEnc   []byte
			passEnc  []byte
			isPhones bool
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.SSID, &p.Security, &pskEnc, &p.EAPMethod, &p.Identity, &p.AnonymousIdentity, &passEnc,
			&p.CACert, &p.Priority, &p.Hidden, &p.PreferAccount, &p.Verified, &isPhones); err != nil {
			return nil, err
		}
		if pskEnc != nil {
			if s.box == nil {
				return nil, prov.ErrSealed
			}
			if p.PSK, err = s.box.Open(pskEnc, prov.WifiPSKAAD(p.ID)); err != nil {
				return nil, prov.ErrSealed
			}
		}
		if passEnc != nil {
			if s.box == nil {
				return nil, prov.ErrSealed
			}
			if p.Password, err = s.box.Open(passEnc, prov.WifiPasswordAAD(p.ID)); err != nil {
				return nil, prov.ErrSealed
			}
		}
		if isPhones {
			own = append(own, p)
		} else {
			group = append(group, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	info := &prov.WifiInfo{Profiles: prov.MergeWifi(group, own)}
	for _, p := range info.Profiles {
		info.Verified = info.Verified || p.Verified
	}
	var r prov.WifiRadio
	err = s.db.QueryRowContext(ctx, `SELECT band, roam_mode, threshold, poor_scan, good_scan, gain, dot11r, dot11v, prompt
		FROM wifi_radio WHERE group_id = (SELECT group_id FROM phones WHERE id = $1)`, phoneID).
		Scan(nullable(&r.Band), nullable(&r.RoamMode), nullable(&r.Threshold), nullable(&r.PoorScan), nullable(&r.GoodScan), nullable(&r.Gain),
			nullable(&r.Dot11r), nullable(&r.Dot11v), nullable(&r.Prompt))
	switch {
	case err == nil:
		info.Radio = &r
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	if len(info.Profiles) == 0 && info.Radio == nil {
		return nil, nil
	}
	return info, nil
}
