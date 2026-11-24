//go:build !faultinject

package engine

import "testing"

// TestReleaseBuildHasNoFaultHook: without the faultinject tag, the engine has
// no fault hook, whatever the environment says.
func TestReleaseBuildHasNoFaultHook(t *testing.T) {
	t.Setenv("WSKIT_FAULT_KILL_AT", "1")
	if defaultFault() != nil {
		t.Fatal("release build has a fault hook")
	}
}
