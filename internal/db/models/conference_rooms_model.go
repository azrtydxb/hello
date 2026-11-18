// momo:fromdb sha256=53feb4e009864b0c930f3338d75c195e137c7d0eb8d1b1955cde98d5be814adf
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ConferenceRoom models the table "conference_rooms".
type ConferenceRoom struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	Name              string
	Number            string
	UserPinEnc        []byte    `momo:"nullable"`
	LeaderPinEnc      []byte    `momo:"nullable"`
	MaxParticipants   *int32    `momo:"default:10,dbdefault"`
	WaitForLeader     bool      `momo:"default:false,dbdefault"`
	MuteOnEntry       bool      `momo:"default:false,dbdefault"`
	EntryExit         string    `momo:"default:'tone'::text,dbdefault"`
	EntryAnnouncement string    `momo:"default:''::text,dbdefault"`
	ExitAnnouncement  string    `momo:"default:''::text,dbdefault"`
	Record            bool      `momo:"default:false,dbdefault"`
	Enabled           *bool     `momo:"default:true,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt         time.Time `momo:"default:now(),dbdefault"`
	Video             *bool     `momo:"default:true,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ConferenceRoom) MomoConfig(c *momo.Config) {
	c.Unique("conference_rooms_name_key", &m.Name)
	c.Unique("conference_rooms_number_key", &m.Number)
	c.CheckIn(&m.EntryExit,
		"announcement",
		"none",
		"tone",
	)
	c.Check("conference_rooms_max_participants_check", "((max_participants >= 2) AND (max_participants <= 50))")
	c.Check("conference_rooms_name_check", "((length(name) >= 1) AND (length(name) <= 64))")
	c.Check("conference_rooms_number_check", "(number ~ '^[0-9]{2,10}$'::text)")
}
