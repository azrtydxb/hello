package redirect

// Stand-ins for the plan Task 4 functions (contract 5's trailing comment),
// with exactly the contract's signatures, so the control plane (Task 3)
// builds before prov-redirect lands. DELETE THIS FILE when merging
// prov-redirect: its real implementations replace every function here,
// and the duplicate definitions make the merge fail to build until this
// file is gone.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/prov"
)

type unsupported struct{ v prov.Vendor }

func (u unsupported) Vendor() prov.Vendor                                  { return u.v }
func (unsupported) Capabilities() Caps                                     { return Caps{} }
func (unsupported) Check(context.Context) error                            { return ErrUnsupported }
func (unsupported) Register(context.Context, string, string, string) error { return ErrUnsupported }
func (unsupported) Unregister(context.Context, string) error               { return ErrUnsupported }
func (unsupported) Lookup(context.Context, string) (string, bool, error) {
	return "", false, ErrUnsupported
}

// New: see the contract comment in redirect.go.
func New(v prov.Vendor, _ Credentials, _ []byte, _ *http.Client) (Client, error) {
	return unsupported{v}, nil
}

// Deployment: see the contract comment in redirect.go.
func Deployment(config.ProvRedirect) map[prov.Vendor]Credentials { return nil }

// Worker works the redirect queue (Task 4).
type Worker struct{}

// NewWorker: see the contract comment in redirect.go.
func NewWorker(Store, map[prov.Vendor]Credentials, *slog.Logger) *Worker { return &Worker{} }

// Run: see the contract comment in redirect.go.
func (*Worker) Run(ctx context.Context) { <-ctx.Done() }
