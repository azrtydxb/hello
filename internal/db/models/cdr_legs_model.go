// momo:fromdb sha256=f33692b721e08ff9e54887d0183381d1e34d7552924661af2dc80a3803e52282
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// CdrLeg models the table "cdr_legs".
type CdrLeg struct {
	momo.Model
	CdrID       int64 `momo:"pk"`
	Position    int32 `momo:"pk"`
	Role        string
	Extension   string `momo:"default:''::text,dbdefault"`
	Trunk       string `momo:"default:''::text,dbdefault"`
	Codec       *string
	PayloadType *int32
	Result      string `momo:"default:''::text,dbdefault"`
	TargetKind  string `momo:"default:''::text,dbdefault"`

	Cdr *Cdr `momo:"belongs_to,fk:CdrID,on_delete:cascade"`
}
