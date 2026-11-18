// momo:fromdb sha256=526acb91d6a9e14bf25dfd3a801194dc647afb322e28253865059b7ec7e8f5d7
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// SlackDestination models the table "slack_destinations".
type SlackDestination struct {
	momo.Model
	ID                  int64 `momo:"pk,serial"`
	Name                string
	Mode                string
	SecretEnc           []byte
	Channel             string   `momo:"default:''::text,dbdefault"`
	Events              []string `momo:"dbdefault"`
	Extensions          []string `momo:"default:'{}'::text[],dbdefault"`
	Enabled             *bool    `momo:"default:true,dbdefault"`
	DisabledReason      string   `momo:"default:''::text,dbdefault"`
	ConsecutiveFailures int32    `momo:"default:0,dbdefault"`
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	LastError           string    `momo:"default:''::text,dbdefault"`
	CreatedBy           string    `momo:"default:''::text,dbdefault"`
	CreatedAt           time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt           time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *SlackDestination) MomoConfig(c *momo.Config) {
	c.Default(&m.Events, "'{call.missed,voicemail.received}'::text[]")
	c.Unique("slack_destinations_name_key", &m.Name)
	c.CheckIn(&m.Mode,
		"bot",
		"webhook",
	)
}
