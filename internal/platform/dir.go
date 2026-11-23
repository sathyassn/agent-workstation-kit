package platform

import (
	"fmt"
	"os"
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
