// momo:fromdb sha256=6712333cb1ab27abc3a87c84c3bd97379eb9c368314a4c400b91189899c7023e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// CrmConnector models the table "crm_connectors".
type CrmConnector struct {
	momo.Model
	ID              int64 `momo:"pk,serial"`
	Kind            string
	Name            string
	Enabled         *bool                       `momo:"default:true,dbdefault"`
	Config          types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	ClientSecretEnc []byte                      `momo:"nullable"`
	Recordings      bool                        `momo:"default:false,dbdefault"`
	LinkTTLHours    *int32                      `momo:"default:720,dbdefault"`
	LogMissed       *bool                       `momo:"default:true,dbdefault"`
	FallbackOwner   string                      `momo:"default:''::text,dbdefault"`
	CreatedAt       time.Time                   `momo:"default:now(),dbdefault"`
	UpdatedAt       time.Time                   `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CrmConnector) MomoConfig(c *momo.Config) {
	c.Unique("crm_connectors_name_key", &m.Name)
	c.Check("crm_connectors_link_ttl_hours_check", "((link_ttl_hours >= 1) AND (link_ttl_hours <= 8760))")
}
