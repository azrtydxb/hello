package store

// The phone onboarding queue (spec phone-onboarding): unadopted phones that
// asked the provisioning endpoint for a configuration, the MACs operators
// ignore, and adopting a candidate as a phone.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/prov"
)

var _ prov.Onboarding = (*Store)(nil)

// onboardLockKey serialises inserts of new candidates, so two control pods
// cannot both fill the last free slot of the cap.
const onboardLockKey = 0x68656c6c6f6f6e62 // "helloonb"

// ttlSeconds is the candidate lifetime in seconds, for SQL.
const ttlSeconds = float64(prov.CandidateTTL / time.Second)

// Candidate is a queued, unadopted phone.
type Candidate struct {
	MAC           string    `json:"mac"`
	Vendor        string    `json:"vendor"`
	Model         string    `json:"model"`
	Firmware      string    `json:"firmware"`
	SourceIP      string    `json:"sourceIp"`
	RequestedPath string    `json:"requestedPath"`
	FirstSeen     time.Time `json:"firstSeen"`
	LastSeen      time.Time `json:"lastSeen"`
	RequestCount  int64     `json:"requestCount"`
}

// IgnoredMAC is a MAC operators told the queue never to record.
type IgnoredMAC struct {
	MAC       string    `json:"mac"`
	Vendor    string    `json:"vendor"`
	Model     string    `json:"model"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// upsertSQL inserts or refreshes a candidate. It binds @mac, @vendor,
// @model, @firmware, @source_ip (nullable), @path and @ttl (seconds), the
// arguments CaptureCandidate passes to it and to its refresh.
// A row unseen for the TTL (not pruned yet) starts over. Times come from the
// database clock, so pods with skewed clocks agree.
const upsertSQL = `
	INSERT INTO onboarding_candidates (mac, vendor, model, firmware, source_ip, requested_path)
	VALUES (@mac, @vendor, @model, @firmware, @source_ip::inet, @path)
	ON CONFLICT (mac) DO UPDATE SET
		vendor = CASE WHEN EXCLUDED.vendor <> 'generic' THEN EXCLUDED.vendor ELSE onboarding_candidates.vendor END,
		model = COALESCE(NULLIF(EXCLUDED.model, ''), onboarding_candidates.model),
		firmware = COALESCE(NULLIF(EXCLUDED.firmware, ''), onboarding_candidates.firmware),
		source_ip = COALESCE(EXCLUDED.source_ip, onboarding_candidates.source_ip),
		requested_path = EXCLUDED.requested_path,
		first_seen = CASE WHEN onboarding_candidates.last_seen < now() - @ttl::float8 * interval '1 second'
		                  THEN now() ELSE onboarding_candidates.first_seen END,
		request_count = CASE WHEN onboarding_candidates.last_seen < now() - @ttl::float8 * interval '1 second'
		                     THEN 1 ELSE onboarding_candidates.request_count + 1 END,
		last_seen = now()`

// CaptureCandidate implements prov.Onboarding. A candidate already queued is
// refreshed by one statement; a new MAC is inserted under an advisory lock
// that makes the cap exact across control pods.
func (s *Store) CaptureCandidate(ctx context.Context, c prov.Candidate) (prov.CaptureOutcome, error) {
	if !prov.CandidateMAC(c.MAC) {
		return "", errors.New("store: not a device MAC")
	}
	vendor := c.Vendor
	if !vendor.Valid() {
		vendor = prov.Generic
	}
	var ip any
	if c.IP.IsValid() {
		ip = c.IP.Unmap().String()
	}
	// Every name the refresh below and upsertSQL bind; both use them all.
	args := []any{momo.Named("mac", c.MAC), momo.Named("vendor", string(vendor)), momo.Named("model", c.Model),
		momo.Named("firmware", c.Firmware), momo.Named("source_ip", ip), momo.Named("path", c.Path), momo.Named("ttl", ttlSeconds)}
	// Fast path: refresh a row that is queued, unless the MAC has become a
	// phone or been ignored meanwhile.
	res, err := s.db.ExecContext(ctx, `
		UPDATE onboarding_candidates SET
			vendor = CASE WHEN @vendor::text <> 'generic' THEN @vendor::text ELSE vendor END,
			model = COALESCE(NULLIF(@model, ''), model),
			firmware = COALESCE(NULLIF(@firmware, ''), firmware),
			source_ip = COALESCE(@source_ip::inet, source_ip),
			requested_path = @path,
			first_seen = CASE WHEN last_seen < now() - @ttl::float8 * interval '1 second' THEN now() ELSE first_seen END,
			request_count = CASE WHEN last_seen < now() - @ttl::float8 * interval '1 second' THEN 1 ELSE request_count + 1 END,
			last_seen = now()
		WHERE mac = @mac
		  AND NOT EXISTS (SELECT 1 FROM phones WHERE mac = @mac)
		  AND NOT EXISTS (SELECT 1 FROM onboarding_ignored WHERE mac = @mac)`, args...)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n > 0 {
		return prov.CaptureStored, nil
	}
	out := prov.CaptureStored
	err = s.tx(ctx, func(tx *dbTx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(@key)`, momo.Named("key", int64(onboardLockKey))); err != nil {
			return err
		}
		var known, ignored bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS (SELECT 1 FROM phones WHERE mac = @mac),
			EXISTS (SELECT 1 FROM onboarding_ignored WHERE mac = @mac)`, momo.Named("mac", c.MAC)).Scan(&known, &ignored); err != nil {
			return err
		}
		switch {
		case known:
			out = prov.CaptureKnown
			return nil
		case ignored:
			out = prov.CaptureIgnored
			return nil
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM onboarding_candidates WHERE last_seen >= now() - @ttl::float8 * interval '1 second'`,
			momo.Named("ttl", ttlSeconds)).Scan(&n); err != nil {
			return err
		}
		if n >= prov.MaxCandidates {
			out = prov.CaptureFull
			return nil
		}
		_, err := tx.ExecContext(ctx, upsertSQL, args...)
		return err
	})
	return out, err
}

const candidateSelect = `
	SELECT mac, vendor, model, firmware, COALESCE(host(source_ip), ''), requested_path, first_seen, last_seen, request_count
	FROM onboarding_candidates`

func scanCandidate(r interface{ Scan(...any) error }) (Candidate, error) {
	var c Candidate
	err := r.Scan(&c.MAC, &c.Vendor, &c.Model, &c.Firmware, &c.SourceIP, &c.RequestedPath, &c.FirstSeen, &c.LastSeen, &c.RequestCount)
	return c, err
}

// ListCandidates returns the queue, newest sighting first. A row unseen for
// the TTL is expired and not listed even before the pruner removes it.
func (s *Store) ListCandidates(ctx context.Context) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, candidateSelect+`
		WHERE last_seen >= now() - @ttl::float8 * interval '1 second' ORDER BY last_seen DESC, mac`, momo.Named("ttl", ttlSeconds))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Candidate{}
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCandidate returns one queued candidate; ErrNotFound when the MAC is not
// queued (or has expired).
func (s *Store) GetCandidate(ctx context.Context, mac string) (Candidate, error) {
	c, err := scanCandidate(s.db.QueryRowContext(ctx, candidateSelect+
		` WHERE mac = @mac AND last_seen >= now() - @ttl::float8 * interval '1 second'`, momo.Named("mac", mac), momo.Named("ttl", ttlSeconds)))
	return c, mapErr(err)
}

// CountCandidates is the queue's size, for the hello_onboarding_candidates
// gauge.
func (s *Store) CountCandidates(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM onboarding_candidates WHERE last_seen >= now() - @ttl::float8 * interval '1 second'`,
		momo.Named("ttl", ttlSeconds)).Scan(&n)
	return n, err
}

// PruneCandidates deletes candidates unseen for the TTL. Idempotent, so every
// control pod may run it.
func (s *Store) PruneCandidates(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM onboarding_candidates WHERE last_seen < now() - @ttl::float8 * interval '1 second'`, momo.Named("ttl", ttlSeconds))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DismissCandidate removes a candidate from the queue; with ignore the MAC is
// also put on the ignore list, so it is not recorded again. ErrNotFound when
// the MAC is not queued.
func (s *Store) DismissCandidate(ctx context.Context, actor, mac string, ignore bool) error {
	return s.tx(ctx, func(tx *dbTx) error {
		var vendor, model string
		if err := tx.QueryRowContext(ctx,
			`DELETE FROM onboarding_candidates WHERE mac = @mac RETURNING vendor, model`, momo.Named("mac", mac)).Scan(&vendor, &model); err != nil {
			return err
		}
		action := "dismiss"
		if ignore {
			action = "ignore"
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO onboarding_ignored (mac, vendor, model, created_by) VALUES (@mac, @vendor, @model, @created_by)
				ON CONFLICT (mac) DO NOTHING`, momo.Named("mac", mac), momo.Named("vendor", vendor), momo.Named("model", model),
				momo.Named("created_by", actor)); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, actor, action, "onboarding_candidate", mac)
	})
}

// ListIgnored returns the ignored MACs, newest first.
func (s *Store) ListIgnored(ctx context.Context) ([]IgnoredMAC, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mac, vendor, model, created_by, created_at FROM onboarding_ignored ORDER BY created_at DESC, mac`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []IgnoredMAC{}
	for rows.Next() {
		var m IgnoredMAC
		if err := rows.Scan(&m.MAC, &m.Vendor, &m.Model, &m.CreatedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Unignore takes a MAC off the ignore list; ErrNotFound when it is not on it.
func (s *Store) Unignore(ctx context.Context, actor, mac string) error {
	return s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM onboarding_ignored WHERE mac = @mac`, momo.Named("mac", mac))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return sql.ErrNoRows
		}
		return insertAudit(ctx, tx, actor, "unignore", "onboarding_candidate", mac)
	})
}

// AdoptCandidates creates a phone for each input from the queue, removing its
// candidate, in one transaction: all or none. It is the manual phone creation
// (CreatePhone) for each row, so validation, the device, the sealed secrets,
// the boot arming and the redirect registration behave the same. An input
// whose MAC is not queued fails the batch with a validation error naming the
// row.
func (s *Store) AdoptCandidates(ctx context.Context, actor string, in []PhoneInput) ([]CreatedPhone, error) {
	out := make([]CreatedPhone, 0, len(in))
	action, resource := "adopt", "onboarding_candidate"
	if len(in) > 1 {
		action = "adopt-batch"
	}
	err := s.provChange(ctx, actor, action, resource, func(tx *dbTx) (string, bool, error) {
		for i, p := range in {
			res, err := tx.ExecContext(ctx, `DELETE FROM onboarding_candidates WHERE mac = @mac`, momo.Named("mac", p.MAC))
			if err != nil {
				return "", false, err
			}
			if n, err := res.RowsAffected(); err != nil {
				return "", false, err
			} else if n == 0 {
				return "", false, fieldError("items["+strconv.Itoa(i)+"].mac", "is not in the onboarding queue")
			}
			c, err := s.createPhone(ctx, tx, actor, p)
			if err != nil {
				return "", false, fmt.Errorf("row %d: %w", i+1, err)
			}
			if len(in) > 1 {
				if err := insertAudit(ctx, tx, actor, "adopt", resource, p.MAC); err != nil {
					return "", false, err
				}
			}
			if err := insertAudit(ctx, tx, actor, "create", "phone", strconv.FormatInt(c.Phone.ID, 10)); err != nil {
				return "", false, err
			}
			out = append(out, c)
		}
		if len(in) == 1 {
			return in[0].MAC, true, nil
		}
		return strconv.Itoa(len(in)), len(in) > 0, nil
	})
	return out, err
}
