package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	darwinDataVolumePrefix = "/System/Volumes/Data"
	// maxPackageDBWalkDirs bounds the startup subdirectory walk under existing
	// package-DB roots so an adversarial directory tree cannot hang startup.
	// Exceeding it fails closed (ErrPackageDBScan, exit 2) after all root
	// identities are already recorded.
	maxPackageDBWalkDirs = 262144
)

// packageDBPrefixes lists the package-manager database and store roots on
// supported platforms (Ubuntu apt/dpkg/debconf/ucf, macOS Homebrew and
// Apple installer receipts). Package changes are made and reverted only
// through the package manager, never by backing up or restoring these files
// (ADR 0005 section 5, TSK-018 AC-2).
func packageDBPrefixes() []string {
	return []string{
		"/var/lib/dpkg",
		"/var/lib/apt",
		"/var/cache/apt",
		"/var/cache/debconf",
		"/var/lib/ucf",
		"/var/log/apt",
		"/opt/homebrew/Cellar",
		"/opt/homebrew/Caskroom",
		"/opt/homebrew/var/homebrew",
		"/usr/local/Cellar",
		"/usr/local/Caskroom",
		"/usr/local/var/homebrew",
		"/var/db/receipts",
		"/private/var/db/receipts",
		"/Library/Receipts",
		"/Library/Apple/System/Library/Receipts",
	}
}

// isPackageDBPath reports whether path lies inside a package-manager database
// or package store directory on the running OS.
func isPackageDBPath(path string) bool {
	return isPackageDBPathForOS(path, runtime.GOOS)
}

func pathSegments(clean string) []string {
	trimmed := strings.TrimPrefix(clean, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func segmentEqual(a, b string, fold bool) bool {
	if fold {
		// strings.EqualFold implements full Unicode simple case folding,
		// catching characters such as U+017F (long s 'ſ') and U+212A ('K')
		// that strings.ToLower leaves unchanged.
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasSegmentPrefix(pathParts, prefParts []string, fold bool) bool {
	if len(pathParts) < len(prefParts) {
		return false
	}
	for i := range prefParts {
		if !segmentEqual(pathParts[i], prefParts[i], fold) {
			return false
		}
	}
	return true
}

// isPackageDBPathForOS checks path against packageDBPrefixes using goos's
// case rules (Unicode case folding on darwin APFS) and stripping macOS's
// /System/Volumes/Data firmlink prefix.
func isPackageDBPathForOS(path, goos string) bool {
	clean := filepath.Clean(path)
	fold := goos == "darwin"
	parts := pathSegments(clean)
	candidates := [][]string{parts}
	dataParts := pathSegments(darwinDataVolumePrefix)
	if hasSegmentPrefix(parts, dataParts, fold) {
		candidates = append(candidates, parts[len(dataParts):])
	}
	for _, prefix := range packageDBPrefixes() {
		prefParts := pathSegments(prefix)
		for _, c := range candidates {
			if hasSegmentPrefix(c, prefParts, fold) {
				return true
			}
		}
	}
	return false
}

type dirOpener func(string) (*os.File, error)

// resolvePackageDBIDs resolves every existing package-DB root first (both
// statOnlyRoots and walkRoots) so every root's (dev, ino) is guaranteed
// recorded before descending into any subdirectories. It then walks each
// existing walkRoot depth-first without following symlinks, closing each
// directory handle immediately after reading its entries so at most one
// directory descriptor stays open at a time. Only ENOENT (a non-existent root
// or vanished subdirectory) is non-fatal; any permission, read, link, or fd
// error (such as EACCES, EPERM, EMFILE, ENFILE) or exceeding maxDirs fails
// closed with ErrPackageDBScan while still returning roots already recorded.
func resolvePackageDBIDs(walkRoots, statOnlyRoots []string, maxDirs int, openDir dirOpener) (map[platform.FileID]string, error) {
	if maxDirs <= 0 {
		maxDirs = maxPackageDBWalkDirs
	}
	if openDir == nil {
		openDir = platform.OpenDirNoFollow
	}
	ids := make(map[platform.FileID]string)

	// Phase 1: record every existing root identity before descending anywhere.
	for _, r := range statOnlyRoots {
		var st unix.Stat_t
		if err := unix.Stat(r, &st); err != nil {
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
				continue
			}
			return ids, fmt.Errorf("%w: stat root %q: %w", ErrPackageDBScan, r, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		id := platform.FileIDFromStat(st)
		if _, exists := ids[id]; !exists {
			ids[id] = r
		}
	}

	var existingWalkRoots []string
	seenWalkRoot := make(map[platform.FileID]bool)
	for _, r := range walkRoots {
		var st unix.Stat_t
		if err := unix.Stat(r, &st); err != nil {
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
				continue
			}
			return ids, fmt.Errorf("%w: stat root %q: %w", ErrPackageDBScan, r, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		id := platform.FileIDFromStat(st)
		if _, exists := ids[id]; !exists {
			ids[id] = r
		}
		if seenWalkRoot[id] {
			continue
		}
		seenWalkRoot[id] = true
		realRoot, err := filepath.EvalSymlinks(r)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return ids, fmt.Errorf("%w: resolve root %q: %w", ErrPackageDBScan, r, err)
		}
		existingWalkRoots = append(existingWalkRoots, realRoot)
	}

	// Phase 2: depth-first walk holding at most one directory fd open at once.
	visited := 0
	for _, root := range existingWalkRoots {
		if err := walkPackageDBRootDFS(root, ids, &visited, maxDirs, openDir); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

func walkPackageDBRootDFS(root string, ids map[platform.FileID]string, visited *int, maxDirs int, openDir dirOpener) error {
	stack := []string{root}
	for len(stack) > 0 {
		curPath := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		children, err := scanPackageDBDir(curPath, ids, visited, maxDirs, openDir)
		if err != nil {
			return err
		}
		stack = append(stack, children...)
	}
	return nil
}

// scanPackageDBDir opens curPath without following links, reads and stats its
// entries, closes the directory descriptor immediately before returning, and
// returns newly discovered child directory paths for the DFS stack. Only ENOENT
// is non-fatal; any permission, read, or fd error fails closed with
// ErrPackageDBScan.
func scanPackageDBDir(curPath string, ids map[platform.FileID]string, visited *int, maxDirs int, openDir dirOpener) ([]string, error) {
	d, err := openDir(curPath)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: open %q: %w", ErrPackageDBScan, curPath, err)
	}
	defer func() { _ = d.Close() }()

	names, err := d.Readdirnames(-1)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read %q: %w", ErrPackageDBScan, curPath, err)
	}

	dirFD := int(d.Fd())
	var children []string
	for _, name := range names {
		var cst unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &cst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return nil, fmt.Errorf("%w: stat %q: %w", ErrPackageDBScan, filepath.Join(curPath, name), err)
		}
		if cst.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		cid := platform.FileIDFromStat(cst)
		if _, seen := ids[cid]; seen {
			continue
		}
		childPath := filepath.Join(curPath, name)
		if *visited >= maxDirs {
			return nil, fmt.Errorf("%w: directory count exceeded limit %d at %q", ErrPackageDBScan, maxDirs, childPath)
		}
		ids[cid] = childPath
		*visited++
		children = append(children, childPath)
	}
	return children, nil
}

func (e *Engine) checkPackageDBStat(st unix.Stat_t, path string) error {
	if len(e.pkgDBIDs) == 0 {
		return nil
	}
	if root, ok := e.pkgDBIDs[platform.FileIDFromStat(st)]; ok {
		return fmt.Errorf("%w: %q resolves to package-database directory %q", ErrPackageDBPath, path, root)
	}
	return nil
}

func (e *Engine) checkPackageDBFD(fd int, path string) error {
	if len(e.pkgDBIDs) == 0 {
		return nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("stat walked folder %q: %w", path, err)
	}
	return e.checkPackageDBStat(st, path)
}
