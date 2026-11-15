// Package dirstore connects the directory engine's range auto-provisioning
// (spec integrations I-11) to the configuration store, so an extension a
// directory sync creates follows the same numbering rules and the same
// whole-configuration check as one an administrator creates.
package dirstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// Provisioner is a directory.Provisioner on the configuration store.
type Provisioner struct {
	Store *store.Store
	// Realm is the SIP domain the device's digest values are computed for.
	Realm string
	// Check is the whole-configuration check; nil compiles the routing
	// configuration (routing.Compile), the check the API runs.
	Check store.Check
}

var _ directory.Provisioner = (*Provisioner)(nil)

func (p *Provisioner) check() store.Check {
	if p.Check != nil {
		return p.Check
	}
	return func(cfg routing.Config) []routing.FieldError {
		_, errs := routing.Compile(cfg)
		return errs
	}
}

// Taken implements directory.Provisioner.
func (p *Provisioner) Taken(ctx context.Context, tx *sql.Tx, lo, hi string) (map[string]bool, error) {
	return p.Store.DialNumbersTaken(ctx, tx, lo, hi)
}

// Provision implements directory.Provisioner. A range with no free number is
// directory.ErrRangeExhausted; a change the configuration check refuses is a
// directory.ValidationError naming the failing fields.
func (p *Provisioner) Provision(ctx context.Context, tx *sql.Tx, actor string, req directory.ProvisionRequest) (directory.ProvisionResult, error) {
	res, err := p.Store.ProvisionDirectoryExtension(ctx, tx, actor, store.DirectoryExtension{
		ConnectorID: req.ConnectorID, Preferred: req.Preferred, RangeStart: req.RangeStart, RangeEnd: req.RangeEnd,
		Name: req.Name, Voicemail: req.Voicemail, Email: req.Email, Device: req.Device, Realm: p.Realm,
	}, p.check())
	var ve *store.ValidationError
	switch {
	case errors.Is(err, store.ErrNoFreeNumber):
		return directory.ProvisionResult{}, directory.ErrRangeExhausted
	case errors.As(err, &ve):
		out := &directory.ValidationError{}
		for _, f := range ve.Fields {
			out.Fields = append(out.Fields, directory.FieldError{Path: f.Path, Message: f.Message})
		}
		return directory.ProvisionResult{}, out
	case err != nil:
		return directory.ProvisionResult{}, err
	}
	return directory.ProvisionResult{ExtensionID: res.ExtensionID, Number: res.Number, DeviceID: res.DeviceID}, nil
}
