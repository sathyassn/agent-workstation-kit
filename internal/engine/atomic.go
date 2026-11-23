package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
// any moment leaves either the old file or the new one at path.
func (e *Engine) atomicWrite(path string, spec fileSpec) error {
	dir, base := filepath.Split(path)
	tmp, err := createTemp(dir, base)
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(spec.data); err != nil {
		return fail(fmt.Errorf("write %q: %w", name, err))
	}
	// chown before chmod: chown clears the setuid and setgid bits.
	if spec.owner != nil {
		if err := chownFile(tmp, *spec.owner); err != nil {
			return fail(err)
		}
	}
	if err := tmp.Chmod(spec.perm & modeBits); err != nil {
		return fail(fmt.Errorf("chmod %q: %w", name, err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("fsync %q: %w", name, err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close %q: %w", name, err)
	}
	e.fault(PointAtomicTemp)
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("rename %q: %w", path, err)
	}
	e.fault(PointAtomicRenamed)
	return syncDir(dir)
}

// createTemp creates an exclusive temporary file next to base. O_EXCL and a
// random name mean it never follows or reuses an existing file.
func createTemp(dir, base string) (*os.File, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("random temp name: %w", err)
	}
	name := filepath.Join(dir, "."+base+tempMarker+hex.EncodeToString(b[:]))
	// #nosec G304 -- the name is the target's folder plus a random suffix.
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create temp for %q: %w", base, err)
	}
	return f, nil
}

// removeTemps removes temporary files a kill left next to path.
func removeTemps(path string) error {
	dir, base := filepath.Split(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("list %q: %w", dir, err)
	}
	prefix := "." + base + tempMarker
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), prefix) {
			if err := os.Remove(filepath.Join(dir, ent.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale temp: %w", err)
			}
		}
	}
	return nil
}

// removeDurable removes path and fsyncs its folder. A missing path is not
// an error.
func removeDurable(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	if dir == "" {
		dir = "."
	}
	// #nosec G304 -- dir is a target's or the journal's own folder.
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open folder %q: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync folder %q: %w", dir, err)
	}
	return nil
}

func chownFile(f *os.File, o contract.Owner) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", f.Name(), err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == o.UID && int(st.Gid) == o.GID {
		return nil
	}
	if err := f.Chown(o.UID, o.GID); err != nil {
		return fmt.Errorf("%w: %d:%d on %q: %w", ErrOwnerChange, o.UID, o.GID, f.Name(), err)
	}
	return nil
}
