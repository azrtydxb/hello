// momo:fromdb sha256=1f4cde46470c3fec53d76395d58c73e764ae82054814dfc66d2a576c71901067
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// IvrMenu models the table "ivr_menus".
type IvrMenu struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	Name              string
	Number            string `momo:"default:''::text,dbdefault"`
	Enabled           *bool  `momo:"default:true,dbdefault"`
	GreetingID        int64
	InvalidPromptID   *int64
	TimeoutPromptID   *int64
	DigitTimeout      *int32    `momo:"default:5,dbdefault"`
	InterDigitTimeout *int32    `momo:"default:3,dbdefault"`
	MaxRetries        *int32    `momo:"default:3,dbdefault"`
	AllowDirectDial   bool      `momo:"default:false,dbdefault"`
	TimeoutKind       string    `momo:"default:'hangup'::text,dbdefault"`
	TimeoutTarget     string    `momo:"default:''::text,dbdefault"`
	InvalidKind       string    `momo:"default:'hangup'::text,dbdefault"`
	InvalidTarget     string    `momo:"default:''::text,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt         time.Time `momo:"default:now(),dbdefault"`
	PromptLanguage    *string

	Greeting      *Announcement `momo:"belongs_to,fk:GreetingID,on_delete:restrict"`
	InvalidPrompt *Announcement `momo:"belongs_to,fk:InvalidPromptID,on_delete:restrict"`
	TimeoutPrompt *Announcement `momo:"belongs_to,fk:TimeoutPromptID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *IvrMenu) MomoConfig(c *momo.Config) {
	c.Unique("ivr_menus_name_key", &m.Name)
	c.Check("ivr_menus_digit_timeout_check", "((digit_timeout >= 1) AND (digit_timeout <= 60))")
	c.Check("ivr_menus_inter_digit_timeout_check", "((inter_digit_timeout >= 1) AND (inter_digit_timeout <= 30))")
	c.CheckIn(&m.InvalidKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"fax",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("ivr_menus_max_retries_check", "((max_retries >= 0) AND (max_retries <= 10))")
	c.Check("ivr_menus_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.Check("ivr_menus_number_check", "(number ~ '^([0-9]{2,16})?$'::text)")
	c.CheckIn(&m.TimeoutKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"fax",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Unique("ivr_menus_number", &m.Number).Where("(number <> ''::text)")
}
