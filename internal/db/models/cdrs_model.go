// momo:fromdb sha256=bf0f00a78111eaabe1f3d1c29bb87b2be0ce748385b45ace7196f6719815f6f5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// Cdr models the table "cdrs".
type Cdr struct {
	momo.Model
	ID                   int64 `momo:"pk,serial"`
	CorrelationID        string
	SipCallID            string
	Source               string
	Destination          string
	StartTime            time.Time
	RingTime             *time.Time
	AnswerTime           *time.Time
	EndTime              time.Time
	DurationMs           int64
	BillableMs           int64
	SipNode              string
	MediaMode            string `momo:"default:'direct'::text,dbdefault"`
	FinalStatus          int32
	TerminationSide      string
	FailureReason        string                      `momo:"default:''::text,dbdefault"`
	Direction            string                      `momo:"default:'internal'::text,dbdefault"`
	OriginalDestination  string                      `momo:"default:''::text,dbdefault"`
	RewrittenDestination string                      `momo:"default:''::text,dbdefault"`
	RouteName            string                      `momo:"default:''::text,dbdefault"`
	TrunkName            string                      `momo:"default:''::text,dbdefault"`
	Trace                types.JSON[json.RawMessage] `momo:"default:'[]'::jsonb,dbdefault"`
	RtpPackets           *int64
	RtpLost              *int64
	RtpJitterMs          *float32
	Kind                 string `momo:"default:'call'::text,dbdefault"`
	RingTag              string `momo:"default:''::text,dbdefault"`
	RingRule             string `momo:"default:''::text,dbdefault"`
	CallerCodec          *string
	CalleeCodec          *string
	Origin               string `momo:"default:'phone'::text,dbdefault"`
	OriginClient         string `momo:"default:''::text,dbdefault"`
	AnsweredBy           string `momo:"default:''::text,dbdefault"`
	PeerCallID           string `momo:"default:''::text,dbdefault"`
	Srtp                 string `momo:"default:'none'::text,dbdefault"`
	SrtpKeyex            string `momo:"default:'none'::text,dbdefault"`
	Ice                  bool   `momo:"default:false,dbdefault"`
	Video                bool   `momo:"default:false,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Cdr) MomoConfig(c *momo.Config) {
	c.Unique("cdrs_correlation_id_key", &m.CorrelationID)
	c.CheckIn(&m.Direction,
		"inbound",
		"internal",
		"outbound",
	)
	c.CheckIn(&m.Kind,
		"alarm",
		"call",
		"callback",
		"conference",
		"door",
		"fax",
		"intercom",
		"page",
	)
	c.CheckIn(&m.Origin,
		"api",
		"phone",
	)
	c.CheckIn(&m.Srtp,
		"both",
		"callee",
		"caller",
		"none",
	)
	c.CheckIn(&m.SrtpKeyex,
		"dtls",
		"mixed",
		"none",
		"sdes",
	)
	c.Index("cdrs_peer_call_id_idx", &m.PeerCallID).Where("(peer_call_id <> ''::text)")
}
