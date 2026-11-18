// momo:fromdb sha256=f42297b93ec80b8f8be2c5ac891ff38751e491504f5be8a0f2f82a927d8c06d4
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// OutboundRoute models the table "outbound_routes".
type OutboundRoute struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	Position          int32
	Name              string
	MatchKind         string
	Match             string
	SourceExtensions  []string `momo:"default:'{}'::text[],dbdefault"`
	Schedule          *types.JSON[json.RawMessage]
	NumberTransform   types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	CalleridTransform types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	FailoverCodes     []int32                     `momo:"dbdefault"`
	Emergency         bool                        `momo:"default:false,dbdefault"`
	Enabled           *bool                       `momo:"default:true,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OutboundRoute) MomoConfig(c *momo.Config) {
	c.Default(&m.FailoverCodes, "'{408,480,500,502,503,504}'::integer[]")
	c.Unique("outbound_routes_position", &m.Position)
	c.Check("outbound_routes_match_check", "(length(match) <= 500)")
	c.CheckIn(&m.MatchKind,
		"prefix",
		"regex",
	)
}
