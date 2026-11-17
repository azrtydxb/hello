// momo:fromdb sha256=9b72f3afea2227411bdcdc8d013d842787858b1850a473faa6870965528e7623
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ScepCertificate models the table "scep_certificates".
type ScepCertificate struct {
	momo.Model
	ID           int64 `momo:"pk,serial"`
	CaID         int64
	PhoneID      *int64
	Mac          string
	Serial       string
	Subject      string
	KeySha256    string
	CertPem      string
	Kind         string
	NotBefore    time.Time
	NotAfter     time.Time
	IssuedAt     time.Time `momo:"default:now(),dbdefault"`
	RevokedAt    *time.Time
	RevokeReason *string

	Ca    *ScepCa `momo:"belongs_to,fk:CaID,on_delete:restrict"`
	Phone *Phone  `momo:"belongs_to,fk:PhoneID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ScepCertificate) MomoConfig(c *momo.Config) {
	c.Unique("scep_certificates_serial_key", &m.Serial)
	c.Check("scep_certificates_check", "((revoked_at IS NULL) = (revoke_reason IS NULL))")
	c.Check("scep_certificates_key_sha256_check", "(key_sha256 ~ '^[0-9a-f]{64}$'::text)")
	c.CheckIn(&m.Kind,
		"enrol",
		"renewal",
	)
	c.Check("scep_certificates_mac_check", "(mac ~ '^[0-9a-f]{12}$'::text)")
	c.CheckIn(&m.RevokeReason,
		"cessation_of_operation",
		"key_compromise",
		"phone_removed",
		"superseded",
		"unspecified",
	)
	c.Check("scep_certificates_serial_check", "(serial ~ '^[0-9a-f]{1,40}$'::text)")
	c.Index("scep_certificates_phone", &m.PhoneID, &m.IssuedAt).Desc(&m.IssuedAt)
	c.Index("scep_certificates_revoked", &m.CaID).Where("(revoked_at IS NOT NULL)")
}
