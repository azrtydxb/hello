// momo:fromdb sha256=6f4c3082b1c409c91e560a46b2d7da16cfe4afb61a9cc36169c0611b770acfda
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Fax models the table "faxes".
type Fax struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	Direction      string
	Status         string
	PeerNumber     string `momo:"default:''::text,dbdefault"`
	LocalNumber    string `momo:"default:''::text,dbdefault"`
	DestinationID  *int64
	OwnerExtension string `momo:"default:''::text,dbdefault"`
	Pages          int32  `momo:"default:0,dbdefault"`
	Mode           string `momo:"default:''::text,dbdefault"`
	BitRate        int32  `momo:"default:0,dbdefault"`
	ResultCode     *int32
	ResultText     string    `momo:"default:''::text,dbdefault"`
	ObjectKey      string    `momo:"default:''::text,dbdefault"`
	CorrelationID  string    `momo:"default:''::text,dbdefault"`
	CreatedBy      string    `momo:"default:''::text,dbdefault"`
	CreatedAt      time.Time `momo:"default:now(),dbdefault"`
	FinishedAt     *time.Time
	TrunkName      string    `momo:"default:''::text,dbdefault"`
	Attempts       int32     `momo:"default:0,dbdefault"`
	NextAttemptAt  time.Time `momo:"default:now(),dbdefault"`
	ClaimedBy      string    `momo:"default:''::text,dbdefault"`
	LeaseUntil     *time.Time
	MailTo         string    `momo:"default:''::text,dbdefault"`
	MailStatus     string    `momo:"default:'none'::text,dbdefault"`
	MailAttempts   int32     `momo:"default:0,dbdefault"`
	MailNextAt     time.Time `momo:"default:now(),dbdefault"`
	MailError      string    `momo:"default:''::text,dbdefault"`

	Destination *FaxDestination `momo:"belongs_to,fk:DestinationID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Fax) MomoConfig(c *momo.Config) {
	c.Check("faxes_attempts_check", "((attempts >= 0) AND (attempts <= 3))")
	c.CheckIn(&m.Direction,
		"inbound",
		"outbound",
	)
	c.Check("faxes_local_number_check", "(length(local_number) <= 40)")
	c.Check("faxes_mail_error_check", "(length(mail_error) <= 200)")
	c.CheckIn(&m.MailStatus,
		"failed",
		"none",
		"pending",
		"sending",
		"sent",
	)
	c.CheckIn(&m.Mode,
		"",
		"g711",
		"t38",
	)
	c.Check("faxes_owner_extension_check", "(length(owner_extension) <= 10)")
	c.Check("faxes_pages_check", "(pages >= 0)")
	c.Check("faxes_peer_number_check", "(length(peer_number) <= 40)")
	c.Check("faxes_result_text_check", "(length(result_text) <= 200)")
	c.CheckIn(&m.Status,
		"cancelled",
		"failed",
		"partial",
		"queued",
		"received",
		"receiving",
		"sending",
		"sent",
	)
	c.Index("faxes_corr_idx", &m.CorrelationID).Where("(correlation_id <> ''::text)")
	c.Index("faxes_created_idx", &m.CreatedAt, &m.ID).Desc(&m.CreatedAt).Desc(&m.ID)
	c.Index("faxes_mail_idx", &m.MailNextAt).Where("(mail_status = ANY (ARRAY['pending'::text, 'sending'::text]))")
	c.Index("faxes_owner_idx", &m.OwnerExtension, &m.CreatedAt).Desc(&m.CreatedAt)
	c.Index("faxes_queue_idx", &m.NextAttemptAt).Where("(status = 'queued'::text)")
}
