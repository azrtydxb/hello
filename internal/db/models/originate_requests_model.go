// momo:fromdb sha256=4f1efa0c56ff8de6e053312601263a7cde1978d97ffed3245cb43842fd71d22d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OriginateRequest models the table "originate_requests".
type OriginateRequest struct {
	momo.Model
	ID               string `momo:"pk"`
	IdempotencyKey   string `momo:"default:''::text,dbdefault"`
	ExtensionNumber  string
	Destination      string
	CallerID         string    `momo:"default:''::text,dbdefault"`
	Client           string    `momo:"default:''::text,dbdefault"`
	Actor            string    `momo:"default:''::text,dbdefault"`
	State            string    `momo:"default:'queued'::text,dbdefault"`
	Reason           string    `momo:"default:''::text,dbdefault"`
	CdrCorrelationID string    `momo:"default:''::text,dbdefault"`
	CreatedAt        time.Time `momo:"default:now(),dbdefault,index:originate_requests_created"`
	UpdatedAt        time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OriginateRequest) MomoConfig(c *momo.Config) {
	c.Index("originate_requests_open", &m.UpdatedAt).Where("(state <> ALL (ARRAY['ended'::text, 'failed'::text]))")
}
