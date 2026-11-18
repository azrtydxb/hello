// momo:fromdb sha256=22fe3c6dfa2fc0b8b9aa7a4ba8c52c84515ea1ea2e2aa6b6d521ab4085976f42
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// DoorOpen models the table "door_open".
type DoorOpen struct {
	momo.Model      `momo:"table:door_open"`
	ExtensionID     int64     `momo:"pk"`
	CodeEnc         []byte    `momo:"nullable"`
	HangupAfterOpen bool      `momo:"default:false,dbdefault"`
	UpdatedAt       time.Time `momo:"default:now(),dbdefault"`

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
}
