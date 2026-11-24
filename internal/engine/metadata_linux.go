package engine

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func isMissingXattrErr(err error) bool {
	return errors.Is(err, unix.ENODATA)
}

// On Linux, POSIX ACLs are stored as extended attributes in the system
// namespace. Separating them in FileMetadata keeps the manifest explicit about
// both ACLs and ordinary extended attributes.
const (
	posixACLAccess  = "system.posix_acl_access"
	posixACLDefault = "system.posix_acl_default"
)

func readFileMetadata(f *os.File) (FileMetadata, error) {
	status, all, err := readAllXattrs(f, nil)
	if err != nil {
		return FileMetadata{}, err
	}
	if status == MetadataUnsupported {
		return FileMetadata{
			XattrsStatus: MetadataUnsupported,
			ACLStatus:    MetadataUnsupported,
		}, nil
	}
	var xattrs []XattrEntry
	var acl *FileACL
	for _, x := range all {
		switch x.Name {
		case posixACLAccess:
			if acl == nil {
				acl = &FileACL{Kind: ACLKindLinuxPOSIX}
			}
			acl.Access = x.Value
		case posixACLDefault:
			if acl == nil {
				acl = &FileACL{Kind: ACLKindLinuxPOSIX}
			}
			acl.Default = x.Value
		default:
			xattrs = append(xattrs, x)
		}
	}
	return FileMetadata{
		XattrsStatus: MetadataCaptured,
		Xattrs:       xattrs,
		ACLStatus:    MetadataCaptured,
		ACL:          acl,
	}, nil
}

func applyFileMetadata(f *os.File, m FileMetadata) error {
	if m.XattrsStatus == MetadataCaptured {
		for _, x := range m.Xattrs {
			if err := setOneXattr(f, x.Name, x.Value); err != nil {
				return err
			}
		}
	}
	if m.ACLStatus == MetadataCaptured {
		if m.ACL == nil {
			for _, name := range []string{posixACLAccess, posixACLDefault} {
				if err := unix.Fremovexattr(int(f.Fd()), name); err != nil && !isMissingXattrErr(err) && !isUnsupportedMetadataErr(err) {
					return fmt.Errorf("clear %s on %q: %w", name, f.Name(), err)
				}
			}
			return nil
		}
		if m.ACL.Kind != ACLKindLinuxPOSIX {
			return fmt.Errorf("%w: cannot apply %q ACL on linux", ErrRestoreMismatch, m.ACL.Kind)
		}
		if len(m.ACL.Access) > 0 {
			if err := setOneXattr(f, posixACLAccess, m.ACL.Access); err != nil {
				return err
			}
		}
		if len(m.ACL.Default) > 0 {
			if err := setOneXattr(f, posixACLDefault, m.ACL.Default); err != nil {
				return err
			}
		}
	}
	return nil
}
