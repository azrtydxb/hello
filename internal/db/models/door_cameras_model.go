// momo:fromdb sha256=bf6faa194b400acc261cfc6eb85f9eca8beebd99fb3de2487cf498e168689ba7
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// DoorCamera models the table "door_cameras".
type DoorCamera struct {
	momo.Model
	ExtensionID int64 `momo:"pk"`
	Kind        string
	URL         string
	Username    string    `momo:"default:''::text,dbdefault"`
	PasswordEnc []byte    `momo:"nullable"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *DoorCamera) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Kind,
		"mjpeg",
		"snapshot",
	)
	c.Check("door_cameras_url_check", "((length(url) >= 8) AND (length(url) <= 2048))")
	c.Check("door_cameras_username_check", "(length(username) <= 128)")
}
