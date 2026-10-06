package prov

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// checkList returns the quoted values of the CHECK (column IN (...)) list
// for column in the CREATE TABLE of table in the provisioning migration.
func checkList(t *testing.T, sql, table, column string) []string {
	t.Helper()
	start := strings.Index(sql, "CREATE TABLE "+table+" (")
	if start < 0 {
		t.Fatalf("migration has no table %s", table)
	}
	body := sql[start:]
	body = body[:strings.Index(body, "\n);")]
	m := regexp.MustCompile(`(?s)\b` + column + `\s+TEXT[^\n]*?CHECK \(` + column + ` IN \(([^)]*)\)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s.%s has no IN list", table, column)
	}
	var out []string
	for _, q := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

// TestContractMatchesMigration fails if the vendors, file kinds or fetch
// results the Go contract names drift from the values the migration's
// CHECK constraints accept: a value only one side knows would either be
// unrepresentable in Go or rejected by the database at insert time.
func TestContractMatchesMigration(t *testing.T) {
	b, err := os.ReadFile("../../migrations/00006_provisioning.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	sql = sql[:strings.Index(sql, "-- +goose Down")]
	firstClass := strs(Vendors[:5])
	for _, tc := range []struct {
		table, column string
		want          []string
	}{
		{"prov_templates", "vendor", strs(Vendors)},
		{"phones", "vendor", strs(Vendors)},
		{"prov_firmware", "vendor", strs(Vendors)},
		{"prov_firmware_pins", "vendor", strs(Vendors)},
		{"prov_redirect_accounts", "vendor", firstClass},
		{"prov_redirect_jobs", "vendor", firstClass},
		{"prov_fetches", "kind", strs(FileKinds)},
		{"prov_fetches", "result", strs(Results)},
	} {
		got := checkList(t, sql, tc.table, tc.column)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s.%s CHECK = %v, Go contract = %v", tc.table, tc.column, got, tc.want)
		}
	}
	for _, v := range Vendors {
		if !v.Valid() {
			t.Errorf("%s not Valid", v)
		}
	}
	if Vendor("cisco").Valid() || Vendor("").Valid() {
		t.Error("an unknown vendor is Valid")
	}
}

// TestSealingContexts fails if an AAD differs from the spec's
// device:<id>, phone-token:<id>, phone-admin:<id> and redirect:<vendor>:
// a value sealed under one spelling would not open under another.
func TestSealingContexts(t *testing.T) {
	for got, want := range map[string]string{
		DeviceSecretAAD(7):   "device:7",
		PhoneTokenAAD(42):    "phone-token:42",
		PhoneAdminAAD(42):    "phone-admin:42",
		RedirectAAD(Yealink): "redirect:yealink",
	} {
		if got != want {
			t.Errorf("AAD = %q, want %q", got, want)
		}
	}
	if !(Template{BuiltinRef: "yealink"}).Builtin() || (Template{ID: 3, BuiltinRef: "yealink"}).Builtin() {
		t.Error("Builtin must hold exactly for ID 0")
	}
}
