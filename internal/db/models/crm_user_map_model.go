// momo:fromdb sha256=e1291ed7672053b099bf25c578da515259fa9006b09636897a838fdfedfc9b2b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// CrmUserMap models the table "crm_user_map".
type CrmUserMap struct {
	momo.Model     `momo:"table:crm_user_map"`
	ConnectorID    int64 `momo:"pk"`
	UserID         int64 `momo:"pk"`
	ExternalUserID string
	Source         string `momo:"default:'email'::text,dbdefault"`

	Connector *CrmConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CrmUserMap) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Source,
		"email",
		"manual",
	)
}
