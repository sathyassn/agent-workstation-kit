//go:build faultinject

package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseKillAt(t *testing.T) {
	tests := []struct {
		in   string
		name string
		n    int64
	}{
		{"7", "", 7},
		{"intent.written", "intent.written", 1},
		{"tx.mutation:3", "tx.mutation", 3},
		{"tx.mutation:x", "tx.mutation", 1},
	}
	for _, tc := range tests {
		if name, n := parseKillAt(tc.in); name != tc.name || n != tc.n {
			t.Errorf("parseKillAt(%q) = %q, %d; want %q, %d", tc.in, name, n, tc.name, tc.n)
		}
	}
}

// TestFaultInjectBuildKillsAtNamedPoint runs a child whose only hook is the
// faultinject build's default, and checks it dies by SIGKILL at the point
// named in the environment. The child writes a marker after the earlier
// point returns, so a hook that kills at the first point reached fails.
func TestFaultInjectBuildKillsAtNamedPoint(t *testing.T) {
	const markerEnv = "WSKIT_TEST_INJECT_MARKER"
	if os.Getenv("WSKIT_TEST_INJECT_CHILD") == "1" {
		fault := defaultFault()
		if fault == nil {
			t.Fatal("faultinject build has no hook")
		}
		fault(PointBackupEntry)
		if err := os.WriteFile(os.Getenv(markerEnv), nil, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		fault(PointIntent) // killed here
		t.Fatal("not killed at intent.written")
	}
	marker := filepath.Join(t.TempDir(), "passed-backup-entry")
	// #nosec G204,G702 -- re-runs this test binary.
	cmd := exec.Command(os.Args[0], "-test.run=^TestFaultInjectBuildKillsAtNamedPoint$")
	cmd.Env = append(os.Environ(), "WSKIT_TEST_INJECT_CHILD=1", markerEnv+"="+marker,
		EnvFaultKillAt+"="+string(PointIntent))
	err := cmd.Run()
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("child did not get past %s before it ended (%v): %v", PointBackupEntry, err, statErr)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child err = %v, want a signal exit", err)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL { //nolint:misspell // Go API name
		t.Fatalf("child ended with %v, want SIGKILL", err)
	}
}
