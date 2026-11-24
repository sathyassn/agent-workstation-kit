package engine

import (
	"bytes"
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
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// EntryKind is what a declared path was before the apply.
type EntryKind string

// Entry kinds.
const (
	KindAbsent  EntryKind = "absent"
	KindFile    EntryKind = "file"
	KindSymlink EntryKind = "symlink"
)

const stateBackupPrefix = "state-"

// ManifestEntry is the full backup of one declared path, including owner,
// mode, extended attributes, ACLs and content digest.
type ManifestEntry struct {
	Path         string          `json:"path"`
	Kind         EntryKind       `json:"kind"`
	Mode         fs.FileMode     `json:"mode,omitempty"`
	UID          int             `json:"uid,omitempty"`
	GID          int             `json:"gid,omitempty"`
	Digest       contract.Digest `json:"digest,omitempty"`
	Data         string          `json:"data,omitempty"`
	Link         string          `json:"link,omitempty"`
	XattrsStatus MetadataSupport `json:"xattrs_status,omitempty"`
	Xattrs       []XattrEntry    `json:"xattrs,omitempty"`
	ACLStatus    MetadataSupport `json:"acl_status,omitempty"`
	ACL          *FileACL        `json:"acl,omitempty"`
}

func (en ManifestEntry) fileMetadata() FileMetadata {
	return FileMetadata{
		XattrsStatus: en.XattrsStatus,
		Xattrs:       en.Xattrs,
		ACLStatus:    en.ACLStatus,
		ACL:          en.ACL,
	}
}

// ManifestState is the backup of one StateProvider taken before any mutation
// to that provider during the run.
type ManifestState struct {
	Provider contract.StateProviderName `json:"provider"`
	Digest   contract.Digest            `json:"digest"`
	Data     string                     `json:"data"`
}

// Manifest lists the backups of a run. It is written and fsynced last, so it
// exists only for a complete backup.
type Manifest struct {
	RunID   contract.RunID  `json:"run_id"`
	Unit    contract.UnitID `json:"unit"`
	Entries []ManifestEntry `json:"entries"`
	States  []ManifestState `json:"states,omitempty"`
}

func (m *Manifest) entry(path string) (ManifestEntry, bool) {
	for _, en := range m.Entries {
		if en.Path == path {
			return en, true
		}
	}
	return ManifestEntry{}, false
}

func (m *Manifest) hasState(name contract.StateProviderName) bool {
	for _, st := range m.States {
		if st.Provider == name {
			return true
		}
	}
	return false
}

func digest(data []byte) contract.Digest {
	sum := sha256.Sum256(data)
	return contract.Digest(hex.EncodeToString(sum[:]))
}

// backup copies every declared path (with owner, mode, xattrs, ACLs and
// digest) and snapshots every matching registered state provider into the
// run's backup folder, then writes and fsyncs manifest.json last.
func (e *Engine) backup(ctx context.Context, runID contract.RunID, desc contract.Description) (*Manifest, error) {
	paths, err := declaredPaths(desc)
	if err != nil {
		return nil, err
	}
	dir := e.backupDir(runID)
	if err := e.mkdirDurable(dir); err != nil {
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
	for i, name := range e.matchedProviders(desc.OwnedStateKeys) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("backup state %s: %w", name, err)
		}
		p := e.opts.Providers[name]
		snap, err := p.Snapshot(ctx)
		if err != nil {
			return nil, fmt.Errorf("snapshot state %s: %w", name, err)
		}
		if snap.Provider != name {
			return nil, fmt.Errorf("%w: provider %q returned snapshot for %q", platform.ErrSnapshotProviderMismatch, name, snap.Provider)
		}
		fileName := stateBackupPrefix + strconv.Itoa(i)
		if err := e.atomicWrite(filepath.Join(dir, fileName), fileSpec{data: snap.Data, perm: 0o600}); err != nil {
			return nil, err
		}
		m.States = append(m.States, ManifestState{
			Provider: name,
			Digest:   digest(snap.Data),
			Data:     fileName,
		})
		e.fault(PointBackupEntry)
	}
	if err := e.writeJSON(filepath.Join(dir, manifestFile), m); err != nil {
		return nil, err
	}
	return m, nil
}

// matchedProviders returns the sorted names of registered providers that own
// at least one key in keys.
func (e *Engine) matchedProviders(keys []contract.StateKey) []contract.StateProviderName {
	if len(e.opts.Providers) == 0 || len(keys) == 0 {
		return nil
	}
	seen := map[contract.StateProviderName]bool{}
	var out []contract.StateProviderName
	for _, k := range keys {
		name := e.providerNameForKey(k)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func (e *Engine) providerNameForKey(key contract.StateKey) contract.StateProviderName {
	s := string(key)
	if strings.HasPrefix(s, keyPackage) {
		if _, ok := e.lookupProvider(contract.ProviderPackages); ok {
			return contract.ProviderPackages
		}
		return ""
	}
	e.provMu.RLock()
	defer e.provMu.RUnlock()
	for name := range e.opts.Providers {
		ns := string(name)
		if s == ns || strings.HasPrefix(s, ns+".") || strings.HasPrefix(s, ns+":") {
			return name
		}
	}
	return ""
}

func (e *Engine) backupOne(dir, name, path string) (ManifestEntry, error) {
	p, err := e.openParent(e.real(path))
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
	if err := e.checkPackageDBStat(st, path); err != nil {
		return ManifestEntry{}, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if e.opts.afterLstat != nil {
			e.opts.afterLstat(path)
		}
		f, fst, err := p.openChecked(st)
		if err != nil {
			return ManifestEntry{}, err
		}
		if err := e.checkPackageDBStat(fst, path); err != nil {
			_ = f.Close()
			return ManifestEntry{}, err
		}
		// Take every attribute (mode, owner, size, xattrs, ACLs and content)
		// from the opened descriptor so a concurrent swap cannot split them.
		en := ManifestEntry{
			Path: path,
			Kind: KindFile,
			Mode: unixMode(fst),
			UID:  int(fst.Uid),
			GID:  int(fst.Gid),
			Data: name,
		}
		meta, metaErr := e.meta().Read(f)
		if metaErr != nil {
			_ = f.Close()
			return ManifestEntry{}, fmt.Errorf("read metadata of %q: %w", path, metaErr)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return ManifestEntry{}, fmt.Errorf("read %q: %w", path, err)
		}
		if int64(len(data)) != fst.Size {
			return ManifestEntry{}, fmt.Errorf("%w: %q size changed during read (%d vs %d)", ErrTargetChanged, path, len(data), fst.Size)
		}
		en.Digest = digest(data)
		en.XattrsStatus, en.Xattrs = meta.XattrsStatus, meta.Xattrs
		en.ACLStatus, en.ACL = meta.ACLStatus, meta.ACL
		if err := e.atomicWrite(filepath.Join(dir, name), fileSpec{data: data, perm: 0o600}); err != nil {
			return ManifestEntry{}, err
		}
		return en, nil
	case unix.S_IFLNK:
		return ManifestEntry{}, fmt.Errorf("%w: %q %s", ErrUnsupportedTarget, path, errLinkTarget)
	default:
		return ManifestEntry{}, fmt.Errorf("%w: %q has file type %o", ErrUnsupportedTarget, path, st.Mode&unix.S_IFMT)
	}
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

// restore validates the whole manifest, replays every state snapshot and file
// entry in m in reverse order, verifies content and metadata against the
// manifest, and runs every configured RestoreValidator. It is idempotent: a
// restore a kill interrupted is finished by running it again. Every error is
// wrapped in ErrRestoreFailed so it fails closed with exit 4 and leaves the
// intent unfinished.
func (e *Engine) restore(ctx context.Context, m *Manifest) error {
	if err := e.restoreInner(ctx, m); err != nil {
		return fmt.Errorf("%w: %w", ErrRestoreFailed, err)
	}
	return nil
}

func (e *Engine) restoreInner(ctx context.Context, m *Manifest) error {
	dir := e.backupDir(m.RunID)
	if err := e.validateManifestForRestore(dir, m); err != nil {
		return err
	}
	for _, st := range slices.Backward(m.States) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restore state %s: %w", st.Provider, err)
		}
		if err := e.restoreState(ctx, dir, st); err != nil {
			return err
		}
		e.fault(PointRestoreEntry)
	}
	for _, en := range slices.Backward(m.Entries) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		if err := e.restoreOne(dir, en); err != nil {
			return err
		}
		if err := e.verifyRestoredEntry(en); err != nil {
			return err
		}
		e.fault(PointRestoreEntry)
	}
	for _, v := range e.opts.Validators {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate restore: %w", err)
		}
		if err := v.Validate(ctx); err != nil {
			return fmt.Errorf("%w: validator %q: %w", ErrRestoreValidation, v.Name(), err)
		}
	}
	return nil
}

// validateManifestForRestore checks the whole manifest and every live target's
// restorability (parent accessibility/writability, absence of symlinks or
// package-database aliases, incompatible live file types such as an absent
// target that became a directory, metadata validity, backup digests and state
// provider registration) before replaying any state snapshot or file entry, so
// pre-validation failure exits 4 with zero state or file mutations.
func (e *Engine) validateManifestForRestore(dir string, m *Manifest) error {
	for _, en := range m.Entries {
		if isPackageDBPath(en.Path) {
			return fmt.Errorf("%w: %w: %q", ErrRestoreMismatch, ErrPackageDBPath, en.Path)
		}
		for _, xa := range en.Xattrs {
			if len(xa.Name) == 0 || bytes.IndexByte(xa.Name, 0) >= 0 {
				return fmt.Errorf("%w: %q has invalid extended attribute name", ErrCorruptRecord, en.Path)
			}
			if err := validateXattrForOS(en.Path, xa.Name); err != nil {
				return err
			}
		}
		if en.ACLStatus == MetadataCaptured {
			if err := validateACLForOS(en.ACL); err != nil {
				return err
			}
		}
		switch en.Kind {
		case KindAbsent, KindSymlink:
		case KindFile:
			data, err := readFileAt(filepath.Join(dir, en.Data))
			if err != nil {
				return fmt.Errorf("read backup of %q: %w", en.Path, err)
			}
			if digest(data) != en.Digest {
				return fmt.Errorf("%w: %q", ErrBackupMismatch, en.Path)
			}
		default:
			return fmt.Errorf("%w: entry kind %q", ErrCorruptRecord, en.Kind)
		}
		if err := e.validateTargetRestorable(en); err != nil {
			return err
		}
	}
	for _, st := range m.States {
		data, err := readFileAt(filepath.Join(dir, st.Data))
		if err != nil {
			return fmt.Errorf("%w: read state backup %q: %w", ErrRestoreMismatch, st.Provider, err)
		}
		if digest(data) != st.Digest {
			return fmt.Errorf("%w: state %q", ErrBackupMismatch, st.Provider)
		}
		if _, ok := e.lookupProvider(st.Provider); !ok {
			return fmt.Errorf("%w: no state provider registered for %q", ErrRestoreMismatch, st.Provider)
		}
	}
	return nil
}

// validateTargetRestorable checks live filesystem preconditions for en without
// modifying anything on disk.
func (e *Engine) validateTargetRestorable(en ManifestEntry) error {
	p, err := e.openParent(e.real(en.Path))
	if err != nil {
		return err
	}
	defer p.close()
	st, statErr := p.lstat()
	exists := false
	switch {
	case statErr == nil:
		exists = true
		if err := e.checkPackageDBStat(st, en.Path); err != nil {
			return err
		}
	case errors.Is(statErr, fs.ErrNotExist):
	default:
		return statErr
	}
	switch en.Kind {
	case KindAbsent:
		if exists && st.Mode&unix.S_IFMT == unix.S_IFDIR {
			return fmt.Errorf("%w: %q became a folder", ErrUnsupportedTarget, en.Path)
		}
		stale, err := hasStaleTemps(p)
		if err != nil {
			return err
		}
		if exists || stale {
			return p.checkWritable()
		}
		return nil
	case KindFile, KindSymlink:
		if exists && st.Mode&unix.S_IFMT == unix.S_IFDIR {
			return fmt.Errorf("%w: %q became a folder", ErrUnsupportedTarget, en.Path)
		}
		return p.checkWritable()
	default:
		return fmt.Errorf("%w: entry kind %q", ErrCorruptRecord, en.Kind)
	}
}

func (e *Engine) restoreState(ctx context.Context, dir string, st ManifestState) error {
	data, err := readFileAt(filepath.Join(dir, st.Data))
	if err != nil {
		return fmt.Errorf("%w: read state backup %q: %w", ErrRestoreMismatch, st.Provider, err)
	}
	if digest(data) != st.Digest {
		return fmt.Errorf("%w: state %q", ErrBackupMismatch, st.Provider)
	}
	p, ok := e.lookupProvider(st.Provider)
	if !ok {
		return fmt.Errorf("%w: no state provider registered for %q", ErrRestoreMismatch, st.Provider)
	}
	if err := p.Restore(ctx, contract.Snapshot{Provider: st.Provider, Data: data}); err != nil {
		return fmt.Errorf("%w: restore state %q: %w", ErrRestoreMismatch, st.Provider, err)
	}
	e.fault(PointStateRestore)
	after, err := p.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("%w: verify restored state %q: %w", ErrRestoreMismatch, st.Provider, err)
	}
	if after.Provider != st.Provider || !bytes.Equal(after.Data, data) {
		return fmt.Errorf("%w: state %q does not match manifest after restore", ErrRestoreMismatch, st.Provider)
	}
	return nil
}

func (e *Engine) restoreOne(dir string, en ManifestEntry) error {
	real := e.real(en.Path)
	if err := e.removeTemps(real); err != nil {
		return err
	}
	p, err := e.openParent(real)
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
		meta := en.fileMetadata()
		return e.atomicWriteAt(p, fileSpec{
			data:  data,
			perm:  en.Mode,
			owner: &contract.Owner{UID: en.UID, GID: en.GID},
			meta:  &meta,
		})
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

// verifyRestoredEntry re-reads the restored target from disk without
// following links and verifies its type, content digest, owner, mode,
// extended attributes and ACLs against en.
func (e *Engine) verifyRestoredEntry(en ManifestEntry) error {
	p, err := e.openParent(e.real(en.Path))
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrRestoreMismatch, en.Path, err)
	}
	defer p.close()
	switch en.Kind {
	case KindAbsent:
		if _, err := p.lstat(); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %q still exists after restore", ErrRestoreMismatch, en.Path)
		}
		return nil
	case KindFile:
		st, err := p.lstat()
		if err != nil {
			return fmt.Errorf("%w: stat %q: %w", ErrRestoreMismatch, en.Path, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("%w: %q is not a regular file after restore", ErrRestoreMismatch, en.Path)
		}
		f, fst, err := p.openChecked(st)
		if err != nil {
			return fmt.Errorf("%w: open %q: %w", ErrRestoreMismatch, en.Path, err)
		}
		defer func() { _ = f.Close() }()
		if unixMode(fst) != en.Mode {
			return fmt.Errorf("%w: %q mode = %v, want %v", ErrRestoreMismatch, en.Path, unixMode(fst), en.Mode)
		}
		if int(fst.Uid) != en.UID || int(fst.Gid) != en.GID {
			return fmt.Errorf("%w: %q owner = %d:%d, want %d:%d", ErrRestoreMismatch, en.Path, fst.Uid, fst.Gid, en.UID, en.GID)
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return fmt.Errorf("%w: read %q: %w", ErrRestoreMismatch, en.Path, err)
		}
		if int64(len(data)) != fst.Size {
			return fmt.Errorf("%w: %q size changed during verify (%d vs %d)", ErrRestoreMismatch, en.Path, len(data), fst.Size)
		}
		if digest(data) != en.Digest {
			return fmt.Errorf("%w: %q content digest mismatch after restore", ErrRestoreMismatch, en.Path)
		}
		liveMeta, err := e.meta().Read(f)
		if err != nil {
			return fmt.Errorf("%w: read metadata of %q: %w", ErrRestoreMismatch, en.Path, err)
		}
		if en.XattrsStatus == MetadataCaptured {
			if liveMeta.XattrsStatus != MetadataCaptured || !equalXattrs(liveMeta.Xattrs, en.Xattrs) {
				return fmt.Errorf("%w: %q extended attributes mismatch after restore", ErrRestoreMismatch, en.Path)
			}
		}
		if en.ACLStatus == MetadataCaptured {
			if liveMeta.ACLStatus != MetadataCaptured || !equalACL(liveMeta.ACL, en.ACL) {
				return fmt.Errorf("%w: %q ACL mismatch after restore", ErrRestoreMismatch, en.Path)
			}
		}
		return nil
	case KindSymlink:
		cur, err := p.readlink()
		if err != nil || cur != en.Link {
			return fmt.Errorf("%w: link %q = %q, want %q", ErrRestoreMismatch, en.Path, cur, en.Link)
		}
		return nil
	default:
		return fmt.Errorf("%w: entry kind %q", ErrCorruptRecord, en.Kind)
	}
}

// declaredPaths returns the description's paths, cleaned, sorted and
// without repeats. Every path must be absolute and outside package-manager
// databases.
func declaredPaths(desc contract.Description) ([]string, error) {
	out := make([]string, 0, len(desc.OwnedPaths))
	for _, p := range desc.OwnedPaths {
		c, err := cleanPath(p)
		if err != nil {
			return nil, err
		}
		if isPackageDBPath(c) {
			return nil, fmt.Errorf("%w: %q (packages are changed and reverted only through the package manager)", ErrPackageDBPath, c)
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

// removeOrphanBackups removes backup folders that no run refers to: a kill
// during the backup, before the intent record, leaves one behind. A folder
// is kept when it belongs to the record's run or has a receipt. Call it
// under the host lock and the journal lock, with an idle record.
func (e *Engine) removeOrphanBackups(rec *Record) error {
	dir, err := openDir(filepath.Join(e.opts.JournalDir, backupsDir))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	names, err := listDir(dir)
	if err != nil {
		return err
	}
	removed := false
	for _, n := range names {
		id := contract.RunID(n)
		if platform.ValidateRunID(id) != nil || (rec != nil && id == rec.RunID) {
			continue
		}
		receipt, err := e.readReceipt(id)
		if err != nil {
			return err
		}
		if receipt != nil {
			continue
		}
		if err := removeFlatDir(dir, n); err != nil {
			return err
		}
		removed = true
	}
	if !removed {
		return nil
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync folder %q: %w", dir.Name(), err)
	}
	return nil
}

// removeFlatDir removes the folder name inside parent and the files in it,
// without following links. A backup folder holds only files.
func removeFlatDir(parent *os.File, name string) error {
	fd, err := unix.Openat(int(parent.Fd()), name, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("open backup folder %q: %w", name, err)
	}
	d := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	defer func() { _ = d.Close() }()
	files, err := listDir(d)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := unix.Unlinkat(fd, f, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove %q in orphan backup %q: %w", f, name, err)
		}
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove orphan backup %q: %w", name, err)
	}
	return nil
}
