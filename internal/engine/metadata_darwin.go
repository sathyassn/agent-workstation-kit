package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func isMissingXattrErr(err error) bool {
	return errors.Is(err, unix.ENOATTR) || errors.Is(err, unix.ENODATA)
}

// systemProvenance is the attribute macOS attaches by itself to files that
// ordinary processes write, the engine's own writes included. It records
// which process wrote the file for Gatekeeper; it is not file content the
// kit set, and the OS attaches it again to whatever the engine writes, so a
// backup omits it and restore validation ignores it.
const (
	systemProvenance = "com.apple.provenance"
	initialACLBufLen = 512
	maxACLBufLen     = 1 << 20
	attrRefHeaderLen = 8
	attrReplyMinLen  = 12
)

func readFileMetadata(f *os.File) (FileMetadata, error) {
	xStatus, xattrs, err := readAllXattrs(f, func(name string) bool {
		return name == systemProvenance
	})
	if err != nil {
		return FileMetadata{}, err
	}
	aclStatus, acl, err := readDarwinACL(f)
	if err != nil {
		return FileMetadata{}, err
	}
	return FileMetadata{
		XattrsStatus: xStatus,
		Xattrs:       xattrs,
		ACLStatus:    aclStatus,
		ACL:          acl,
	}, nil
}

func applyFileMetadata(f *os.File, m FileMetadata) error {
	if m.XattrsStatus == MetadataCaptured {
		for _, x := range m.Xattrs {
			if x.Name == systemProvenance {
				continue
			}
			if err := setOneXattr(f, x.Name, x.Value); err != nil {
				return err
			}
		}
	}
	if m.ACLStatus == MetadataCaptured && m.ACL != nil {
		if m.ACL.Kind != ACLKindDarwinFilesec {
			return fmt.Errorf("%w: cannot apply %q ACL on darwin", ErrRestoreMismatch, m.ACL.Kind)
		}
		if len(m.ACL.Access) > 0 {
			if err := applyDarwinACL(f, m.ACL.Access); err != nil {
				return err
			}
		}
	}
	return nil
}

// readDarwinACL reads a macOS file's extended security attribute
// (ATTR_CMN_EXTENDED_SECURITY) using fgetattrlist(2).
func readDarwinACL(f *os.File) (MetadataSupport, *FileACL, error) {
	al := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	bufLen := initialACLBufLen
	for range maxXattrRetries {
		buf := make([]byte, bufLen)
		// The reply is a u32 total length, then an attrreference_t {i32
		// offset, u32 length} for the extended security attribute.
		//nolint:staticcheck,gosec // x/sys/unix has no fgetattrlist wrapper on darwin; pointers refer to live local buffers
		_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, f.Fd(),
			uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		if errno != 0 {
			if isUnsupportedMetadataErr(errno) {
				return MetadataUnsupported, nil, nil
			}
			return "", nil, fmt.Errorf("read ACL of %q: %w", f.Name(), errno)
		}
		totalLen := int(binary.LittleEndian.Uint32(buf[0:4]))
		if totalLen > len(buf) {
			if totalLen > maxACLBufLen {
				return "", nil, fmt.Errorf("read ACL of %q: size %d exceeds limit %d", f.Name(), totalLen, maxACLBufLen)
			}
			bufLen = totalLen
			continue
		}
		if totalLen < attrReplyMinLen {
			return MetadataCaptured, nil, nil
		}
		rawOffset := binary.LittleEndian.Uint32(buf[4:8])
		// #nosec G115 -- attrreference_t.attr_dataoffset is a signed 32-bit offset in macOS sys/attr.h.
		dataOffset := int(int32(rawOffset))
		dataLen := int(binary.LittleEndian.Uint32(buf[8:12]))
		if dataLen == 0 {
			return MetadataCaptured, nil, nil
		}
		start := 4 + dataOffset
		end := start + dataLen
		if start < attrReplyMinLen || end > len(buf) || end > totalLen {
			return "", nil, fmt.Errorf("read ACL of %q: invalid attrreference offset=%d len=%d total=%d", f.Name(), dataOffset, dataLen, totalLen)
		}
		raw := make([]byte, dataLen)
		copy(raw, buf[start:end])
		return MetadataCaptured, &FileACL{Kind: ACLKindDarwinFilesec, Access: raw}, nil
	}
	return "", nil, fmt.Errorf("read ACL of %q: size kept changing", f.Name())
}

// applyDarwinACL writes a macOS extended security attribute
// (ATTR_CMN_EXTENDED_SECURITY) to f using fsetattrlist(2).
func applyDarwinACL(f *os.File, raw []byte) error {
	if len(raw) > maxACLBufLen {
		return fmt.Errorf("%w: ACL size %d exceeds limit %d", ErrRestoreMismatch, len(raw), maxACLBufLen)
	}
	al := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	// fsetattrlist input omits the leading u32 length: it starts directly with
	// attrreference_t {i32 offset=8, u32 length} followed by the kauth_filesec bytes.
	buf := make([]byte, attrRefHeaderLen+len(raw))
	binary.LittleEndian.PutUint32(buf[0:4], attrRefHeaderLen)
	// #nosec G115 -- len(raw) is bounded by maxACLBufLen (1 MiB), well within uint32.
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(raw)))
	copy(buf[attrRefHeaderLen:], raw)
	//nolint:staticcheck,gosec // x/sys/unix has no fsetattrlist wrapper on darwin; pointers refer to live local buffers
	_, _, errno := syscall.Syscall6(unix.SYS_FSETATTRLIST, f.Fd(),
		uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return fmt.Errorf("apply ACL to %q: %w", f.Name(), errno)
	}
	return nil
}
