// momo:fromdb sha256=dfe23b0cd9e10fdd49146474180987f197cb49115c2fa5bb932de6e31e51068b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// CrmOauthState models the table "crm_oauth_states".
type CrmOauthState struct {
	momo.Model
	StateHash   []byte `momo:"pk"`
	ConnectorID int64
	Actor       string
	ExpiresAt   time.Time `momo:"index:crm_oauth_states_expires"`

	Connector *CrmConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}
