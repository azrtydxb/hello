// momo:fromdb sha256=3b56086df552ec37f80c735aef70dc0341aec8e354876ef982d23f20aef7d21d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// CdrRtcpxr models the table "cdr_rtcpxr".
type CdrRtcpxr struct {
	momo.Model `momo:"table:cdr_rtcpxr"`
	CallID     string `momo:"pk"`
	Device     string `momo:"pk"`
	Extension  string `momo:"default:''::text,dbdefault"`
	Final      bool   `momo:"default:false,dbdefault"`
	MosLq      *float32
	MosCq      *float32
	JitterMs   *float32
	LossPct    *float32
	DiscardPct *float32
	RttMs      *float32
	ReportedAt time.Time `momo:"default:now(),dbdefault,index"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CdrRtcpxr) MomoConfig(c *momo.Config) {
	c.Check("cdr_rtcpxr_call_id_check", "((length(call_id) >= 1) AND (length(call_id) <= 256))")
	c.Check("cdr_rtcpxr_discard_pct_check", "((discard_pct >= (0)::double precision) AND (discard_pct <= (100)::double precision))")
	c.Check("cdr_rtcpxr_jitter_ms_check", "(jitter_ms >= (0)::double precision)")
	c.Check("cdr_rtcpxr_loss_pct_check", "((loss_pct >= (0)::double precision) AND (loss_pct <= (100)::double precision))")
	c.Check("cdr_rtcpxr_mos_cq_check", "((mos_cq >= (0)::double precision) AND (mos_cq <= (5)::double precision))")
	c.Check("cdr_rtcpxr_mos_lq_check", "((mos_lq >= (0)::double precision) AND (mos_lq <= (5)::double precision))")
	c.Check("cdr_rtcpxr_rtt_ms_check", "(rtt_ms >= (0)::double precision)")
}
