// momo:fromdb sha256=507434eb8586a0b4dff6067a5e474845a7f260875a1628eb6ca20905c685661e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// VoicemailBox models the table "voicemail_boxes".
type VoicemailBox struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	ExtensionID       int64
	PasswordHash      string    `momo:"default:''::text,dbdefault"`
	Email             string    `momo:"default:''::text,dbdefault"`
	GreetingObject    string    `momo:"default:''::text,dbdefault"`
	UnreachableObject string    `momo:"default:''::text,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt         time.Time `momo:"default:now(),dbdefault"`
	GreetingTts       *types.JSON[json.RawMessage]
	UnreachableTts    *types.JSON[json.RawMessage]
	BusyObject        string `momo:"default:''::text,dbdefault"`
	NameObject        string `momo:"default:''::text,dbdefault"`
	BusyTts           *types.JSON[json.RawMessage]
	PromptLanguage    *string

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *VoicemailBox) MomoConfig(c *momo.Config) {
	c.Unique("voicemail_boxes_extension_id_key", &m.ExtensionID)
}
