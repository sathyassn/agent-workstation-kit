package engine

import (
	"path/filepath"
	"runtime"
	"strings"
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

// isPackageDBPathForOS checks path against packageDBPrefixes using goos's
// case rules (case-insensitive on darwin APFS) and stripping macOS's
// /System/Volumes/Data firmlink prefix.
func isPackageDBPathForOS(path, goos string) bool {
	clean := filepath.Clean(path)
	fold := goos == "darwin"
	norm := func(s string) string {
		if fold {
			return strings.ToLower(s)
		}
		return s
	}
	candidates := []string{norm(clean)}
	dataPref := norm(darwinDataVolumePrefix)
	if candidates[0] == dataPref {
		candidates = append(candidates, "/")
	} else if strings.HasPrefix(candidates[0], dataPref+"/") {
		candidates = append(candidates, strings.TrimPrefix(candidates[0], dataPref))
	}
	for _, prefix := range packageDBPrefixes() {
		np := norm(prefix)
		for _, c := range candidates {
			if c == np || strings.HasPrefix(c, np+"/") {
				return true
			}
		}
	}
	return false
}
