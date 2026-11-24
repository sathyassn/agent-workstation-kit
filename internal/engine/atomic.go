package engine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// tempMarker is in the name of every temporary file the engine creates next
// to a target, so a restore can find and remove those a kill left behind.
const tempMarker = ".wskit-tmp-"

// modeBits are the mode bits a backup keeps and a write sets: permissions
// plus setuid, setgid and sticky.
const modeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// fileSpec is what atomicWrite puts in place.
type fileSpec struct {
	data  []byte
	perm  fs.FileMode
	owner *contract.Owner // nil keeps the creating process's owner
}

// atomicWrite replaces path with spec: it writes a temporary file in the same
// folder, fsyncs it, renames it over path and fsyncs the folder. A kill at
// any moment leaves either the old file or the new one at path. The folder
// is opened without following links (fsroot.go).
func (e *Engine) atomicWrite(path string, spec fileSpec) error {
	p, err := openParent(path)
	if err != nil {
		return err
	}
	defer p.close()
	return e.atomicWriteAt(p, spec)
}

func (e *Engine) atomicWriteAt(p parent, spec fileSpec) error {
	tmpName, tmp, err := createTemp(p)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tmp.Close()
		_ = unix.Unlinkat(p.fd(), tmpName, 0)
		return err
	}
	if _, err := tmp.Write(spec.data); err != nil {
		return fail(fmt.Errorf("write %q: %w", tmp.Name(), err))
	}
	// chown before chmod: chown clears the setuid and setgid bits.
	if spec.owner != nil {
		if err := chownFile(tmp, *spec.owner); err != nil {
			return fail(err)
		}
	}
	if err := tmp.Chmod(spec.perm & modeBits); err != nil {
		return fail(fmt.Errorf("chmod %q: %w", tmp.Name(), err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("fsync %q: %w", tmp.Name(), err))
	}
	if err := tmp.Close(); err != nil {
		_ = unix.Unlinkat(p.fd(), tmpName, 0)
		return fmt.Errorf("close %q: %w", tmp.Name(), err)
	}
	e.fault(PointAtomicTemp)
	if err := unix.Renameat(p.fd(), tmpName, p.fd(), p.name); err != nil {
		_ = unix.Unlinkat(p.fd(), tmpName, 0)
		return fmt.Errorf("rename %q: %w", p.path(), err)
	}
	e.trace(traceEntry, p.dir.Name(), p.name)
	e.fault(PointAtomicRenamed)
	return e.syncDir(p)
}

// syncDir fsyncs p's folder and notes it for the durability model in tests.
func (e *Engine) syncDir(p parent) error {
	if err := p.sync(); err != nil {
		return err
	}
	e.trace(traceSync, p.dir.Name(), "")
	return nil
}

// createTemp creates an exclusive temporary file next to p's name. O_EXCL,
// O_NOFOLLOW and a random name mean it never follows or reuses a file.
func createTemp(p parent) (string, *os.File, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("random temp name: %w", err)
	}
	name := "." + p.name + tempMarker + hex.EncodeToString(b[:])
	fd, err := unix.Openat(p.fd(), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("create temp for %q: %w", p.path(), err)
	}
	return name, os.NewFile(uintptr(fd), p.dir.Name()+"/"+name), nil
}

// removeTemps removes temporary files a kill left next to path. A missing
// folder has none.
func removeTemps(path string) error {
	p, err := openParent(path)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer p.close()
	names, err := listDir(p.dir)
	if err != nil {
		return err
	}
	prefix := "." + p.name + tempMarker
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			if err := unix.Unlinkat(p.fd(), n, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("remove stale temp %q: %w", n, err)
			}
		}
	}
	return nil
}

// listDir lists an open folder's names through a duplicate descriptor, so
// the caller's descriptor keeps its position and stays open.
func listDir(dir *os.File) ([]string, error) {
	fd, err := unix.Openat(int(dir.Fd()), ".", dirFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("reopen folder %q: %w", dir.Name(), err)
	}
	d := os.NewFile(uintptr(fd), dir.Name())
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("list folder %q: %w", dir.Name(), err)
	}
	return names, nil
}

// mkdirDurable creates the folder path if it is missing and fsyncs its
// parent, so the new entry survives power loss. An existing entry must be a
// real folder, not a link.
func (e *Engine) mkdirDurable(path string) error {
	p, err := openParent(path)
	if err != nil {
		return err
	}
	defer p.close()
	err = unix.Mkdirat(p.fd(), p.name, privateMode)
	switch {
	case err == nil:
		e.trace(traceEntry, p.dir.Name(), p.name)
	case errors.Is(err, unix.EEXIST):
		st, statErr := p.lstat()
		if statErr != nil {
			return statErr
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("%w: %q is not a folder", ErrPathLink, path)
		}
	default:
		return fmt.Errorf("create folder %q: %w", path, err)
	}
	return e.syncDir(p)
}

// mkdirAllDurable creates path and every missing ancestor, each with mkdir
// and an fsync of its parent, so no new folder entry is lost to power loss.
// The deepest existing ancestor is resolved once; each new folder is then
// created through the no-follow walk.
func (e *Engine) mkdirAllDurable(path string) error {
	var missing []string
	p := filepath.Clean(path)
	for {
		_, err := os.Lstat(p)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) || p == "/" {
			return fmt.Errorf("stat %q: %w", p, err)
		}
		missing = append(missing, filepath.Base(p))
		p = filepath.Dir(p)
	}
	base, err := canonical(p)
	if err != nil {
		return err
	}
	for _, name := range slices.Backward(missing) {
		base = filepath.Join(base, name)
		if err := e.mkdirDurable(base); err != nil {
			return err
		}
	}
	return nil
}

func chownFile(f *os.File, o contract.Owner) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", f.Name(), err)
	}
	if st, ok := info.Sys().(*unix.Stat_t); ok && int(st.Uid) == o.UID && int(st.Gid) == o.GID {
		return nil
	}
	if err := f.Chown(o.UID, o.GID); err != nil {
		return fmt.Errorf("%w: %d:%d on %q: %w", ErrOwnerChange, o.UID, o.GID, f.Name(), err)
	}
	return nil
}

// Trace events for the durability model in tests: a folder entry was added
// or replaced, or a folder was fsynced.
const (
	traceEntry = "entry"
	traceSync  = "sync"
)

func (e *Engine) trace(op, dir, name string) {
	if e.opts.traceFn != nil {
		e.opts.traceFn(op, dir, name)
	}
}
