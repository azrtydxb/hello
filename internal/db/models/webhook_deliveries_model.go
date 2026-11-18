// momo:fromdb sha256=239d5cfd7279c345681604bce8d3201f01e411f7f7cada6e271c45924d14890f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// WebhookDelivery models the table "webhook_deliveries".
type WebhookDelivery struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	EndpointID    int64
	EventID       string `momo:"index:webhook_deliveries_event"`
	EventType     string
	Status        string `momo:"default:'pending'::text,dbdefault"`
	Attempt       int32  `momo:"default:0,dbdefault"`
	SeriesAttempt int32  `momo:"default:0,dbdefault"`
	NextAttemptAt time.Time
	ClaimedUntil  *time.Time
	LastStatus    *int32
	LastError     string `momo:"default:''::text,dbdefault"`
	LastLatencyMs *int32
	DeliveredAt   *time.Time
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt     time.Time `momo:"default:now(),dbdefault"`

	Endpoint *WebhookEndpoint `momo:"belongs_to,fk:EndpointID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *WebhookDelivery) MomoConfig(c *momo.Config) {
	c.Unique("webhook_deliveries_endpoint_id_event_id_key", &m.EndpointID, &m.EventID)
	c.CheckIn(&m.Status,
		"dead",
		"pending",
		"retrying",
		"succeeded",
	)
	c.Index("webhook_deliveries_due", &m.Status, &m.NextAttemptAt)
	c.Index("webhook_deliveries_endpoint", &m.EndpointID, &m.ID).Desc(&m.ID)
}
