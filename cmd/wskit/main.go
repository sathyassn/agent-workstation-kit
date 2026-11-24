// Command wskit is the Agent Workstation Kit CLI.
package main

import (
	"context"
	"io"
	"os"

	"github.com/sathyassn/agent-workstation-kit/internal/cli"
	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

func main() {
	code := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	os.Exit(int(code))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) contract.ExitCode {
	err := cli.Execute(ctx, args, cli.Options{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	return cli.ExitCodeFor(err)
}
