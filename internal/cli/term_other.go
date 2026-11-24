//go:build !linux && !darwin

package cli

// isTerminalFd reports false on platforms the kit does not support, so they
// get plain output.
func isTerminalFd(uintptr) bool { return false }
