package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

// TestDockerfileLdflagsStampVersion builds a binary with the exact -ldflags
// the Dockerfile uses (build args substituted) and checks the version package
// reports the injected values. It fails if the Dockerfile's -X paths stop
// matching this package, which would ship images reporting commit "unknown".
func TestDockerfileLdflagsStampVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	df, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`-ldflags "([^"]+)"`).FindSubmatch(df)
	if m == nil {
		t.Fatal(`Dockerfile has no -ldflags "..." in its go build`)
	}
	const wantVersion, wantCommit = "v9.8.7", "0123456789abcdef0123456789abcdef01234567"
	ldflags := strings.NewReplacer("${VERSION}", wantVersion, "${COMMIT}", wantCommit).Replace(string(m[1]))
	if strings.Contains(ldflags, "${") {
		t.Fatalf("unsubstituted build arg in Dockerfile ldflags: %s", ldflags)
	}

	bin := filepath.Join(t.TempDir(), "printversion")
	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, "./testdata/printversion")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(out)), wantVersion+" "+wantCommit; got != want {
		t.Fatalf("stamped binary reports %q, want %q", got, want)
	}
}

func TestVCSRevisionFallback(t *testing.T) {
	info := func(settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Settings: settings}, true }
	}
	cases := []struct {
		name string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{"no build info", func() (*debug.BuildInfo, bool) { return nil, false }, "unknown"},
		{"no vcs", info(), "unknown"},
		{"clean", info(debug.BuildSetting{Key: "vcs.revision", Value: "abc"}, debug.BuildSetting{Key: "vcs.modified", Value: "false"}), "abc"},
		{"dirty", info(debug.BuildSetting{Key: "vcs.revision", Value: "abc"}, debug.BuildSetting{Key: "vcs.modified", Value: "true"}), "abc-dirty"},
	}
	for _, c := range cases {
		if got := vcsRevision(c.read); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
