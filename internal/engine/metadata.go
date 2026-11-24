package engine

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// MetadataSupport records whether an optional metadata class (extended
// attributes or ACLs) was captured from the filesystem or unavailable there.
type MetadataSupport string

// Metadata capture states stored in ManifestEntry.
const (
	// MetadataCaptured means the filesystem supports this metadata class and
	// the manifest holds the complete value (possibly empty when none was set).
	MetadataCaptured MetadataSupport = "captured"
	// MetadataUnsupported means the filesystem does not support this metadata
	// class (ENOTSUP / EOPNOTSUPP / ENOSYS), so nothing was captured.
	MetadataUnsupported MetadataSupport = "unsupported"
)

// ACLKind identifies the OS-native ACL encoding carried in FileACL.
type ACLKind string

// Supported ACL encodings.
const (
	ACLKindDarwinFilesec ACLKind = "darwin-filesec"
	ACLKindLinuxPOSIX    ACLKind = "linux-posix"
)

// XattrEntry is one extended attribute name and raw value.
type XattrEntry struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// FileACL holds an OS-native ACL blob captured from a file.
type FileACL struct {
	Kind    ACLKind `json:"kind"`
	Access  []byte  `json:"access,omitempty"`
	Default []byte  `json:"default,omitempty"`
}

// FileMetadata holds the extended attributes and ACL of a file.
type FileMetadata struct {
	XattrsStatus MetadataSupport
	Xattrs       []XattrEntry
	ACLStatus    MetadataSupport
	ACL          *FileACL
}

// metadataOps abstracts reading and applying extended file metadata so
// platform differences sit behind an interface and tests can simulate
// filesystems without xattr or ACL support.
type metadataOps interface {
	Read(f *os.File) (FileMetadata, error)
	Apply(f *os.File, m FileMetadata) error
}

type osMetadataOps struct{}

func (osMetadataOps) Read(f *os.File) (FileMetadata, error) {
	return readFileMetadata(f)
}

func (osMetadataOps) Apply(f *os.File, m FileMetadata) error {
	return applyFileMetadata(f, m)
}

const maxXattrRetries = 8

func isUnsupportedMetadataErr(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS)
}

// readAllXattrs reads every extended attribute on f except those for which
// skip returns true, sorted by name.
func readAllXattrs(f *os.File, skip func(string) bool) (MetadataSupport, []XattrEntry, error) {
	names, supported, err := listXattrNames(f)
	if err != nil {
		return "", nil, err
	}
	if !supported {
		return MetadataUnsupported, nil, nil
	}
	slices.Sort(names)
	var entries []XattrEntry
	for _, name := range names {
		if skip != nil && skip(name) {
			continue
		}
		val, err := readOneXattr(f, name)
		if isMissingXattrErr(err) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		entries = append(entries, XattrEntry{Name: name, Value: val})
	}
	return MetadataCaptured, entries, nil
}

func listXattrNames(f *os.File) ([]string, bool, error) {
	fd := int(f.Fd())
	for range maxXattrRetries {
		n, err := unix.Flistxattr(fd, nil)
		if isUnsupportedMetadataErr(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("list extended attributes of %q: %w", f.Name(), err)
		}
		if n == 0 {
			return nil, true, nil
		}
		buf := make([]byte, n)
		n, err = unix.Flistxattr(fd, buf)
		if errors.Is(err, unix.ERANGE) {
			continue
		}
		if isUnsupportedMetadataErr(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("list extended attributes of %q: %w", f.Name(), err)
		}
		var names []string
		for _, name := range strings.Split(string(buf[:n]), "\x00") {
			if name != "" {
				names = append(names, name)
			}
		}
		return names, true, nil
	}
	return nil, false, fmt.Errorf("list extended attributes of %q: size kept changing", f.Name())
}

func readOneXattr(f *os.File, name string) ([]byte, error) {
	fd := int(f.Fd())
	for range maxXattrRetries {
		n, err := unix.Fgetxattr(fd, name, nil)
		if err != nil {
			return nil, fmt.Errorf("read extended attribute %q of %q: %w", name, f.Name(), err)
		}
		if n == 0 {
			return []byte{}, nil
		}
		buf := make([]byte, n)
		n, err = unix.Fgetxattr(fd, name, buf)
		if errors.Is(err, unix.ERANGE) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read extended attribute %q of %q: %w", name, f.Name(), err)
		}
		return buf[:n], nil
	}
	return nil, fmt.Errorf("read extended attribute %q of %q: size kept changing", name, f.Name())
}

func setOneXattr(f *os.File, name string, val []byte) error {
	if val == nil {
		val = []byte{}
	}
	if err := unix.Fsetxattr(int(f.Fd()), name, val, 0); err != nil {
		return fmt.Errorf("set extended attribute %q on %q: %w", name, f.Name(), err)
	}
	return nil
}

func sortXattrs(in []XattrEntry) []XattrEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]XattrEntry, len(in))
	copy(out, in)
	slices.SortFunc(out, func(a, b XattrEntry) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

func equalXattrs(a, b []XattrEntry) bool {
	sa, sb := sortXattrs(a), sortXattrs(b)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i].Name != sb[i].Name || !bytes.Equal(sa[i].Value, sb[i].Value) {
			return false
		}
	}
	return true
}

func equalACL(a, b *FileACL) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Kind == b.Kind && bytes.Equal(a.Access, b.Access) && bytes.Equal(a.Default, b.Default)
}
