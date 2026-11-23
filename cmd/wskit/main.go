// Command wskit is the Agent Workstation Kit CLI.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

func main() {
	if err := run(os.Stdout); err != nil {
		os.Exit(1)
	}
}

func run(w io.Writer) error {
	if _, err := fmt.Fprintln(w, version.String()); err != nil {
		return fmt.Errorf("print version: %w", err)
	}
	return nil
}
