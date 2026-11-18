// momo:fromdb sha256=071fd0c4bd009fcdb64c278a8430ee59d71ffffe5f3686d4a7bff8006b868805
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// WebhookAttempt models the table "webhook_attempts".
type WebhookAttempt struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	DeliveryID  int64
	Attempt     int32
	DeliveryRef string
	At          time.Time
	HTTPStatus  *int32
	Error       string `momo:"default:''::text,dbdefault"`
	LatencyMs   int32  `momo:"default:0,dbdefault"`

	Delivery *WebhookDelivery `momo:"belongs_to,fk:DeliveryID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *WebhookAttempt) MomoConfig(c *momo.Config) {
	c.Index("webhook_attempts_delivery", &m.DeliveryID, &m.Attempt)
}
