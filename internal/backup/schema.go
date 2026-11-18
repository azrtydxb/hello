package backup

import "github.com/azrtydxb/hello/migrations"

// LatestSchema is the newest migration version this binary carries (a
// 14-digit momo version); a backup of a newer schema is refused. A backup
// from before the cutover to momo's migration engine carries a goose version
// (00001 to 00107), which is always older.
func LatestSchema() int64 { return migrations.Latest() }
