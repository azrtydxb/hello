package prov

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"time"
)

// Errors a Store returns for the handler to tell apart. Any other error is
// an outage: the handler answers 503 with Retry-After.
var (
	// ErrNotFound: no phone holds the token or MAC, or no firmware has the
	// name.
	ErrNotFound = errors.New("prov: not found")
	// ErrNoTemplate: no template matches the phone (fetch result
	// no_template).
	ErrNoTemplate = errors.New("prov: no template matches the phone")
	// ErrSealed: a sealed secret, token or admin password does not open
	// under HELLO_SECRET_KEY (fetch result render_error). The error never
	// carries the sealed or opened value.
	ErrSealed = errors.New("prov: a sealed value does not open")
)

// PhoneRecord is what the handler needs to authorise a request for a phone.
type PhoneRecord struct {
	ID     int64
	MAC    string // lowercase, no separators
	Vendor Vendor
	Model  string
	// Allowlisted: the phone is enabled and bound to an enabled device of
	// an extension (spec S-6).
	Allowlisted bool
	// ViaPrevious: PhoneByToken matched the in-grace previous token, not
	// the current one.
	ViaPrevious bool
	// HasPrevious: a previous token is still in grace, so the current one
	// has not been fetched with yet; the first fetch with the current token
	// calls PromoteToken.
	HasPrevious bool
	BootArmed   bool
}

// FetchState is what a served per-device fetch records on the phone.
type FetchState struct {
	At           time.Time
	IP           netip.Addr
	UserAgent    string // at most 256 bytes, cut on a rune boundary
	File         string // the requested file name (never the token)
	FirmwareSeen string // parsed from the User-Agent; empty keeps the old value
	// UAMismatch sets the phone's ua_mismatch flag (sticky until an
	// administrator clears it); false leaves it as it is.
	UAMismatch bool
}

// Store is the provisioning handler's view of hello-control's database.
// internal/store implements it; tests use an in-memory fake.
type Store interface {
	// PhoneByToken finds the phone whose current token, or whose previous
	// token still in grace, hashes to hash, and reports which matched.
	// ErrNotFound when none does.
	PhoneByToken(ctx context.Context, hash []byte) (PhoneRecord, error)

	// MarkFetched records a served per-device fetch: first_fetch_at when
	// unset, the last_fetch_* fields, firmware_seen and ua_mismatch as
	// FetchState says. It also disarms the boot hand-off (boot_armed =
	// false), because a phone that fetched over HTTPS with its token never
	// needs it (spec S-10).
	MarkFetched(ctx context.Context, phoneID int64, st FetchState) error

	// PromoteToken ends the previous token's grace at once; called on the
	// first fetch with the current token while HasPrevious.
	PromoteToken(ctx context.Context, phoneID int64) error

	// FlagTokenExposed sets token_exposed after a per-device request
	// arrived over plain HTTP.
	FlagTokenExposed(ctx context.Context, phoneID int64) error

	// ClaimBoot is the trust-on-first-use hand-off for the phone with this
	// MAC (spec S-10), atomic under concurrent callers:
	//   - armed and allowlisted: disarms it in one conditional update and
	//     returns claimed = true; exactly one concurrent caller wins
	//   - disarmed: sets boot_reclaimed and returns claimed = false
	//   - armed but not allowlisted: changes nothing, claimed = false
	// ErrNotFound when no phone has the MAC. The handler checks
	// HELLO_PROV_BOOT_CIDRS before calling, so a denied source never
	// disarms.
	ClaimBoot(ctx context.Context, mac string) (rec PhoneRecord, claimed bool, err error)

	// RenderInputs returns the render data with every secret opened, the
	// current token in Prov.URL (also for a previous-token fetch, so the
	// phone moves itself over) and the resolved template. ErrNoTemplate,
	// ErrSealed or ErrNotFound as above.
	RenderInputs(ctx context.Context, phoneID int64) (RenderData, Template, error)

	// FirmwareByName finds a vendor's uploaded firmware by file name.
	// ErrNotFound when there is none.
	FirmwareByName(ctx context.Context, v Vendor, filename string) (Firmware, error)

	// InsertFetches writes audit rows in one batch.
	InsertFetches(ctx context.Context, rows []FetchRecord) error
}

// Opener opens a firmware object (bucket hello-firmware) for streaming
// with Range support.
type Opener interface {
	OpenFirmware(ctx context.Context, objectKey string) (io.ReadSeekCloser, error)
}
