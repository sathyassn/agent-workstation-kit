package engine

import (
	"path/filepath"
	"strings"
)

// packageDBPrefixes lists the package-manager database and store roots on
// supported platforms (Ubuntu apt/dpkg, macOS Homebrew and installer
// receipts). Package changes are made and reverted only through the package
// manager, never by backing up or restoring these files (ADR 0005 section 5,
// TSK-018 AC-2).
func packageDBPrefixes() []string {
	return []string{
		"/var/lib/dpkg",
		"/var/lib/apt",
		"/var/cache/apt",
		"/opt/homebrew/Cellar",
		"/opt/homebrew/Caskroom",
		"/opt/homebrew/var/homebrew",
		"/usr/local/Cellar",
		"/usr/local/Caskroom",
		"/usr/local/var/homebrew",
		"/var/db/receipts",
		"/private/var/db/receipts",
		"/Library/Receipts",
	}
}

// isPackageDBPath reports whether path lies inside a package-manager database
// or package store directory.
func isPackageDBPath(path string) bool {
	clean := filepath.Clean(path)
	for _, prefix := range packageDBPrefixes() {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}
