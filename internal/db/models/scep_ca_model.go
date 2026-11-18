// momo:fromdb sha256=b550f7c0c0cc9d99ecad124ea2664fd572ca39c6c4721fc1ff9dc6b52def8738
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ScepCa models the table "scep_ca".
type ScepCa struct {
	momo.Model `momo:"table:scep_ca"`
	ID         int64 `momo:"pk,serial"`
	CertPem    string
	KeyEnc     []byte
	Source     string
	Active     *bool     `momo:"default:true,dbdefault"`
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ScepCa) MomoConfig(c *momo.Config) {
	c.Check("scep_ca_cert_pem_check", "(cert_pem ~~ '-----BEGIN CERTIFICATE-----%'::text)")
	c.CheckIn(&m.Source,
		"generated",
		"imported",
	)
	c.Unique("scep_ca_one_active", &m.Active).Where("active")
}
