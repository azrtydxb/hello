package vkconn

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	ciClient = regexp.MustCompile(`valkey\.NewClient\(valkey\.ClientOption\{InitAddress: \[\]string\{addr\}[^\n]*`)
	selectDB = regexp.MustCompile(`SelectDB: (\d+)`)
)

// TestValkeyDBsPerPackage fails if a test opens the CI Valkey
// (HELLO_TEST_VALKEY_ADDR) without choosing a database, or two packages
// share one. go test runs packages in parallel against one Valkey, and
// tests FLUSHDB their database, so a shared one wipes another package's
// keys mid-test (the TestProvRateLimit and TestOAuthThrottle flakes).
func TestValkeyDBsPerPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	owner := map[string]string{} // DB -> package directory
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); p != root && (strings.HasPrefix(n, ".") || n == "web" || n == "node_modules" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		pkg := filepath.Dir(p)
		for _, line := range ciClient.FindAllString(string(src), -1) {
			m := selectDB.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("%s: a CI Valkey client without SelectDB uses database 0, which livestate flushes", p)
				continue
			}
			if o, ok := owner[m[1]]; ok && o != pkg {
				t.Errorf("Valkey database %s is used by both %s and %s", m[1], o, pkg)
			}
			owner[m[1]] = pkg
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(owner) < 5 {
		t.Fatalf("found %d test databases; the pattern no longer matches the tests", len(owner))
	}
}
