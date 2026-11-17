// momo:fromdb sha256=e863a652063a7fb291e8eb5c2c8182fd7d42196b02efa939f855ccfc9cfbe621
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// CdrClientQuality models the table "cdr_client_quality".
type CdrClientQuality struct {
	momo.Model      `momo:"table:cdr_client_quality"`
	CallID          string `momo:"pk"`
	DeviceID        int64  `momo:"pk"`
	UserID          int64
	Codec           string  `momo:"default:''::text,dbdefault"`
	LossPct         float32 `momo:"default:0,dbdefault"`
	JitterMs        float32 `momo:"default:0,dbdefault"`
	RttMs           float32 `momo:"default:0,dbdefault"`
	CandidateLocal  string  `momo:"default:''::text,dbdefault"`
	CandidateRemote string  `momo:"default:''::text,dbdefault"`
	VideoWidth      *int32
	VideoHeight     *int32
	VideoFps        *int32
	VideoCodec      *string
	DurationMs      int64     `momo:"default:0,dbdefault"`
	ReportedAt      time.Time `momo:"default:now(),dbdefault,index"`

	Device *Device `momo:"belongs_to,fk:DeviceID,on_delete:cascade"`
	User   *User   `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CdrClientQuality) MomoConfig(c *momo.Config) {
	c.Check("cdr_client_quality_call_id_check", "((length(call_id) >= 1) AND (length(call_id) <= 256))")
	c.Check("cdr_client_quality_candidate_local_check", "(length(candidate_local) <= 16)")
	c.Check("cdr_client_quality_candidate_remote_check", "(length(candidate_remote) <= 16)")
	c.Check("cdr_client_quality_codec_check", "(length(codec) <= 32)")
	c.Check("cdr_client_quality_duration_ms_check", "(duration_ms >= 0)")
	c.Check("cdr_client_quality_jitter_ms_check", "(jitter_ms >= (0)::double precision)")
	c.Check("cdr_client_quality_loss_pct_check", "((loss_pct >= (0)::double precision) AND (loss_pct <= (100)::double precision))")
	c.Check("cdr_client_quality_rtt_ms_check", "(rtt_ms >= (0)::double precision)")
	c.Check("cdr_client_quality_video_codec_check", "(length(video_codec) <= 32)")
	c.Check("cdr_client_quality_video_fps_check", "((video_fps >= 0) AND (video_fps <= 1000))")
	c.Check("cdr_client_quality_video_height_check", "((video_height >= 0) AND (video_height <= 16384))")
	c.Check("cdr_client_quality_video_width_check", "((video_width >= 0) AND (video_width <= 16384))")
}
