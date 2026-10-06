// Package redirect registers phones with their vendor's redirect
// (zero-touch) service, so a phone fresh out of the box asks the vendor's
// cloud where to provision from and is sent to Hello (spec S-11). One
// Client per vendor; only the reconcile worker calls them.
package redirect

import (
	"context"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// ErrUnsupported is returned, without any network call, by a vendor whose
// redirect service Hello cannot drive (Poly, Fanvil): the administrator
// enters the URL in the vendor's portal by hand.
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

// Client is one vendor's redirect service. Credentials are opened inside
// each call and never appear in an error or a log line.
type Client interface {
	Vendor() prov.Vendor
	Capabilities() Caps
	// Check verifies the credentials (the UI's Test button, and on save).
	Check(ctx context.Context) error
	// Register binds mac (and serial, when NeedsSerial) to url, replacing
	// any earlier registration of the MAC.
	Register(ctx context.Context, mac, serial, url string) error
	// Unregister removes the MAC; removing an unknown MAC is not an error.
	Unregister(ctx context.Context, mac string) error
}

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
// returned as the API's redirectStatus.
type Status struct {
	State  State     `json:"state"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}
