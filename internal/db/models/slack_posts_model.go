// momo:fromdb sha256=022fd7da407fe5af3ee2dffa1bba869830c2030a38384a1d953041632329018c
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// SlackPost models the table "slack_posts".
type SlackPost struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	DestinationID int64
	EventID       string
	EventType     string
	Status        string
	Attempts      int32 `momo:"default:0,dbdefault"`
	SeriesAttempt int32 `momo:"default:0,dbdefault"`
	NextAttemptAt *time.Time
	ClaimedUntil  *time.Time
	LastError     string `momo:"default:''::text,dbdefault"`
	DeliveredAt   *time.Time
	Payload       types.JSON[json.RawMessage]
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt     time.Time `momo:"default:now(),dbdefault"`

	Destination *SlackDestination `momo:"belongs_to,fk:DestinationID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *SlackPost) MomoConfig(c *momo.Config) {
	c.Unique("slack_posts_destination_id_event_id_key", &m.DestinationID, &m.EventID)
	c.CheckIn(&m.Status,
		"dead",
		"pending",
		"retrying",
		"succeeded",
	)
	c.Index("slack_posts_due", &m.NextAttemptAt).Where("(status = ANY (ARRAY['pending'::text, 'retrying'::text]))")
	c.Index("slack_posts_list", &m.DestinationID, &m.ID).Desc(&m.ID)
}
