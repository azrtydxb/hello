// Package version carries build metadata, set at link time:
//
//	go build -ldflags "-X github.com/azrtydxb/hello/internal/version.Version=v0.1.0 -X github.com/azrtydxb/hello/internal/version.Commit=$(git rev-parse --short HEAD)"
package version

var (
	Version = "dev"
	Commit  = "unknown"
)
