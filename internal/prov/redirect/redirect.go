// Package redirect registers phones with their vendor's redirect
// (zero-touch) service, so a phone fresh out of the box asks the vendor's
// cloud where to provision from and is sent to Hello (spec S-11). One
// Client per vendor; the API's credential test and the reconcile worker
// are its only callers.
package redirect

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// ErrUnsupported is returned, without any network call, by a vendor whose
// redirect service Hello cannot drive (Poly, Fanvil), and by Lookup where
// a vendor's API cannot read a registration back. For an unsupported
// vendor the administrator enters the URL in the vendor's portal by hand.
var ErrUnsupported = errors.New("redirect: not supported for this vendor; register the phone in the vendor's portal")

// Caps states what a vendor's client can do.
type Caps struct {
	// RegistersURL: Register binds the MAC to the phone's own URL. False
	// for Grandstream GDMS, which only adds the device to a site whose
	// provisioning server the administrator sets once.
	RegistersURL bool
	// NeedsSerial: Register requires the phone's serial number.
	NeedsSerial bool
	// Supported: false means every call returns ErrUnsupported.
	Supported bool
}

// Client is one vendor's redirect service, built with its credentials.
// Credentials are used only inside each call and never appear in an error
// or a log line.
type Client interface {
	Vendor() prov.Vendor
	Capabilities() Caps
	// Check verifies the credentials (on save and the UI's Test button).
	Check(ctx context.Context) error
	// Register binds mac (and serial, when NeedsSerial) to url, replacing
	// any earlier registration of the MAC.
	Register(ctx context.Context, mac, serial, url string) error
	// Unregister removes the MAC; removing an unknown MAC is not an error.
	Unregister(ctx context.Context, mac string) error
	// Lookup reads back the URL the MAC is registered with, for the daily
	// drift check and the live test's restore. found is false when the
	// vendor holds no registration; ErrUnsupported when its API cannot
	// tell.
	Lookup(ctx context.Context, mac string) (url string, found bool, err error)
}

// Credentials are one vendor's redirect credentials, keyed by the names of
// the hello-prov-redirect secret (spec S-11), e.g. snomSrapsAccessKeyId,
// yealinkRpsAccessSecret, gdmsSiteId. Stored as json.Marshal(c) sealed
// with prov.RedirectAAD; that is the only encoding that shows the values.
// fmt and slog (text and JSON handlers) print a placeholder, also for an
// Account logged as a whole; a struct of your own holding them is not
// covered, so never log one.
type Credentials map[string]string

// String keeps credentials out of %v and %s.
func (Credentials) String() string { return "[redacted]" }

// GoString keeps credentials out of %#v.
func (Credentials) GoString() string { return "[redacted]" }

// LogValue keeps credentials out of slog, whose JSON handler marshals
// values instead of calling String.
func (Credentials) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// State is a phone's redirect registration state.
type State string

// The states shown in the inventory (spec S-11).
const (
	StateNotConfigured State = "not_configured" // no credentials for the vendor
	StateManual        State = "manual"         // the vendor is not Supported
	StatePending       State = "pending"        // queued or retrying
	StateRegistered    State = "registered"
	StateFailed        State = "failed" // Reason says why ("drift" after a reconcile mismatch)
)

// Status is a phone's redirect status, stored as phones.redirect_status and
// returned as the API's redirectStatus. At is omitted until something
// happened (the column default has no time).
type Status struct {
	State  State     `json:"state"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// Op is a queued redirect operation.
type Op string

// The operations.
const (
	OpRegister   Op = "register"
	OpUnregister Op = "unregister"
)

// Job is one prov_redirect_jobs row.
type Job struct {
	// Seq identifies this queued operation; a replacement of the same
	// vendor and MAC takes a new one, so FinishJob and RetryJob match on
	// it and never touch a job queued after this one was read.
	Seq           int64
	Vendor        prov.Vendor
	MAC           string
	Op            Op
	Attempts      int
	FirstQueuedAt time.Time // the 24-hour give-up counts from here
}

// Target is what a register needs: the phone's MAC, serial and its own
// provisioning URL with the current token (opened by the store).
type Target struct {
	MAC    string
	Serial string
	URL    string
}

// Account is a vendor's stored redirect account, its credentials opened.
type Account struct {
	Vendor      prov.Vendor
	Enabled     bool
	Credentials Credentials // nil when none are stored
	Settings    []byte      // the settings JSON object, vendor-specific
}

// LogValue logs an account without its credentials (slog does not consult
// a nested field's LogValue).
func (a Account) LogValue() slog.Value {
	return slog.GroupValue(slog.String("vendor", string(a.Vendor)), slog.Bool("enabled", a.Enabled),
		slog.Bool("hasCredentials", a.Credentials != nil))
}

// Store is the worker's view of hello-control's database. internal/store
// implements it; tests use an in-memory fake.
type Store interface {
	// DueJobs returns up to limit jobs whose next attempt is due.
	DueJobs(ctx context.Context, limit int) ([]Job, error)
	// Target returns the phone with this vendor and MAC; prov.ErrNotFound
	// when it is gone or now has another vendor (a stale job for the old
	// vendor must not register it there).
	Target(ctx context.Context, v prov.Vendor, mac string) (Target, error)
	// Account returns the vendor's stored account; prov.ErrNotFound when
	// there is none.
	Account(ctx context.Context, v prov.Vendor) (Account, error)
	// FinishJob removes j and sets the phone's status (when the phone
	// exists), only while the row still has j.Seq: a replacement queued
	// since j was read (a rotation's new URL) stays queued and its status
	// stands.
	FinishJob(ctx context.Context, j Job, st Status) error
	// RetryJob counts an attempt, schedules the next one at next and sets
	// the phone's status, only while the row still has j.Seq.
	RetryJob(ctx context.Context, j Job, next time.Time, st Status) error
	// Registered returns the vendor's phones whose status is registered,
	// for the daily drift check.
	Registered(ctx context.Context, v prov.Vendor) ([]Target, error)
	// SetStatus sets the status of the phone with this MAC.
	SetStatus(ctx context.Context, mac string, st Status) error
}

// Functions this package provides (plan Task 4), with exactly these
// signatures:
//
//	New(v prov.Vendor, creds Credentials, settings []byte, hc *http.Client) (Client, error)
//	    the vendor's client; Poly and Fanvil return one whose calls all
//	    return ErrUnsupported
//	Deployment(c config.ProvRedirect) map[prov.Vendor]Credentials
//	    the credentials set by the deployment, by vendor; they take
//	    precedence over stored ones (the API's fromDeployment)
//	NewWorker(s Store, deployment map[prov.Vendor]Credentials, log *slog.Logger) *Worker
//	(*Worker).Run(ctx context.Context)
//	    processes due jobs and runs the daily drift check until ctx ends;
//	    hello-control runs it only while it holds the redirect lease, so
//	    one replica works the queue
