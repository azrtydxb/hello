// momo:fromdb sha256=9e868ad458ed6f32da6af75c5b11791e6d65795b2375a432d9031925c1a4ab54
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
	User      *User         `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CrmConnection) MomoConfig(c *momo.Config) {
	c.Check("crm_connections_check", "((scope = 'user'::text) = (user_id IS NOT NULL))")
	c.CheckIn(&m.Scope,
		"org",
		"user",
	)
	c.CheckIn(&m.Status,
		"connected",
		"needs_reauth",
		"revoked",
	)
	c.Unique("crm_connections_org", &m.ConnectorID).Where("(scope = 'org'::text)")
	c.Unique("crm_connections_user", &m.ConnectorID, &m.UserID).Where("(scope = 'user'::text)")
}
