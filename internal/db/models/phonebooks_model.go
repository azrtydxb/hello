// momo:fromdb sha256=a4ad2c7da7731d1be2156b17bd485af236107a475bbb6c2ff105be925570682e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Phonebook models the table "phonebooks".
type Phonebook struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	Kind        string
	Name        string
	OwnerUserID *int64
	Version     *int64    `momo:"default:1,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`

	OwnerUser *User `momo:"belongs_to,fk:OwnerUserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Phonebook) MomoConfig(c *momo.Config) {
	c.Check("phonebooks_check", "((kind = 'personal'::text) = (owner_user_id IS NOT NULL))")
	c.CheckIn(&m.Kind,
		"company",
		"extensions",
		"named",
		"personal",
	)
	c.Check("phonebooks_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 100))")
	c.Check("phonebooks_version_check", "(version >= 1)")
	c.IndexExpr("phonebooks_named_name", "lower(name)").Unique().Where("(kind = 'named'::text)")
	c.Unique("phonebooks_one_company", &m.Kind).Where("(kind = 'company'::text)")
	c.Unique("phonebooks_one_extensions", &m.Kind).Where("(kind = 'extensions'::text)")
	c.Unique("phonebooks_one_personal", &m.OwnerUserID).Where("(kind = 'personal'::text)")
}
