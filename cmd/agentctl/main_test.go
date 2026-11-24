package main

import (
	"bytes"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		code contract.ExitCode
		out  string
	}{
		{"version", []string{"version"}, contract.ExitDone, version.String() + "\n"},
		{"no subcommand", nil, contract.ExitRefused, ""},
		{"unknown subcommand", []string{"sh"}, contract.ExitRefused, ""},
		{"extra argument", []string{"version", "--all"}, contract.ExitRefused, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			if code := run(tc.args, &out, &errOut); code != tc.code || out.String() != tc.out {
				t.Fatalf("run(%q) = %d, %q; want %d, %q", tc.args, code, out.String(), tc.code, tc.out)
			}
			if tc.code != contract.ExitDone && errOut.Len() == 0 {
				t.Fatal("refusal printed no reason")
			}
		})
	}
}
