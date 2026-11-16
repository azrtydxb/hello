// momo:fromdb sha256=a77b9d29618f175f2a536fe975e04f7ce589b23f2daabecceb49e6f699cde658
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
	c.Check("crm_user_map_source_check", "(source = ANY (ARRAY['email'::text, 'manual'::text]))")
}
