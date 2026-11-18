// momo:fromdb sha256=ae8b8593d6087be1a4440955f4743ee0913b755bb1707e072ee18d49e5ed4246
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Queue models the table "queues".
type Queue struct {
	momo.Model
	ID                        int64 `momo:"pk,serial"`
	Name                      string
	Number                    string `momo:"default:''::text,dbdefault"`
	Enabled                   *bool  `momo:"default:true,dbdefault"`
	Strategy                  string `momo:"default:'ring_all'::text,dbdefault"`
	AgentTimeout              *int32 `momo:"default:20,dbdefault"`
	RetryDelay                *int32 `momo:"default:5,dbdefault"`
	WrapUp                    int32  `momo:"default:0,dbdefault"`
	MaxCallers                int32  `momo:"default:0,dbdefault"`
	MaxWait                   *int32 `momo:"default:600,dbdefault"`
	JoinEmpty                 *bool  `momo:"default:true,dbdefault"`
	LeaveEmpty                bool   `momo:"default:false,dbdefault"`
	TimeoutKind               string `momo:"default:'hangup'::text,dbdefault"`
	TimeoutTarget             string `momo:"default:''::text,dbdefault"`
	FullKind                  string `momo:"default:'hangup'::text,dbdefault"`
	FullTarget                string `momo:"default:''::text,dbdefault"`
	NoAgentsKind              string `momo:"default:'hangup'::text,dbdefault"`
	NoAgentsTarget            string `momo:"default:''::text,dbdefault"`
	MohClassID                *int64
	AnnounceFrequency         int32 `momo:"default:0,dbdefault"`
	AnnouncePosition          bool  `momo:"default:false,dbdefault"`
	AnnounceWait              bool  `momo:"default:false,dbdefault"`
	PeriodicPromptID          *int64
	PositionPromptID          *int64
	WaitPromptID              *int64
	MinutesPromptID           *int64
	DigitsPrefix              string `momo:"default:''::text,dbdefault"`
	BreakoutKey               string `momo:"default:''::text,dbdefault"`
	BreakoutPromptID          *int64
	BreakoutVoicemail         string    `momo:"default:''::text,dbdefault"`
	CreatedAt                 time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt                 time.Time `momo:"default:now(),dbdefault"`
	CallbackEnabled           bool      `momo:"default:false,dbdefault"`
	CallbackOfferAfterSeconds *int32    `momo:"default:120,dbdefault"`
	CallbackDigit             string    `momo:"default:'2'::text,dbdefault"`
	PromptLanguage            *string
	TextEnabled               bool   `momo:"default:false,dbdefault"`
	TextCapacity              *int32 `momo:"default:3,dbdefault"`
	TextReassignSeconds       *int32 `momo:"default:300,dbdefault"`
	TextRequiresIdleVoice     bool   `momo:"default:false,dbdefault"`

	BreakoutPrompt *Announcement `momo:"belongs_to,fk:BreakoutPromptID,on_delete:restrict"`
	MinutesPrompt  *Announcement `momo:"belongs_to,fk:MinutesPromptID,on_delete:restrict"`
	MohClass       *MohClass     `momo:"belongs_to,fk:MohClassID,on_delete:set_null"`
	PeriodicPrompt *Announcement `momo:"belongs_to,fk:PeriodicPromptID,on_delete:restrict"`
	PositionPrompt *Announcement `momo:"belongs_to,fk:PositionPromptID,on_delete:restrict"`
	WaitPrompt     *Announcement `momo:"belongs_to,fk:WaitPromptID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Queue) MomoConfig(c *momo.Config) {
	c.Unique("queues_name_key", &m.Name)
	c.Check("queues_agent_timeout_check", "((agent_timeout >= 5) AND (agent_timeout <= 120))")
	c.Check("queues_announce_frequency_check", "((announce_frequency >= 0) AND (announce_frequency <= 600))")
	c.Check("queues_breakout_key_check", "(breakout_key ~ '^[0-9*#]?$'::text)")
	c.Check("queues_breakout_voicemail_check", "(breakout_voicemail ~ '^([0-9]{2,10})?$'::text)")
	c.Check("queues_callback_digit_check", "(callback_digit ~ '^[0-9*#]$'::text)")
	c.Check("queues_callback_offer_after_seconds_check", "((callback_offer_after_seconds >= 0) AND (callback_offer_after_seconds <= 3600))")
	c.Check("queues_digits_prefix_check", "(digits_prefix ~ '^[A-Za-z0-9._-]{0,32}$'::text)")
	c.CheckIn(&m.FullKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("queues_max_callers_check", "((max_callers >= 0) AND (max_callers <= 1000))")
	c.Check("queues_max_wait_check", "((max_wait >= 0) AND (max_wait <= 86400))")
	c.Check("queues_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.CheckIn(&m.NoAgentsKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("queues_number_check", "(number ~ '^([0-9]{2,16})?$'::text)")
	c.Check("queues_retry_delay_check", "((retry_delay >= 0) AND (retry_delay <= 120))")
	c.CheckIn(&m.Strategy,
		"fewest_calls",
		"linear",
		"longest_idle",
		"random",
		"ring_all",
		"round_robin",
	)
	c.Check("queues_text_capacity_check", "((text_capacity >= 1) AND (text_capacity <= 50))")
	c.Check("queues_text_reassign_seconds_check", "((text_reassign_seconds >= 0) AND (text_reassign_seconds <= 86400))")
	c.CheckIn(&m.TimeoutKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("queues_wrap_up_check", "((wrap_up >= 0) AND (wrap_up <= 600))")
	c.Unique("queues_number", &m.Number).Where("(number <> ''::text)")
}
