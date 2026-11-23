package main

import (
	"bytes"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/version"
)

func TestRunPrintsVersion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := out.String(), version.String()+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
