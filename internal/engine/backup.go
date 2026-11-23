package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"

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
	real := e.real(path)
	info, err := os.Lstat(real)
	if errors.Is(err, fs.ErrNotExist) {
		return ManifestEntry{Path: path, Kind: KindAbsent}, nil
	}
	if err != nil {
		return ManifestEntry{}, fmt.Errorf("stat %q: %w", path, err)
	}
	en := ManifestEntry{Path: path, Mode: info.Mode() & modeBits}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		en.UID, en.GID = int(st.Uid), int(st.Gid)
	}
	switch {
	case info.Mode().IsRegular():
		// #nosec G304 -- a declared path under the engine's root.
		data, err := os.ReadFile(real)
		if err != nil {
			return ManifestEntry{}, fmt.Errorf("read %q: %w", path, err)
		}
		en.Kind, en.Digest, en.Data = KindFile, digest(data), name
		if err := e.atomicWrite(filepath.Join(dir, name), fileSpec{data: data, perm: 0o600}); err != nil {
			return ManifestEntry{}, err
		}
	case info.Mode()&fs.ModeSymlink != 0:
		link, err := os.Readlink(real)
		if err != nil {
			return ManifestEntry{}, fmt.Errorf("read link %q: %w", path, err)
		}
		en.Kind, en.Link = KindSymlink, link
	default:
		return ManifestEntry{}, fmt.Errorf("%w: %q is %v", ErrUnsupportedTarget, path, info.Mode().Type())
	}
	return en, nil
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
	switch en.Kind {
	case KindAbsent:
		info, err := os.Lstat(real)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat %q: %w", en.Path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("%w: %q became a folder", ErrUnsupportedTarget, en.Path)
		}
		return removeDurable(real)
	case KindFile:
		// #nosec G304 -- a file in the run's own backup folder.
		data, err := os.ReadFile(filepath.Join(dir, en.Data))
		if err != nil {
			return fmt.Errorf("read backup of %q: %w", en.Path, err)
		}
		if digest(data) != en.Digest {
			return fmt.Errorf("%w: %q", ErrBackupMismatch, en.Path)
		}
		return e.atomicWrite(real, fileSpec{data: data, perm: en.Mode, owner: &contract.Owner{UID: en.UID, GID: en.GID}})
	case KindSymlink:
		if cur, err := os.Readlink(real); err == nil && cur == en.Link {
			return nil
		}
		if err := removeDurable(real); err != nil {
			return err
		}
		if err := os.Symlink(en.Link, real); err != nil {
			return fmt.Errorf("restore link %q: %w", en.Path, err)
		}
		return syncDir(filepath.Dir(real))
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
