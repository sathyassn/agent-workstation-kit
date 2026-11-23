package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
)

// TestParentSymlinkIsNotFollowed: a declared path whose parent folder is a
// link is refused before anything is written, so a write never lands
// outside the test root through the link.
func TestParentSymlinkIsNotFollowed(t *testing.T) {
	w := newWorld(t)
	// The real folder moves outside the root and a link takes its place,
	// so the unit still sees its original files through the link.
	outside := filepath.Join(w.dir, "outside")
	dir := filepath.Join(w.root, "etc", "wskit-test")
	if err := os.Rename(dir, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	e, _ := w.engine(t)
	err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan())
	if err == nil {
		t.Fatal("apply through a linked parent succeeded")
	}
	w.assertState(t, originalState(), "files behind the link")
	entries, _ := os.ReadDir(outside)
	if len(entries) != 3 {
		t.Fatalf("files written through the link: %v", entries)
	}
}

// TestAncestorSwapDuringApplyIsNotFollowed: an ancestor folder is replaced
// by a link while the apply runs; the next write and the restore refuse to
// go through it, so nothing lands outside the test root.
func TestAncestorSwapDuringApplyIsNotFollowed(t *testing.T) {
	w := newWorld(t)
	outside := filepath.Join(w.dir, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "wskit-test"), 0o700); err != nil {
		t.Fatal(err)
	}
	etc := filepath.Join(w.root, "etc")
	moved := filepath.Join(w.root, "etc-moved")
	e, _ := w.engine(t)
	u := &testUnit{root: w.root, afterRun: func(context.Context) error {
		if err := os.Rename(etc, moved); err != nil {
			return err
		}
		return os.Symlink(outside, etc)
	}}
	if err := e.Apply(context.Background(), u, testPlan()); err == nil {
		t.Fatal("apply after an ancestor swap succeeded")
	}
	entries, _ := os.ReadDir(filepath.Join(outside, "wskit-test"))
	if len(entries) != 0 {
		t.Fatalf("files written through the swapped ancestor: %v", entries)
	}
	if r := w.record(t); r.Status == engine.StatusIdle {
		if rc := w.receipt(t, r.RunID); rc != nil && rc.Outcome == contract.OutcomeVerified {
			t.Fatal("run verified after an ancestor swap")
		}
	}
}
