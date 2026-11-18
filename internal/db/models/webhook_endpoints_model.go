// momo:fromdb sha256=a42f89cab2ba7fefa17eea033fb389162b388b103fc976219747694c4c5b3bca
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// WebhookEndpoint models the table "webhook_endpoints".
type WebhookEndpoint struct {
	momo.Model
	ID                    int64 `momo:"pk,serial"`
	Name                  string
	URL                   string
	SecretEnc             []byte
	PreviousSecretEnc     []byte `momo:"nullable"`
	PreviousSecretExpires *time.Time
	Events                []string `momo:"default:'{}'::text[],dbdefault"`
	Extensions            []string `momo:"default:'{}'::text[],dbdefault"`
	Enabled               *bool    `momo:"default:true,dbdefault"`
	AllowPrivate          bool     `momo:"default:false,dbdefault"`
	DisabledReason        string   `momo:"default:''::text,dbdefault"`
	ConsecutiveFailures   int32    `momo:"default:0,dbdefault"`
	LastSuccessAt         *time.Time
	LastFailureAt         *time.Time
	LastStatus            *int32
	CreatedBy             string    `momo:"default:''::text,dbdefault"`
	CreatedAt             time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt             time.Time `momo:"default:now(),dbdefault"`
	IncludePeer           bool      `momo:"default:false,dbdefault"`
	IncludeMessageBody    bool      `momo:"default:false,dbdefault"`
}
