package engine

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// extendedMetadata names the ACL or extended attributes f carries, or ""
// when it has none. On Linux, POSIX ACLs are extended attributes
// (system.posix_acl_access and system.posix_acl_default).
func extendedMetadata(f *os.File) (string, error) {
	n, err := unix.Flistxattr(int(f.Fd()), nil)
	if errors.Is(err, unix.ENOTSUP) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list extended attributes of %q: %w", f.Name(), err)
	}
	if n > 0 {
		return "extended attributes or an ACL", nil
	}
	return "", nil
}
