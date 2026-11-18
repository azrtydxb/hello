// momo:fromdb sha256=146071ce21819bcae758a17872050c2be833a1414e15cbb6c9fa3d04f89b63ec
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// Extension models the table "extensions".
type Extension struct {
	momo.Model
	ID                 int64 `momo:"pk,serial"`
	Number             string
	Name               string
	CreatedAt          time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt          time.Time `momo:"default:now(),dbdefault"`
	ExternalNumber     string    `momo:"default:''::text,dbdefault"`
	Dnd                bool      `momo:"default:false,dbdefault"`
	ForwardAlways      string    `momo:"default:''::text,dbdefault"`
	ForwardBusy        string    `momo:"default:''::text,dbdefault"`
	ForwardNoAnswer    string    `momo:"default:''::text,dbdefault"`
	VoicemailEnabled   *bool     `momo:"default:true,dbdefault"`
	RecordDefault      bool      `momo:"default:false,dbdefault"`
	DirectoryHidden    bool      `momo:"default:false,dbdefault"`
	AllowIntercom      *bool     `momo:"default:true,dbdefault"`
	CallWaiting        *bool     `momo:"default:true,dbdefault"`
	MohClassID         *int64
	NoAnswerRings      int32                       `momo:"default:0,dbdefault"`
	Kind               string                      `momo:"default:'user'::text,dbdefault"`
	DoorTargets        types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	AnonymousOutbound  bool                        `momo:"default:false,dbdefault"`
	PhoneSendAnonymous bool                        `momo:"default:false,dbdefault"`
	LocationID         *int64
	DndSource          string `momo:"default:'manual'::text,dbdefault"`
	VideoAllowed       *bool  `momo:"default:true,dbdefault"`
	RecordVideo        *bool  `momo:"default:true,dbdefault"`
	ProvisionedBy      *int64

	Location           *Location           `momo:"belongs_to,fk:LocationID,on_delete:restrict"`
	MohClass           *MohClass           `momo:"belongs_to,fk:MohClassID,on_delete:set_null"`
	DirectoryConnector *DirectoryConnector `momo:"belongs_to,fk:ProvisionedBy,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Extension) MomoConfig(c *momo.Config) {
	c.Unique("extensions_number_key", &m.Number)
	c.CheckIn(&m.DndSource,
		"calendar",
		"manual",
	)
	c.Check("extensions_external_number_check", "(external_number ~ '^(\\+?[0-9]{2,20})?$'::text)")
	c.CheckIn(&m.Kind,
		"door",
		"user",
	)
	c.Check("extensions_no_answer_rings_check", "((no_answer_rings >= 0) AND (no_answer_rings <= 20))")
	c.Check("extensions_number_check", "(number ~ '^[0-9]{2,10}$'::text)")
	c.Index("extensions_location", &m.LocationID).Where("(location_id IS NOT NULL)")
	c.Index("extensions_provisioned_by", &m.ProvisionedBy).Where("(provisioned_by IS NOT NULL)")
}
