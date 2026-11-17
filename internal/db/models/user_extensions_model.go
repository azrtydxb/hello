// momo:fromdb sha256=7e59b3cbc8f5305ca0060b5fc792e4ed57242294475c76d7f93346a156c3f3e5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// UserExtension models the table "user_extensions".
type UserExtension struct {
	momo.Model
	UserID            int64 `momo:"pk"`
	ExtensionID       int64 `momo:"pk"`
	Position          int32 `momo:"default:0,dbdefault"`
	SourceConnectorID *int64

	Extension       *Extension          `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	SourceConnector *DirectoryConnector `momo:"belongs_to,fk:SourceConnectorID"`
	User            *User               `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *UserExtension) MomoConfig(c *momo.Config) {
	c.Unique("user_extensions_extension_id_key", &m.ExtensionID)
}
