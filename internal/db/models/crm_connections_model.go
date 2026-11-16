// momo:fromdb sha256=694305941535abfccaaa3c514cfd30943465af2ab4405694fe5c9c1bf1517319
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// CrmConnection models the table "crm_connections".
type CrmConnection struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	ConnectorID       int64
	Scope             string
	UserID            *int64
	AccessEnc         []byte `momo:"nullable"`
	RefreshEnc        []byte `momo:"nullable"`
	ExpiresAt         *time.Time
	GrantedScopes     string    `momo:"default:''::text,dbdefault"`
	ExternalAccount   string    `momo:"default:''::text,dbdefault"`
	Status            string    `momo:"default:'connected'::text,dbdefault"`
	LastError         string    `momo:"default:''::text,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt         time.Time `momo:"default:now(),dbdefault"`
	CalendarDnd       bool      `momo:"default:false,dbdefault"`
	CalendarActive    bool      `momo:"default:false,dbdefault"`
	CalendarBusyUntil *time.Time
	CalendarCheckedAt *time.Time
	CalendarError     string `momo:"default:''::text,dbdefault"`

	Connector *CrmConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CrmConnection) MomoConfig(c *momo.Config) {
	c.Check("crm_connections_check", "((scope = 'user'::text) = (user_id IS NOT NULL))")
	c.Check("crm_connections_scope_check", "(scope = ANY (ARRAY['org'::text, 'user'::text]))")
	c.Check("crm_connections_status_check", "(status = ANY (ARRAY['connected'::text, 'needs_reauth'::text, 'revoked'::text]))")
	c.Unique("crm_connections_org", &m.ConnectorID).Where("(scope = 'org'::text)")
	c.Unique("crm_connections_user", &m.ConnectorID, &m.UserID).Where("(scope = 'user'::text)")
}
