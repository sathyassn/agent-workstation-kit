package platform_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// TestOSCommandRunnerConcurrentStdoutStderrNoRace is R7: passing the same
// unsynchronised bytes.Buffer as both Stdout and Stderr to OSCommandRunner
// must not trigger a data race when a child writes both streams concurrently.
func TestOSCommandRunnerConcurrentStdoutStderrNoRace(t *testing.T) {
	var shared bytes.Buffer
	res, err := (platform.OSCommandRunner{}).Run(context.Background(), platform.ExecSpec{
		Argv:   []string{"/bin/sh", "-c", "i=0; while [ $i -lt 200 ]; do echo out-$i; echo err-$i >&2; i=$((i+1)); done"},
		Env:    []string{"PATH=/usr/bin:/bin"},
		Stdout: &shared,
		Stderr: &shared,
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
}
