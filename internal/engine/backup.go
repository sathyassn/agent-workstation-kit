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
	switch {
	case strings.HasPrefix(s, keyPackage):
		if _, ok := e.opts.Providers[contract.ProviderPackages]; ok {
			return contract.ProviderPackages
		}
		return ""
	case strings.HasPrefix(s, keyUserGroup):
		if _, ok := e.opts.Providers[contract.ProviderAccounts]; ok {
			return contract.ProviderAccounts
		}
		return ""
	case strings.HasPrefix(s, keyService):
		return e.serviceProviderName()
	}
	for name := range e.opts.Providers {
		ns := string(name)
		if s == ns || strings.HasPrefix(s, ns+".") || strings.HasPrefix(s, ns+":") {
			return name
		}
	}
	return ""
}

func (e *Engine) serviceProviderName() contract.StateProviderName {
	if _, ok := e.opts.Providers[contract.ProviderUnitMasks]; ok {
		return contract.ProviderUnitMasks
	}
	if _, ok := e.opts.Providers[contract.ProviderLaunchd]; ok {
		return contract.ProviderLaunchd
	}
	return ""
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
		en.Kind, en.Digest, en.Data = KindFile, digest(data), name
		en.XattrsStatus, en.Xattrs = meta.XattrsStatus, meta.Xattrs
		en.ACLStatus, en.ACL = meta.ACLStatus, meta.ACL
		if err := e.atomicWrite(filepath.Join(dir, name), fileSpec{data: data, perm: 0o600}); err != nil {
			return ManifestEntry{}, err
		}
	case unix.S_IFLNK:
		return ManifestEntry{}, fmt.Errorf("%w: %q %s", ErrUnsupportedTarget, path, errLinkTarget)
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

// restore replays every state snapshot and file entry in m in reverse order,
// verifies content and metadata against the manifest, and runs every
// configured RestoreValidator. It is idempotent: a restore a kill interrupted
// is finished by running it again. Any mismatch or validator failure fails
// closed (exit 4, intent left unfinished).
func (e *Engine) restore(ctx context.Context, m *Manifest, extra map[contract.StateProviderName]contract.StateProvider) error {
	dir := e.backupDir(m.RunID)
	for _, st := range slices.Backward(m.States) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restore state %s: %w", st.Provider, err)
		}
		if err := e.restoreState(ctx, dir, st, extra); err != nil {
			return err
		}
		e.fault(PointRestoreEntry)
	}
	for _, en := range slices.Backward(m.Entries) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		if isPackageDBPath(en.Path) {
			return fmt.Errorf("%w: %w: %q", ErrRestoreMismatch, ErrPackageDBPath, en.Path)
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

func (e *Engine) lookupProvider(name contract.StateProviderName, extra map[contract.StateProviderName]contract.StateProvider) contract.StateProvider {
	if p, ok := e.opts.Providers[name]; ok {
		return p
	}
	if extra != nil {
		if p, ok := extra[name]; ok {
			return p
		}
	}
	return nil
}

func (e *Engine) restoreState(ctx context.Context, dir string, st ManifestState, extra map[contract.StateProviderName]contract.StateProvider) error {
	data, err := readFileAt(filepath.Join(dir, st.Data))
	if err != nil {
		return fmt.Errorf("%w: read state backup %q: %w", ErrRestoreMismatch, st.Provider, err)
	}
	if digest(data) != st.Digest {
		return fmt.Errorf("%w: state %q", ErrBackupMismatch, st.Provider)
	}
	p := e.lookupProvider(st.Provider, extra)
	if p == nil {
		return fmt.Errorf("%w: no state provider registered for %q", ErrRestoreMismatch, st.Provider)
	}
	if err := p.Restore(ctx, contract.Snapshot{Provider: st.Provider, Data: data}); err != nil {
		return fmt.Errorf("%w: restore state %q: %w", ErrRestoreMismatch, st.Provider, err)
	}
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
	p, err := openParent(e.real(en.Path))
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
		if unixMode(st) != en.Mode {
			return fmt.Errorf("%w: %q mode = %v, want %v", ErrRestoreMismatch, en.Path, unixMode(st), en.Mode)
		}
		if int(st.Uid) != en.UID || int(st.Gid) != en.GID {
			return fmt.Errorf("%w: %q owner = %d:%d, want %d:%d", ErrRestoreMismatch, en.Path, st.Uid, st.Gid, en.UID, en.GID)
		}
		f, err := p.open()
		if err != nil {
			return fmt.Errorf("%w: open %q: %w", ErrRestoreMismatch, en.Path, err)
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			return fmt.Errorf("%w: read %q: %w", ErrRestoreMismatch, en.Path, err)
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
