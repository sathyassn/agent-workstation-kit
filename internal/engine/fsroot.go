package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Every file operation the engine makes opens the target's folder by walking
// from "/" one component at a time with O_NOFOLLOW|O_DIRECTORY, then acts on
// the final name relative to that folder. A link anywhere on the path, or a
// folder swapped for a link after the engine started, fails the walk instead
// of redirecting the write. The configured roots are resolved once, by New,
// to the canonical paths the walk then follows.

// ErrPathLink is wrapped when a walk meets a link or a non-folder where a
// folder should be.
var ErrPathLink = errors.New("path holds a link or a non-folder")

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openDir opens the folder at the absolute path abs without following any
// link on the way.
func openDir(abs string) (*os.File, error) {
	if !filepath.IsAbs(abs) || filepath.Clean(abs) != abs {
		return nil, fmt.Errorf("%w: %q is not a clean absolute path", ErrPathLink, abs)
	}
	fd, err := unix.Open("/", dirFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("open /: %w", err)
	}
	for _, comp := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if comp == "" {
			continue
		}
		next, err := unix.Openat(fd, comp, dirFlags, 0)
		_ = unix.Close(fd)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: %q at %q: %w", ErrPathLink, abs, comp, err)
			}
			return nil, fmt.Errorf("open %q at %q: %w", abs, comp, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), abs), nil
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
