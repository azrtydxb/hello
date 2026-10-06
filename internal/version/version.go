// Package version carries build metadata, set at link time:
//
//	go build -ldflags "-X github.com/azrtydxb/hello/internal/version.Version=v0.1.0 -X github.com/azrtydxb/hello/internal/version.Commit=$(git rev-parse HEAD)"
//
// The Dockerfile does this from its VERSION and COMMIT build args, which CI's
// publish job sets (COMMIT=github.sha). A plain `go build` in a git checkout
// falls back to the VCS revision Go stamps into the binary.
package version

import "runtime/debug"

var (
	Version = "dev"
	Commit  = "unknown"
)

func init() {
	if Commit == "unknown" {
		Commit = vcsRevision(debug.ReadBuildInfo)
	}
}

// vcsRevision returns the vcs.revision build setting (suffixed "-dirty" for
// a modified tree), or "unknown" when the binary carries none.
func vcsRevision(read func() (*debug.BuildInfo, bool)) string {
	info, ok := read()
	if !ok {
		return "unknown"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown"
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
