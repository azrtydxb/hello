// Command doctable prints the MCP tool and resource tables of
// docs/ai-access.md, generated from the embedded API document. Paste its
// output between the doctable markers; TestDocsAIAccess fails until the
// guide matches it.
package main

import (
	"fmt"
	"os"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/mcp"
)

func main() {
	spec, err := apispec.Load(api.OpenAPI())
	if err == nil {
		var out string
		if out, err = mcp.DocTables(spec); err == nil {
			fmt.Print(out)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "doctable:", err)
	os.Exit(1)
}
