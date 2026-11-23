// Command agentctl is the narrow helper named by the agent account's sudoers
// rule on Linux. It runs only the exact subcommands in its table, each with
// a fixed number of arguments, and refuses everything else with exit 2. The
// root actions arrive with later tasks; root admission is TSK-024.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

// errUsage reports a command line outside the subcommand table.
var errUsage = errors.New("usage")

// subcommand is one exact entry of the helper's command table.
type subcommand struct {
	name  string
	nargs int
	run   func(stdout io.Writer, args []string) error
}

func subcommands() []subcommand {
	return []subcommand{
		{name: "version", nargs: 0, run: func(w io.Writer, _ []string) error {
			if _, err := fmt.Fprintln(w, version.String()); err != nil {
				return fmt.Errorf("print version: %w", err)
			}
			return nil
		}},
	}
}

func main() {
	os.Exit(int(run(os.Args[1:], os.Stdout, os.Stderr)))
}

// run dispatches args to an exact subcommand and returns the exit code.
func run(args []string, stdout, stderr io.Writer) contract.ExitCode {
	err := dispatch(args, stdout)
	switch {
	case err == nil:
		return contract.ExitDone
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "agentctl: %v\nusage: agentctl version\n", err)
		return contract.ExitRefused
	default:
		_, _ = fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return contract.ExitFailed
	}
}

func dispatch(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: no subcommand", errUsage)
	}
	for _, sc := range subcommands() {
		if sc.name != args[0] {
			continue
		}
		if len(args)-1 != sc.nargs {
			return fmt.Errorf("%w: %s takes %d arguments, got %d", errUsage, sc.name, sc.nargs, len(args)-1)
		}
		return sc.run(stdout, args[1:])
	}
	return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
}
