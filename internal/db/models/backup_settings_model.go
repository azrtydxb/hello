// momo:fromdb sha256=b2c90d2763ac3ada2990941fc59a056fcccb2e44ab2e818d61ed0f792c7c9bec
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// BackupSetting models the table "backup_settings".
type BackupSetting struct {
	momo.Model
	ID           *bool     `momo:"pk,default:true,dbdefault"`
	Enabled      *bool     `momo:"default:true,dbdefault"`
	Cron         string    `momo:"default:'0 2 * * *'::text,dbdefault"`
	Kind         string    `momo:"default:'minio'::text,dbdefault"`
	S3Endpoint   string    `momo:"default:''::text,dbdefault"`
	S3Bucket     string    `momo:"default:''::text,dbdefault"`
	S3Region     string    `momo:"default:''::text,dbdefault"`
	S3PathStyle  bool      `momo:"default:false,dbdefault"`
	S3KeysSealed []byte    `momo:"nullable"`
	KeepCount    *int32    `momo:"default:14,dbdefault"`
	NotifyEmail  string    `momo:"default:''::text,dbdefault"`
	UpdatedAt    time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *BackupSetting) MomoConfig(c *momo.Config) {
	c.Check("backup_settings_id_check", "id")
	c.Check("backup_settings_keep_count_check", "((keep_count >= 1) AND (keep_count <= 365))")
	c.CheckIn(&m.Kind,
		"minio",
		"s3",
	)
}
