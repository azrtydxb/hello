// momo:fromdb sha256=247d0e796ce777aaa17212aca03558ace977d9182da01ecab61a28e65706abcd
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// CrmCallLog models the table "crm_call_logs".
type CrmCallLog struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	ConnectorID    int64
	CallID         string
	Direction      string `momo:"default:''::text,dbdefault"`
	Status         string
	ExternalID     string    `momo:"default:''::text,dbdefault"`
	Attempts       int32     `momo:"default:0,dbdefault"`
	NextAttemptAt  time.Time `momo:"default:now(),dbdefault"`
	LastError      string    `momo:"default:''::text,dbdefault"`
	Payload        *types.JSON[json.RawMessage]
	EventType      string `momo:"default:''::text,dbdefault"`
	OwnerExtension string `momo:"default:''::text,dbdefault"`
	RecordingID    *int64
	LinkExpiresAt  *time.Time
	CreatedAt      time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt      time.Time `momo:"default:now(),dbdefault"`
	State          string    `momo:"default:''::text,dbdefault"`

	Connector *CrmConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CrmCallLog) MomoConfig(c *momo.Config) {
	c.Unique("crm_call_logs_connector_id_call_id_key", &m.ConnectorID, &m.CallID)
	c.Check("crm_call_logs_status_check", "(status = ANY (ARRAY['waiting'::text, 'pending'::text, 'logged'::text, 'update'::text, 'dead'::text, 'skipped'::text]))")
	c.Index("crm_call_logs_due", &m.NextAttemptAt).Where("(status = ANY (ARRAY['pending'::text, 'update'::text]))")
	c.Index("crm_call_logs_renew", &m.LinkExpiresAt).Where("(link_expires_at IS NOT NULL)")
}
