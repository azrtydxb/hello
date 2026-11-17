package main

import (
	"strings"
	"testing"

	momomigrate "github.com/azrtydxb/momo/migrate"

	"github.com/azrtydxb/hello/internal/migrate"
)

// TestPrintStatus pins the `migrate status` lines a runbook reads.
func TestPrintStatus(t *testing.T) {
	vs := []momomigrate.VersionStatus{
		{Version: "20261117120000", Name: "init", State: momomigrate.StateApplied},
		{Version: "20261117120001", Name: "legacy_seed", State: momomigrate.StatePending},
	}
	for _, tc := range []struct {
		name string
		rep  migrate.Report
		want string
	}{
		{"momo", migrate.Report{Goose: 107, TakenOver: true, Versions: vs},
			"20261117120000 applied  init\n20261117120001 pending  legacy_seed\n"},
		{"goose, not taken over", migrate.Report{Goose: 107, Versions: vs},
			"goose schema at 00107: the next migrate up takes it over (the frozen goose migrations, then legacy_seed is recorded as applied)\n" +
				"20261117120000 applied  init\n20261117120001 pending  legacy_seed\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			printStatus(&b, &tc.rep)
			if b.String() != tc.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tc.want)
			}
		})
	}
}
