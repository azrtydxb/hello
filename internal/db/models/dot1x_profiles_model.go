// momo:fromdb sha256=254a6d44caf99415698776c29e99e27ec9011a98a02bcf01d5def80999626efe
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Dot1xProfile models the table "dot1x_profiles".
type Dot1xProfile struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	Name              string
	Method            string
	Identity          string
	AnonymousIdentity string    `momo:"default:''::text,dbdefault"`
	PasswordEnc       []byte    `momo:"nullable"`
	CaCertPem         string    `momo:"default:''::text,dbdefault"`
	ServerDomain      string    `momo:"default:''::text,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt         time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Dot1xProfile) MomoConfig(c *momo.Config) {
	c.Check("dot1x_profiles_anonymous_identity_check", "(char_length(anonymous_identity) <= 64)")
	c.Check("dot1x_profiles_ca_cert_pem_check", "((ca_cert_pem = ''::text) OR (ca_cert_pem ~~ '-----BEGIN CERTIFICATE-----%'::text))")
	c.Check("dot1x_profiles_identity_check", "((char_length(identity) >= 1) AND (char_length(identity) <= 64))")
	c.CheckIn(&m.Method,
		"eap-md5",
		"eap-tls",
		"peap",
	)
	c.Check("dot1x_profiles_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 64))")
	c.Check("dot1x_profiles_server_domain_check", "(char_length(server_domain) <= 253)")
	c.IndexExpr("dot1x_profiles_name", "lower(name)").Unique()
}
