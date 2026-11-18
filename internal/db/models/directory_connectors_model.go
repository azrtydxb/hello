// momo:fromdb sha256=32c738cfeddba3f783086f0d121855cb5bb0a5e5e9423c9b1e28445a0f02b6a2
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// DirectoryConnector models the table "directory_connectors".
type DirectoryConnector struct {
	momo.Model
	ID                  int64 `momo:"pk,serial"`
	Kind                string
	Name                string
	Enabled             bool                        `momo:"default:false,dbdefault"`
	Mode                string                      `momo:"default:'authoritative'::text,dbdefault"`
	ScheduleMinutes     *int32                      `momo:"default:60,dbdefault"`
	Config              types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Mappings            types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	GroupRules          types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Options             types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	AutoProvision       types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	CredentialEnc       []byte                      `momo:"nullable"`
	CredentialFp        string                      `momo:"default:''::text,dbdefault"`
	SyncMark            types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	SyncFingerprint     string                      `momo:"default:''::text,dbdefault"`
	LastFullRunAt       *time.Time
	Status              string `momo:"default:'new'::text,dbdefault"`
	LastRunAt           *time.Time
	LastSuccessAt       *time.Time
	LastErrorCode       string    `momo:"default:''::text,dbdefault"`
	LastErrorDetail     string    `momo:"default:''::text,dbdefault"`
	ConsecutiveFailures int32     `momo:"default:0,dbdefault"`
	CreatedAt           time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt           time.Time `momo:"default:now(),dbdefault"`
	PausedAt            *time.Time
	GuardSince          *time.Time
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *DirectoryConnector) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Kind,
		"entra",
		"google",
		"ldap",
		"scim",
	)
	c.CheckIn(&m.Mode,
		"authoritative",
		"create_only",
		"link_only",
	)
	c.Check("directory_connectors_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 100))")
	c.Check("directory_connectors_schedule_minutes_check", "((schedule_minutes = 0) OR ((schedule_minutes >= 5) AND (schedule_minutes <= 10080)))")
	c.CheckIn(&m.Status,
		"failing",
		"new",
		"ok",
	)
	c.IndexExpr("directory_connectors_name", "lower(name)").Unique()
}
