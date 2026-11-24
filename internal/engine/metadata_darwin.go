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

// macOS kernel-managed extended attributes fall into two distinct classes:
//
//  1. OS-attached bookkeeping attributes on ordinary user files, excluded
//     consistently at backup, apply and verify because macOS attaches them
//     automatically and blocks user-space modification (EPERM or silent
//     rewriting):
//     - com.apple.provenance: Gatekeeper/EndpointSecurity writer tracking
//     attached to files written by ordinary processes, the engine included.
//     - com.apple.macl: TCC/Sandbox per-file Mandatory Access Control List
//     attached by macOS whenever a sandboxed app accesses a user file;
//     SIP prevents user-space setxattr/removexattr on it.
//
//  2. SIP/kernel-protected target markers whose presence means the target
//     itself cannot be safely replaced or restored by user-space processes;
//     refused at backup time (ErrProtectedMetadata, exit 2) before any change
//     or intent is written, and refused on restore if forged in a manifest:
//     - com.apple.rootless: System Integrity Protection restriction marker.
//     - com.apple.decmpfs: APFS/HFS+ kernel transparent compression store.
//     - com.apple.system.Security: kernel-managed ACL backing store (ACLs
//     are captured through ATTR_CMN_EXTENDED_SECURITY instead).
const (
	xattrDarwinProvenance = "com.apple.provenance"
	xattrDarwinMACL       = "com.apple.macl"
	xattrDarwinRootless   = "com.apple.rootless"
	xattrDarwinDecmpfs    = "com.apple.decmpfs"
	xattrDarwinSecurity   = "com.apple.system.Security"

	initialACLBufLen  = 512
	maxACLBufLen      = 1 << 20
	attrRefHeaderLen  = 8
	attrReplyMinLen   = 12
	kauthFilesecMagic = uint32(0x012cc16d)
	kauthFilesecNoACL = uint32(0xffffffff)
	kauthFilesecSize0 = 44 // magic(4) + owner(16) + group(16) + entrycount(4) + flags(4)
)

func isDarwinOSAttachedXattr(name []byte) bool {
	switch string(name) {
	case xattrDarwinProvenance, xattrDarwinMACL:
		return true
	default:
		return false
	}
}

func isDarwinRefusedXattr(name []byte) bool {
	switch string(name) {
	case xattrDarwinRootless, xattrDarwinDecmpfs, xattrDarwinSecurity:
		return true
	default:
		return false
	}
}

func isDarwinDroppedXattr(name []byte) bool {
	return string(name) == xattrDarwinMACL
}

func classifyDarwinXattr(path string, name []byte) (skip bool, err error) {
	if isDarwinRefusedXattr(name) {
		return false, fmt.Errorf("%w: %q carries %q", ErrProtectedMetadata, path, string(name))
	}
	if isDarwinOSAttachedXattr(name) {
		return true, nil
	}
	return false, nil
}

func validateXattrForOS(path string, name []byte) error {
	if isDarwinRefusedXattr(name) {
		return fmt.Errorf("%w: %q carries %q", ErrProtectedMetadata, path, string(name))
	}
	return nil
}

func validateACLForOS(acl *FileACL) error {
	if acl == nil || len(acl.Access) == 0 {
		return nil
	}
	if acl.Kind != ACLKindDarwinFilesec {
		return fmt.Errorf("%w: cannot apply %q ACL on darwin", ErrRestoreMismatch, acl.Kind)
	}
	if len(acl.Access) > maxACLBufLen {
		return fmt.Errorf("%w: ACL size %d exceeds limit %d", ErrRestoreMismatch, len(acl.Access), maxACLBufLen)
	}
	return nil
}

func readFileMetadata(f *os.File) (FileMetadata, error) {
	var dropped []string
	xStatus, xattrs, err := readAllXattrs(f, func(name []byte) (bool, error) {
		skip, cerr := classifyDarwinXattr(f.Name(), name)
		if cerr != nil {
			return false, cerr
		}
		if skip && isDarwinDroppedXattr(name) {
			dropped = append(dropped, string(name))
		}
		return skip, nil
	})
	if err != nil {
		return FileMetadata{}, err
	}
	aclStatus, acl, err := readDarwinACL(f)
	if err != nil {
		return FileMetadata{}, err
	}
	return FileMetadata{
		XattrsStatus:  xStatus,
		Xattrs:        xattrs,
		DroppedXattrs: dropped,
		ACLStatus:     aclStatus,
		ACL:           acl,
	}, nil
}

func applyFileMetadata(f *os.File, m FileMetadata) error {
	if m.XattrsStatus == MetadataCaptured {
		for _, x := range m.Xattrs {
			skip, err := classifyDarwinXattr(f.Name(), x.Name)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			if err := setOneXattr(f, x.Name, x.Value); err != nil {
				return err
			}
		}
	}
	if m.ACLStatus == MetadataCaptured {
		if m.ACL == nil || len(m.ACL.Access) == 0 {
			if err := clearDarwinACL(f); err != nil {
				return err
			}
			return nil
		}
		if err := validateACLForOS(m.ACL); err != nil {
			return err
		}
		if err := applyDarwinACL(f, m.ACL.Access); err != nil {
			return err
		}
	}
	return nil
}

// clearDarwinACL strips any ACL inherited from the parent folder on f by
// setting a 44-byte kauth_filesec with KAUTH_FILESEC_NOACL and null
// owner/group GUIDs via fsetattrlist(2).
func clearDarwinACL(f *os.File) error {
	var raw [kauthFilesecSize0]byte
	binary.LittleEndian.PutUint32(raw[0:4], kauthFilesecMagic)
	binary.LittleEndian.PutUint32(raw[36:40], kauthFilesecNoACL)
	return applyDarwinACL(f, raw[:])
}

// readDarwinACL reads a macOS file's extended security attribute
// (ATTR_CMN_EXTENDED_SECURITY) using fgetattrlist(2) with
// FSOPT_REPORT_FULLSIZE so ACLs larger than initialACLBufLen report their
// complete required size and grow the buffer instead of truncating.
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
		// FSOPT_REPORT_FULLSIZE guarantees totalLen reports the full required
		// buffer size even when the ACL exceeds len(buf).
		//nolint:staticcheck,gosec // x/sys/unix has no fgetattrlist wrapper on darwin; pointers refer to live local buffers
		_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, f.Fd(),
			uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
			uintptr(unix.FSOPT_REPORT_FULLSIZE), 0)
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
