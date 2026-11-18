// momo:fromdb sha256=e7649b6f30a1ffd2a481b36db6b2cde338a02c4f6a12180bb35462212b2ad1b8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// User models the table "users".
type User struct {
	momo.Model
	ID                 int64 `momo:"pk,serial"`
	Username           string
	PasswordHash       string
	CreatedAt          time.Time `momo:"default:now(),dbdefault"`
	Role               string    `momo:"default:'viewer'::text,dbdefault"`
	ExtensionID        *int64
	DisplayName        string `momo:"default:''::text,dbdefault"`
	Email              string `momo:"default:''::text,dbdefault"`
	AuthMethod         string `momo:"default:'local'::text,dbdefault"`
	Enabled            *bool  `momo:"default:true,dbdefault"`
	MustChangePassword bool   `momo:"default:false,dbdefault"`
	VMEmailNotify      *bool  `momo:"default:true,dbdefault"`
	VMEmailAttach      *bool  `momo:"default:true,dbdefault"`
	VMEmailAfter       string `momo:"default:'none'::text,dbdefault"`
	TotpSecretEnc      []byte `momo:"nullable"`
	TotpEnabledAt      *time.Time
	TotpLastStep       int64 `momo:"default:0,dbdefault"`
	TotpRequired       bool  `momo:"default:false,dbdefault"`
	RingWeb            *bool `momo:"default:true,dbdefault"`
	RingMobile         *bool `momo:"default:true,dbdefault"`
	DeskphoneDelivery  bool  `momo:"default:false,dbdefault"`
	SourceConnectorID  *int64
	ExternalID         *string
	ManagedFields      []string `momo:"default:'{}'::text[],dbdefault"`
	DisabledAt         *time.Time
	AbsentRuns         int32 `momo:"default:0,dbdefault"`
	ProvisionedNumber  *string
	BreakGlass         bool `momo:"default:false,dbdefault"`

	Extension       *Extension          `momo:"belongs_to,fk:ExtensionID,on_delete:set_null"`
	SourceConnector *DirectoryConnector `momo:"belongs_to,fk:SourceConnectorID"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *User) MomoConfig(c *momo.Config) {
	c.Unique("users_extension_id_key", &m.ExtensionID)
	c.Unique("users_username_key", &m.Username)
	c.CheckIn(&m.AuthMethod,
		"local",
	)
	c.Check("users_provisioned_number_check", "((provisioned_number IS NULL) OR (provisioned_number ~ '^[0-9]{2,10}$'::text))")
	c.CheckIn(&m.Role,
		"admin",
		"operator",
		"user",
		"viewer",
	)
	c.Check("users_source_pair", "((source_connector_id IS NULL) = (external_id IS NULL))")
	c.CheckIn(&m.VMEmailAfter,
		"delete",
		"mark_read",
		"none",
	)
	c.Unique("users_source_external", &m.SourceConnectorID, &m.ExternalID).Where("(source_connector_id IS NOT NULL)")
}
