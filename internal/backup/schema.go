package backup

import (
	"io/fs"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/migrations"
)

// LatestSchema is the highest migration number this binary carries; a
// backup of a newer schema is refused.
func LatestSchema() int64 {
	ents, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0
	}
	var max int64
	for _, e := range ents {
		n, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		if v, err := strconv.ParseInt(n, 10, 64); err == nil && v > max {
			max = v
		}
	}
	return max
}
