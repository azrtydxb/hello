// momo:fromdb sha256=0d9e4b5c3257501abff2c69d5eac0790f29c3abb2bf23006c61ac6a1d2056d3b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// FaxDestination models the table "fax_destinations".
type FaxDestination struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	Name        string
	ExtensionID int64
	Email       string    `momo:"default:''::text,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *FaxDestination) MomoConfig(c *momo.Config) {
	c.Unique("fax_destinations_name_key", &m.Name)
	c.Check("fax_destinations_email_check", "(length(email) <= 254)")
	c.Check("fax_destinations_name_check", "(name ~ '^[A-Za-z0-9._-]{1,40}$'::text)")
}
