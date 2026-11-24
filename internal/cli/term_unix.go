//go:build linux || darwin

package cli

import "golang.org/x/sys/unix"

// isTerminalFd reports whether fd is a terminal: the terminal attributes
// ioctl succeeds only on one, unlike a character-device check, which
// /dev/null also passes.
func isTerminalFd(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios) //nolint:gosec // a file descriptor fits in int
	return err == nil
}
