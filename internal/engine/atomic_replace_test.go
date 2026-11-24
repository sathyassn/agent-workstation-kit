package engine

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestReplaceFilePreservingMetadataSyncsParent verifies that
// ReplaceFilePreservingMetadata renames into place, preserves owner and mode,
// and fsyncs the parent directory after replacing an existing file.
func TestReplaceFilePreservingMetadataSyncsParent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dst := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(dst, []byte("old = true\n"), 0o640); err != nil { //nolint:gosec // testing 0640 mode preservation
		t.Fatalf("write initial file: %v", err)
	}
	if err := os.Chmod(dst, 0o640); err != nil { //nolint:gosec // testing 0640 mode preservation
		t.Fatalf("chmod initial file: %v", err)
	}

	var beforeSys unix.Stat_t
	if err := unix.Stat(dst, &beforeSys); err != nil {
		t.Fatalf("stat initial file: %v", err)
	}
	wantUID, wantGID := beforeSys.Uid, beforeSys.Gid
	if groups, gErr := os.Getgroups(); gErr == nil {
		for _, g := range groups {
			if g >= 0 && uint32(g) != wantGID { //nolint:gosec // checked non-negative GID
				if chErr := os.Chown(dst, -1, g); chErr == nil {
					wantGID = uint32(g) //nolint:gosec // checked non-negative GID
					break
				}
			}
		}
	}

	var events []string
	e := &Engine{
		opts: Options{
			traceFn: func(op, _, _ string) {
				events = append(events, op)
			},
		},
	}
	if err := e.replaceFilePreservingMetadata(dst, []byte("new = true\n"), 0o600); err != nil {
		t.Fatalf("replaceFilePreservingMetadata: %v", err)
	}
	if len(events) < 2 || events[len(events)-2] != traceEntry || events[len(events)-1] != traceSync {
		t.Fatalf("trace events = %v, want trailing [%s %s]", events, traceEntry, traceSync)
	}
	afterInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if afterInfo.Mode().Perm() != 0o640 {
		t.Errorf("mode after replace = %04o, want 0640", afterInfo.Mode().Perm())
	}
	var afterSys unix.Stat_t
	if err := unix.Stat(dst, &afterSys); err != nil {
		t.Fatalf("unix.Stat replaced file: %v", err)
	}
	if afterSys.Uid != wantUID || afterSys.Gid != wantGID {
		t.Errorf("owner after replace = %d:%d, want %d:%d", afterSys.Uid, afterSys.Gid, wantUID, wantGID)
	}
	got, err := os.ReadFile(dst) //nolint:gosec // temp file
	if err != nil || string(got) != "new = true\n" {
		t.Fatalf("content after replace = (%q, %v), want %q", string(got), err, "new = true\n")
	}
}
