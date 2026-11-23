package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CheckPrivateDir checks that path is a real folder (not a link), owned by
// this process's effective user, and not writable by group or others.
func CheckPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrUnsafeDir, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a folder", ErrUnsafeDir, path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %q has mode %v", ErrUnsafeDir, path, info.Mode().Perm())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %q: owner unknown", ErrUnsafeDir, path)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: %q is owned by uid %d", ErrUnsafeDir, path, st.Uid)
	}
	return nil
}

// ErrUntrustedBinary reports a supervisor binary that is not an absolute
// path, or that root does not own, or that group or others could replace.
// It is a sentinel for errors.Is and is never reassigned.
var ErrUntrustedBinary = errors.New("binary is not trusted")

// CheckTrustedBinary checks that path is absolute and that the file it
// resolves to, and every folder above it, is owned by root and not writable
// by group or others, so nobody but root can replace what runs.
func CheckTrustedBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %q is not absolute", ErrUntrustedBinary, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrUntrustedBinary, path, err)
	}
	info, err := os.Lstat(real)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrUntrustedBinary, real, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%w: %q is not an executable file", ErrUntrustedBinary, real)
	}
	for p := real; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("%w: %q: %w", ErrUntrustedBinary, p, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%w: %q must be owned by root and not writable by group or others", ErrUntrustedBinary, p)
		}
		if p == "/" {
			return nil
		}
	}
}
