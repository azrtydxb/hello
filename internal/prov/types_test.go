package prov

import (
	"slices"
	"testing"

	"github.com/azrtydxb/momo/migrate"
	"github.com/azrtydxb/momo/schema"

	"github.com/azrtydxb/hello/migrations"
)

// checkList returns the values the value-list CHECK (c.CheckIn) on
// table.column accepts in the newest migration's schema, sorted.
func checkList(t *testing.T, s *schema.Schema, table, column string) []string {
	t.Helper()
	tb, ok := s.Table(table)
	if !ok {
		t.Fatalf("the schema has no table %s", table)
	}
	for _, c := range tb.Checks {
		if c.In != nil && c.In.Column == column {
			return schema.SortedValues(c.In.Values)
		}
	}
	t.Fatalf("%s.%s has no value-list check", table, column)
	return nil
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return schema.SortedValues(out)
}

// TestContractMatchesMigration fails if the vendors, file kinds or fetch
// results the Go contract names drift from the values the schema's CHECK
// constraints accept (the newest migration's, momo's derived state of
// migrations/postgres): a value only one side knows would either be
// unrepresentable in Go or rejected by the database at insert time. Both
// sides compare as sets.
func TestContractMatchesMigration(t *testing.T) {
	st, err := migrate.LatestState(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
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
		if got := checkList(t, st.Schema, tc.table, tc.column); !slices.Equal(got, tc.want) {
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
