package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// systemProvenance is the attribute macOS attaches by itself to files that
// ordinary processes write, the engine's own writes included. It records
// which process wrote the file for Gatekeeper; it is not file content the
// kit set, and the OS attaches it again to whatever the engine writes, so a
// backup does not need to keep it. Every other attribute is refused.
const systemProvenance = "com.apple.provenance"

func xattrNames(f *os.File) ([]string, error) {
	n, err := unix.Flistxattr(int(f.Fd()), nil)
	if errors.Is(err, unix.ENOTSUP) || n == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list extended attributes of %q: %w", f.Name(), err)
	}
	buf := make([]byte, n)
	if n, err = unix.Flistxattr(int(f.Fd()), buf); err != nil {
		return nil, fmt.Errorf("list extended attributes of %q: %w", f.Name(), err)
	}
	var names []string
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// extendedMetadata names the ACL or extended attributes f carries, or ""
// when it has none. listxattr does not show a macOS ACL, so the ACL is read
// with fgetattrlist(ATTR_CMN_EXTENDED_SECURITY): a non-empty attribute is
// an ACL.
func extendedMetadata(f *os.File) (string, error) {
	names, err := xattrNames(f)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if name != systemProvenance {
			return "the extended attribute " + strconv.Quote(name), nil
		}
	}
	al := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buf [512]byte
	// The reply is a u32 total length, then an attrreference_t {i32
	// offset, u32 length} for the security attribute.
	//nolint:staticcheck,gosec // x/sys/unix has no fgetattrlist wrapper on darwin; the pointers are to live locals of fixed size
	_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, f.Fd(),
		uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return "", fmt.Errorf("read ACL of %q: %w", f.Name(), errno)
	}
	if binary.LittleEndian.Uint32(buf[0:4]) >= 12 && binary.LittleEndian.Uint32(buf[8:12]) > 0 {
		return "an ACL", nil
	}
	return "", nil
}
