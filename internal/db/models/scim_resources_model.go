// momo:fromdb sha256=f67c2387a7032038f2851645dcd29ec4b6e19a993779afd17e13fb965578b858
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// ScimResource models the table "scim_resources".
type ScimResource struct {
	momo.Model
	ID           string `momo:"pk"`
	ConnectorID  int64
	ResourceType string
	Name         string
	ExternalID   *string
	Data         types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Version      *int64                      `momo:"default:1,dbdefault"`
	DeletedAt    *time.Time
	CreatedAt    time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt    time.Time `momo:"default:now(),dbdefault"`

	Connector *DirectoryConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ScimResource) MomoConfig(c *momo.Config) {
	c.Check("scim_resources_external_id_check", "((external_id IS NULL) OR ((char_length(external_id) >= 1) AND (char_length(external_id) <= 512)))")
	c.Check("scim_resources_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 512))")
	c.CheckIn(&m.ResourceType,
		"Group",
		"User",
	)
	c.Unique("scim_resources_external", &m.ConnectorID, &m.ResourceType, &m.ExternalID).Where("((external_id IS NOT NULL) AND (deleted_at IS NULL))")
	c.Index("scim_resources_list", &m.ConnectorID, &m.ResourceType, &m.CreatedAt, &m.ID)
	c.IndexExpr("scim_resources_name", "connector_id", "resource_type", "lower(name)").Unique().Where("(deleted_at IS NULL)")
}
