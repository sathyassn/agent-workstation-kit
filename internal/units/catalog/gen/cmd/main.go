// Command gen regenerates internal/contract/enums_gen.go and
// internal/units/catalog/catalog_gen.go from registry/units.toml.
package main

import (
	"flag"
	"os"

	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog/gen"
)

func main() {
	root := flag.String("root", ".", "repository root directory")
	flag.Parse()

	if err := gen.WriteRepo(*root); err != nil {
		_, _ = os.Stderr.WriteString("catalog gen: " + err.Error() + "\n")
		os.Exit(1) //nolint:forbidigo // code generator CLI entrypoint
	}
}
