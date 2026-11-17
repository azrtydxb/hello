package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestProvisioningSchema fails if the provisioning objects (00006 in the
// frozen goose set, part of momo's init) are missing from a new install or
// from a database taken over from goose, or if the schema accepts an enabled
// phone with no device, a fetch path still carrying a token, or a fetch
// result outside the contract.
func TestProvisioningSchema(t *testing.T) {
	for _, b := range schemaBuilds {
		t.Run(b.name, func(t *testing.T) {
			db := openScratch(t)
			b.build(t, db)
			provisioningChecks(t, db)
		})
	}
}

func provisioningChecks(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	tables := []string{"phones", "prov_templates", "prov_template_versions", "prov_firmware",
		"prov_firmware_pins", "prov_firmware_minimums", "prov_redirect_accounts", "prov_redirect_jobs", "prov_fetches"}
	present := func() (n int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('`+strings.Join(tables, "','")+`')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		var col int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'devices' AND column_name = 'secret_enc'`).Scan(&col); err != nil {
			t.Fatal(err)
		}
		return n + col
	}
	if n := present(); n != len(tables)+1 {
		t.Fatalf("after up: %d of %d provisioning objects present", n, len(tables)+1)
	}

	// Constraints the parallel streams rely on.
	phone := `INSERT INTO phones (mac, vendor, model, device_id, enabled, token_hash, token_enc, admin_password_enc)
		VALUES ($1, 'yealink', 'T54W', NULL, $2, $3, '\x00', '\x00')`
	hash := strings.Repeat("a", 32)
	if _, err := db.ExecContext(ctx, phone, "805ec0000001", true, []byte(hash)); err == nil {
		t.Fatal("an enabled phone without a device was accepted")
	}
	if _, err := db.ExecContext(ctx, phone, "805EC0000001", false, []byte(hash)); err == nil {
		t.Fatal("an uppercase MAC was accepted")
	}
	if _, err := db.ExecContext(ctx, phone, "805ec0000001", false, []byte(hash)); err != nil {
		t.Fatalf("a disabled unbound phone: %v", err)
	}
	fetch := `INSERT INTO prov_fetches (path_redacted, kind, result, status) VALUES ($1, 'device', $2, 404)`
	token := strings.Repeat("a2", 26)
	if _, err := db.ExecContext(ctx, fetch, "/p/"+token+"/805ec0000001.cfg", "served"); err == nil {
		t.Fatal("a fetch path carrying a token was accepted")
	}
	if _, err := db.ExecContext(ctx, fetch, "/p/****/805ec0000001.cfg", "teapot"); err == nil {
		t.Fatal("a fetch result outside the contract was accepted")
	}
	if _, err := db.ExecContext(ctx, fetch, "/p/****/805ec0000001.cfg", "mac_mismatch"); err != nil {
		t.Fatalf("a redacted fetch row: %v", err)
	}
}
