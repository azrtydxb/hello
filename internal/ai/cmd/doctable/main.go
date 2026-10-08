// Command doctable prints the generated tables of docs/ai-agent.md.
package main

import (
	"fmt"
	"os"

	"github.com/azrtydxb/hello/internal/ai/doctable"
	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
)

func main() {
	spec, err := apispec.Load(api.OpenAPI())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := doctable.Tables(spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(out)
}
