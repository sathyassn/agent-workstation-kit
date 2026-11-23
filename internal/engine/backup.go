package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// EntryKind is what a declared path was before the apply.
type EntryKind string

// Entry kinds.
const (
	KindAbsent  EntryKind = "absent"
	KindFile    EntryKind = "file"
	KindSymlink EntryKind = "symlink"
)

// ManifestEntry is the backup of one declared path.
type ManifestEntry struct {
	Path   string          `json:"path"`
	Kind   EntryKind       `json:"kind"`
	Mode   fs.FileMode     `json:"mode,omitempty"`
	UID    int             `json:"uid,omitempty"`
	GID    int             `json:"gid,omitempty"`
	Digest contract.Digest `json:"digest,omitempty"`
	Data   string          `json:"data,omitempty"`
	Link   string          `json:"link,omitempty"`
}

// Manifest lists the backups of a run. It is written last, so it exists only
// for a complete backup.
type Manifest struct {
	RunID   contract.RunID  `json:"run_id"`
	Unit    contract.UnitID `json:"unit"`
	Entries []ManifestEntry `json:"entries"`
}

func (m *Manifest) entry(path string) (ManifestEntry, bool) {
	for _, en := range m.Entries {
		if en.Path == path {
			return en, true
		}
	}
	return ManifestEntry{}, false
}

func digest(data []byte) contract.Digest {
	sum := sha256.Sum256(data)
	return contract.Digest(hex.EncodeToString(sum[:]))
}

// backup copies every declared path, with its mode and owner, into the run's
// backup folder, then writes the manifest.
func (e *Engine) backup(ctx context.Context, runID contract.RunID, desc contract.Description) (*Manifest, error) {
	dir := e.backupDir(runID)
	if err := os.MkdirAll(dir, privateMode); err != nil {
		return nil, fmt.Errorf("create backup folder: %w", err)
	}
	paths, err := declaredPaths(desc)
	if err != nil {
		return nil, err
	}
	m := &Manifest{RunID: runID, Unit: desc.Unit}
	for i, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("backup: %w", err)
		}
		en, err := e.backupOne(dir, strconv.Itoa(i), p)
		if err != nil {
			return nil, err
		}
		m.Entries = append(m.Entries, en)
		e.fault(PointBackupEntry)
	}
	if err := e.writeJSON(filepath.Join(dir, manifestFile), m); err != nil {
		return nil, err
	}
	return m, nil
}

func (e *Engine) backupOne(dir, name, path string) (ManifestEntry, error) {
	p, err := openParent(e.real(path))
	if err != nil {
		return ManifestEntry{}, err
	}
	defer p.close()
	st, err := p.lstat()
	if errors.Is(err, fs.ErrNotExist) {
		return ManifestEntry{Path: path, Kind: KindAbsent}, nil
	}
	if err != nil {
		return ManifestEntry{}, err
	}
	en := ManifestEntry{Path: path, Mode: unixMode(st), UID: int(st.Uid), GID: int(st.Gid)}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		f, err := p.open()
		if err != nil {
			return ManifestEntry{}, err
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return ManifestEntry{}, fmt.Errorf("read %q: %w", path, err)
		}
		en.Kind, en.Digest, en.Data = KindFile, digest(data), name
		if err := e.atomicWrite(filepath.Join(dir, name), fileSpec{data: data, perm: 0o600}); err != nil {
			return ManifestEntry{}, err
		}
	case unix.S_IFLNK:
		link, err := p.readlink()
		if err != nil {
			return ManifestEntry{}, err
		}
		en.Kind, en.Link = KindSymlink, link
	default:
		return ManifestEntry{}, fmt.Errorf("%w: %q has file type %o", ErrUnsupportedTarget, path, st.Mode&unix.S_IFMT)
	}
	return en, nil
}

// unixMode converts a raw stat mode to the bits a backup keeps.
func unixMode(st unix.Stat_t) fs.FileMode {
	m := fs.FileMode(st.Mode) & fs.ModePerm
	if st.Mode&unix.S_ISUID != 0 {
		m |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		m |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		m |= fs.ModeSticky
	}
	return m
}

func (e *Engine) readManifest(runID contract.RunID) (*Manifest, error) {
	var m Manifest
	path := filepath.Join(e.backupDir(runID), manifestFile)
	found, err := readJSON(path, &m)
	if err != nil {
		return nil, err
	}
	if !found || m.RunID != runID {
		return nil, fmt.Errorf("%w: no manifest for run %q", ErrCorruptRecord, runID)
	}
	return &m, nil
}

// restore puts every manifest entry back, in reverse order. It is
// idempotent: a restore a kill interrupted is finished by running it again.
func (e *Engine) restore(ctx context.Context, m *Manifest) error {
	dir := e.backupDir(m.RunID)
	for _, en := range slices.Backward(m.Entries) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		if err := e.restoreOne(dir, en); err != nil {
			return err
		}
		e.fault(PointRestoreEntry)
	}
	return nil
}

func (e *Engine) restoreOne(dir string, en ManifestEntry) error {
	real := e.real(en.Path)
	if err := removeTemps(real); err != nil {
		return err
	}
	p, err := openParent(real)
	if err != nil {
		return err
	}
	defer p.close()
	switch en.Kind {
	case KindAbsent:
		st, err := p.lstat()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			return fmt.Errorf("%w: %q became a folder", ErrUnsupportedTarget, en.Path)
		}
		if err := unix.Unlinkat(p.fd(), p.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove %q: %w", en.Path, err)
		}
		return e.syncDir(p)
	case KindFile:
		data, err := readFileAt(filepath.Join(dir, en.Data))
		if err != nil {
			return fmt.Errorf("read backup of %q: %w", en.Path, err)
		}
		if digest(data) != en.Digest {
			return fmt.Errorf("%w: %q", ErrBackupMismatch, en.Path)
		}
		return e.atomicWriteAt(p, fileSpec{data: data, perm: en.Mode, owner: &contract.Owner{UID: en.UID, GID: en.GID}})
	case KindSymlink:
		if cur, err := p.readlink(); err == nil && cur == en.Link {
			return nil
		}
		if err := unix.Unlinkat(p.fd(), p.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove %q: %w", en.Path, err)
		}
		if err := unix.Symlinkat(en.Link, p.fd(), p.name); err != nil {
			return fmt.Errorf("restore link %q: %w", en.Path, err)
		}
		e.trace(traceEntry, p.dir.Name(), p.name)
		return e.syncDir(p)
	default:
		return fmt.Errorf("%w: entry kind %q", ErrCorruptRecord, en.Kind)
	}
}

// declaredPaths returns the description's paths, cleaned, sorted and
// without repeats. Every path must be absolute.
func declaredPaths(desc contract.Description) ([]string, error) {
	out := make([]string, 0, len(desc.OwnedPaths))
	for _, p := range desc.OwnedPaths {
		c, err := cleanPath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func cleanPath(p string) (string, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return "", &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: p}
	}
	return p, nil
}
