package engine

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const darwinDataVolumePrefix = "/System/Volumes/Data"

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

// resolvePackageDBIDs resolves every existing directory in roots once and
// records its (dev, ino) identity, so path walks can refuse crossing into a
// package-manager database even via bind mounts or path aliases.
func resolvePackageDBIDs(roots []string) map[platform.FileID]string {
	ids := make(map[platform.FileID]string)
	for _, r := range roots {
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
	return ids
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
