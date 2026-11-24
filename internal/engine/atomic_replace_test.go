package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReplaceFilePreservingMetadataSyncsParent verifies that
// ReplaceFilePreservingMetadata renames into place and fsyncs the parent
// directory after replacing an existing file.
func TestReplaceFilePreservingMetadataSyncsParent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dst := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(dst, []byte("old = true\n"), 0o640); err != nil { //nolint:gosec // testing 0640 mode preservation
		t.Fatalf("write initial file: %v", err)
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
	got, err := os.ReadFile(dst) //nolint:gosec // temp file
	if err != nil || string(got) != "new = true\n" {
		t.Fatalf("content after replace = (%q, %v), want %q", string(got), err, "new = true\n")
	}
}
