// Command printversion prints the link-time version metadata; the version
// package's test builds it with the Dockerfile's ldflags.
package main

import (
	"fmt"

	"github.com/azrtydxb/hello/internal/version"
)

func main() { fmt.Println(version.Version, version.Commit) }
