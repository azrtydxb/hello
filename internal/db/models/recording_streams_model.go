// momo:fromdb sha256=1d845e49e7f33c48348975c35df811e5365dcd164fcee16506af93b16fd6cb52
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// RecordingStream models the table "recording_streams".
type RecordingStream struct {
	momo.Model
	ID              int64 `momo:"pk,serial"`
	CallCorrelation string
	Part            int32
	Leg             string
	Direction       string `momo:"default:'in'::text,dbdefault"`
	Codec           string
	Container       string
	ObjectPrefix    string
	Segments        int32                       `momo:"default:0,dbdefault"`
	Bytes           int64                       `momo:"default:0,dbdefault"`
	Width           int32                       `momo:"default:0,dbdefault"`
	Height          int32                       `momo:"default:0,dbdefault"`
	OffsetMs        int64                       `momo:"default:0,dbdefault"`
	DurationMs      int64                       `momo:"default:0,dbdefault"`
	SegmentList     types.JSON[json.RawMessage] `momo:"default:'[]'::jsonb,dbdefault"`
	FinalObject     *string
	StartedAt       time.Time `momo:"default:now(),dbdefault"`
	EndedAt         *time.Time
	UpdatedAt       time.Time `momo:"default:now(),dbdefault"`
	State           string    `momo:"default:'recording'::text,dbdefault"`
	Failure         string    `momo:"default:''::text,dbdefault"`
	Node            string    `momo:"default:''::text,dbdefault"`
	ClaimUntil      *time.Time
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RecordingStream) MomoConfig(c *momo.Config) {
	c.Unique("recording_streams_call_correlation_part_leg_key", &m.CallCorrelation, &m.Part, &m.Leg)
	c.Check("recording_streams_bytes_check", "(bytes >= 0)")
	c.Check("recording_streams_call_correlation_check", "((length(call_correlation) >= 1) AND (length(call_correlation) <= 256))")
	c.CheckIn(&m.Codec,
		"h264",
		"vp8",
	)
	c.CheckIn(&m.Container,
		"mp4",
		"webm",
	)
	c.CheckIn(&m.Direction,
		"in",
	)
	c.Check("recording_streams_duration_ms_check", "(duration_ms >= 0)")
	c.Check("recording_streams_failure_check", "(length(failure) <= 64)")
	c.Check("recording_streams_height_check", "((height >= 0) AND (height <= 16384))")
	c.CheckIn(&m.Leg,
		"callee",
		"caller",
	)
	c.Check("recording_streams_node_check", "(length(node) <= 128)")
	c.Check("recording_streams_object_prefix_check", "((length(object_prefix) >= 1) AND (length(object_prefix) <= 512))")
	c.Check("recording_streams_part_check", "(part >= 1)")
	c.Check("recording_streams_segments_check", "(segments >= 0)")
	c.CheckIn(&m.State,
		"ended",
		"failed",
		"finalized",
		"purged",
		"recording",
	)
	c.Check("recording_streams_width_check", "((width >= 0) AND (width <= 16384))")
	c.Index("recording_streams_ended_idx", &m.EndedAt).Where("(state <> 'recording'::text)")
	c.Index("recording_streams_state_idx", &m.State, &m.UpdatedAt)
}
