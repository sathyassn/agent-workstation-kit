package engine

import (
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
	// maxPackageDBWalkDirs bounds the startup directory walk under existing
	// package-DB roots so an unusually large or adversarial directory tree
	// cannot hang startup.
	maxPackageDBWalkDirs = 65536
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

// resolvePackageDBIDs resolves every existing directory under walkRoots
// recursively without following symlinks inside them (bounded by
// maxPackageDBWalkDirs and ignoring unreadable directories), and also stats
// the top-level statOnlyRoots. Recording subfolder (dev, ino) identities
// catches bind mounts of subdirectories (such as /var/lib/dpkg/info).
func resolvePackageDBIDs(walkRoots, statOnlyRoots []string) map[platform.FileID]string {
	ids := make(map[platform.FileID]string)
	for _, r := range statOnlyRoots {
		var st unix.Stat_t
		if err := unix.Stat(r, &st); err != nil {
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		id := platform.FileIDFromStat(st)
		if _, exists := ids[id]; !exists {
			ids[id] = r
		}
	}
	visited := 0
	for _, r := range walkRoots {
		if visited >= maxPackageDBWalkDirs {
			break
		}
		walkPackageDBRoot(r, ids, &visited)
	}
	return ids
}

func walkPackageDBRoot(root string, ids map[platform.FileID]string, visited *int) {
	// Follow links only for the configured root path itself (for example
	// /var/db/receipts -> /private/var/db/receipts), then walk every child
	// directory strictly with O_NOFOLLOW|O_DIRECTORY.
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return
	}
	rootID := platform.FileIDFromStat(st)
	if _, seen := ids[rootID]; seen {
		_ = unix.Close(fd)
		return
	}
	ids[rootID] = root
	*visited++

	type item struct {
		fd   int
		path string
	}
	queue := []item{{fd: fd, path: root}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		names := readDirNamesUnchecked(cur.fd, cur.path)
		for _, name := range names {
			if *visited >= maxPackageDBWalkDirs {
				break
			}
			var cst unix.Stat_t
			if err := unix.Fstatat(cur.fd, name, &cst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				continue
			}
			if cst.Mode&unix.S_IFMT != unix.S_IFDIR {
				continue
			}
			cid := platform.FileIDFromStat(cst)
			if _, seen := ids[cid]; seen {
				continue
			}
			childPath := filepath.Join(cur.path, name)
			ids[cid] = childPath
			*visited++
			if *visited >= maxPackageDBWalkDirs {
				continue
			}
			cfd, err := unix.Openat(cur.fd, name, dirFlags, 0)
			if err != nil {
				// Ignore unreadable or disappeared subdirectories while still
				// keeping the directory's (dev, ino) from fstatat recorded.
				continue
			}
			queue = append(queue, item{fd: cfd, path: childPath})
		}
		_ = unix.Close(cur.fd)
	}
}

func readDirNamesUnchecked(dirFD int, path string) []string {
	dupFD, err := unix.Openat(dirFD, ".", dirFlags, 0)
	if err != nil {
		return nil
	}
	d := os.NewFile(uintptr(dupFD), path)
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil
	}
	return names
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
