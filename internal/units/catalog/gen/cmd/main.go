// Command gen regenerates or checks internal/contract/enums_gen.go,
// internal/units/catalog/catalog_gen.go, internal/schema/types_gen.go and
// the catalog Markdown tables from registry/units.toml and schema/profile.toml.
package main

import (
	"flag"
	"os"

	"github.com/sathyassn/agent-workstation-kit/internal/units/catalog/gen"
)

func main() {
	root := flag.String("root", ".", "repository root directory")
	check := flag.Bool("check", false, "verify generated files and docs tables are current without writing")
	flag.Parse()

	var err error
	if *check {
		err = gen.CheckRepo(*root)
	} else {
		err = gen.WriteRepo(*root)
	}
	if err != nil {
		_, _ = os.Stderr.WriteString("catalog gen: " + err.Error() + "\n")
		os.Exit(1) //nolint:forbidigo // code generator CLI entrypoint
	}
}
