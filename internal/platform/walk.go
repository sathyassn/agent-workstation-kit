package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrPathLink is wrapped when a walk meets a link or a non-folder where a
// folder should be. It is a sentinel for errors.Is and is never reassigned.
var ErrPathLink = errors.New("path holds a link or a non-folder")

const walkDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// OpenDirNoFollow opens the folder at the clean absolute path abs by walking
// from "/" one component at a time with O_NOFOLLOW|O_DIRECTORY. A link
// anywhere on the path, or a folder swapped for a link, fails the walk
// instead of redirecting it. Callers then act on names relative to the
// returned folder.
func OpenDirNoFollow(abs string) (*os.File, error) {
	if !filepath.IsAbs(abs) || filepath.Clean(abs) != abs {
		return nil, fmt.Errorf("%w: %q is not a clean absolute path", ErrPathLink, abs)
	}
	fd, err := unix.Open("/", walkDirFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("open /: %w", err)
	}
	for _, comp := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if comp == "" {
			continue
		}
		next, err := unix.Openat(fd, comp, walkDirFlags, 0)
		_ = unix.Close(fd)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return nil, fmt.Errorf("%w: %q at %q: %w", ErrPathLink, abs, comp, err)
			}
			return nil, fmt.Errorf("open %q at %q: %w", abs, comp, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), abs), nil
}
