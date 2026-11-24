//go:build !faultinject

package engine

// defaultFault is the release build's fault hook: none. Fault injection
// exists only in binaries built with the faultinject tag.
func defaultFault() FaultFunc { return nil }
