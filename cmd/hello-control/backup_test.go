package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/config"
)

func backupCfg() config.Control {
	return config.Control{
		SecretKey:     "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		MinioEndpoint: "minio.invalid:9000", MinioAccessKey: "a", MinioSecretKey: "b",
	}
}

// TestBackupRestoreCommand fails if the command accepts a missing --from or a
// stray argument, or goes on without the typed confirmation: the database
// and the objects must be untouched until RESTORE is typed (or --yes given).
func TestBackupRestoreCommand(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	env := func(string) string { return "" }
	run := func(args []string, in string) (string, error) {
		var out strings.Builder
		err := backupRestore(ctx, backupCfg(), nil, log, args, env, strings.NewReader(in), &out)
		return out.String(), err
	}
	for _, bad := range [][]string{{}, {"--from"}, {"--from", "k", "extra"}, {"--nope"}} {
		if _, err := run(bad, ""); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%v: err = %v, want usage", bad, err)
		}
	}
	for _, in := range []string{"", "no\n", "restore\n", "RESTORE now\n"} {
		out, err := run([]string{"--from", "archives/x.hbk"}, in)
		if err == nil || !strings.Contains(err.Error(), "not confirmed") {
			t.Errorf("input %q: err = %v", in, err)
		}
		if !strings.Contains(out, "Type RESTORE to continue") {
			t.Errorf("no prompt: %q", out)
		}
	}
	// A bad external endpoint is refused before anything else.
	if _, err := run([]string{"--from", "k", "--s3-endpoint", "ftp://x", "--yes"}, ""); err == nil || !strings.Contains(err.Error(), "--s3-endpoint") {
		t.Errorf("bad endpoint: %v", err)
	}
	// A bad key is reported without any network access.
	cfg := backupCfg()
	cfg.SecretKey = "short"
	if err := backupRestore(ctx, cfg, nil, log, []string{"--from", "k", "--yes"}, env, strings.NewReader(""), io.Discard); err == nil {
		t.Error("bad HELLO_SECRET_KEY accepted")
	}
}
