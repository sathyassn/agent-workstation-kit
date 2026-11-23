// Package version reports the build version of the kit's binaries.
package version

import "runtime/debug"

// Dev is reported when the build carries no module version.
const Dev = "dev"

// String returns the main module version from the running binary's build
// info, or Dev when there is none.
func String() string {
	info, ok := debug.ReadBuildInfo()
	return From(info, ok)
}

// From returns the main module version in info, or Dev when info is missing
// or carries no real version.
func From(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return Dev
	}
	switch v := info.Main.Version; v {
	case "", "(devel)":
		return Dev
	default:
		return v
	}
}
