package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// Every file operation the engine makes opens the target's folder by walking
// from "/" one component at a time with O_NOFOLLOW|O_DIRECTORY, then acts on
// the final name relative to that folder. A link anywhere on the path, or a
// folder swapped for a link after the engine started, fails the walk instead
// of redirecting the write. The configured roots are resolved once, by New,
// to the canonical paths the walk then follows.

// ErrPathLink is wrapped when a walk meets a link or a non-folder where a
// folder should be. It is platform.ErrPathLink, so errors.Is matches either.
var ErrPathLink = platform.ErrPathLink

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openDir opens the folder at the absolute path abs without following any
// link on the way (platform.OpenDirNoFollow).
func openDir(abs string) (*os.File, error) {
	d, err := platform.OpenDirNoFollow(abs)
	if err != nil {
		return nil, fmt.Errorf("walk: %w", err)
	}
	return d, nil
}

// openTargetDir opens the folder at abs without following links and refuses
// crossing any walked component whose (dev, ino) matches a resolved
// package-database directory.
func (e *Engine) openTargetDir(abs string) (*os.File, error) {
	d, err := platform.OpenDirNoFollowFunc(abs, e.checkPackageDBFD)
	if err != nil {
		return nil, fmt.Errorf("walk: %w", err)
	}
	return d, nil
}

// parent is an open folder and a name inside it.
type parent struct {
	dir  *os.File
	name string
}

func openParent(abs string) (parent, error) {
	d, name := filepath.Split(abs)
	if name == "" {
		return parent{}, fmt.Errorf("%w: %q has no final name", ErrPathLink, abs)
	}
	dir, err := openDir(filepath.Clean(d))
	if err != nil {
		return parent{}, err
	}
	return parent{dir: dir, name: name}, nil
}

func (e *Engine) openParent(abs string) (parent, error) {
	d, name := filepath.Split(abs)
	if name == "" {
		return parent{}, fmt.Errorf("%w: %q has no final name", ErrPathLink, abs)
	}
	dir, err := e.openTargetDir(filepath.Clean(d))
	if err != nil {
		return parent{}, err
	}
	return parent{dir: dir, name: name}, nil
}

func (p parent) close()       { _ = p.dir.Close() }
func (p parent) fd() int      { return int(p.dir.Fd()) }
func (p parent) path() string { return filepath.Join(p.dir.Name(), p.name) }

// lstat stats the name without following it. A missing name is
// fs.ErrNotExist.
func (p parent) lstat() (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(p.fd(), p.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return st, fmt.Errorf("stat %q: %w", p.path(), fs.ErrNotExist)
		}
		return st, fmt.Errorf("stat %q: %w", p.path(), err)
	}
	return st, nil
}

// open opens the name for reading without following a link.
func (p parent) open() (*os.File, error) {
	fd, err := unix.Openat(p.fd(), p.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("open %q: %w", p.path(), fs.ErrNotExist)
		}
		return nil, fmt.Errorf("open %q: %w", p.path(), err)
	}
	return os.NewFile(uintptr(fd), p.path()), nil
}

// openChecked opens the regular file at p without following links, fstats the
// opened descriptor, and refuses if dev, ino or file type changed since st.
// Callers must derive every file attribute (mode, owner, size, metadata and
// content) exclusively from the returned descriptor and stat result.
func (p parent) openChecked(st unix.Stat_t) (*os.File, unix.Stat_t, error) {
	f, err := p.open()
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	var fst unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &fst); err != nil {
		_ = f.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("fstat %q: %w", p.path(), err)
	}
	if fst.Dev != st.Dev || fst.Ino != st.Ino || (fst.Mode&unix.S_IFMT) != (st.Mode&unix.S_IFMT) {
		_ = f.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("%w: %q dev/ino/type changed after stat", ErrTargetChanged, p.path())
	}
	return f, fst, nil
}

// checkWritable checks that this process has write and search access on p's
// opened parent directory without creating or modifying any entry.
func (p parent) checkWritable() error {
	if err := unix.Faccessat(p.fd(), ".", unix.W_OK|unix.X_OK, 0); err != nil {
		return fmt.Errorf("parent folder %q is not writable: %w", p.dir.Name(), err)
	}
	return nil
}

// errLinkTarget explains why a link target is refused.
const errLinkTarget = "is a symbolic link; the unit description cannot declare link handling yet, so link targets are refused"

// refuseUnsafeTarget runs before a target is replaced or removed. A missing
// name or regular file is accepted. A symbolic link, a folder or any other
// type is refused (ErrUnsupportedTarget).
func (p parent) refuseUnsafeTarget() error {
	st, err := p.lstat()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return nil
	case unix.S_IFLNK:
		return fmt.Errorf("%w: %q %s", ErrUnsupportedTarget, p.path(), errLinkTarget)
	default:
		return fmt.Errorf("%w: %q is not a regular file", ErrUnsupportedTarget, p.path())
	}
}

func (p parent) readlink() (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(p.fd(), p.name, buf)
	if err != nil {
		return "", fmt.Errorf("read link %q: %w", p.path(), err)
	}
	return string(buf[:n]), nil
}

func (p parent) sync() error {
	if err := p.dir.Sync(); err != nil {
		return fmt.Errorf("fsync folder %q: %w", p.dir.Name(), err)
	}
	return nil
}

// readFileAt reads abs without following a link anywhere on its path.
func readFileAt(abs string) ([]byte, error) {
	p, err := openParent(abs)
	if err != nil {
		return nil, err
	}
	defer p.close()
	f, err := p.open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", abs, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular file", ErrUnsupportedTarget, abs)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", abs, err)
	}
	return data, nil
}

// canonical resolves the operator-configured path p once, at startup.
func canonical(p string) (string, error) {
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", p, err)
	}
	return c, nil
}
