package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

func TestRunVersion(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"version"}, strings.NewReader(""), &out, &errOut)
	if code != contract.ExitDone {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, contract.ExitDone, errOut.String())
	}
	if got, want := out.String(), version.String()+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunExitCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		args     []string
		wantCode contract.ExitCode
	}{
		{name: "help", args: []string{"--help"}, wantCode: contract.ExitDone},
		{name: "unimplemented plan", args: []string{"plan"}, wantCode: contract.ExitRefused},
		{name: "removed scope flag", args: []string{"setup", "--scope", "packages"}, wantCode: contract.ExitRefused},
		{name: "invalid stage value", args: []string{"setup", "--stage", "nonexistent"}, wantCode: contract.ExitRefused},
		{name: "valid stage unimplemented", args: []string{"setup", "--stage", "packages"}, wantCode: contract.ExitRefused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			got := run(context.Background(), tc.args, strings.NewReader(""), &out, &errOut)
			if got != tc.wantCode {
				t.Errorf("run(%v) exit code = %d, want %d (stdout=%q stderr=%q)",
					tc.args, got, tc.wantCode, out.String(), errOut.String())
			}
		})
	}
}
