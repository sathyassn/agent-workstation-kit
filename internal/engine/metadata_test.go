package engine_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/engine"
)

// xattrName is a user attribute name accepted on both systems.
func xattrName() string {
	if runtime.GOOS == "darwin" {
		return "com.example.wskit-test"
	}
	return "user.wskit-test"
}

func setXattr(t *testing.T, path string) {
	t.Helper()
	if err := unix.Setxattr(path, xattrName(), []byte("x"), 0); err != nil {
		t.Skipf("extended attributes not supported here: %v", err)
	}
}

func setACL(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("ACL fixture uses macOS chmod +a; Linux ACLs are extended attributes")
	}
	// #nosec G204 -- fixed system tool on a test temp file.
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read", path).CombinedOutput(); err != nil {
		t.Skipf("cannot set an ACL here: %v %s", err, out)
	}
}

// TestRefusesTargetsWithExtendedMetadata is finding 9: a declared file
// that carries an ACL or extended attributes is refused before anything
// changes, because the backup cannot keep them yet; they are never dropped
// silently.
func TestRefusesTargetsWithExtendedMetadata(t *testing.T) {
	for name, set := range map[string]func(*testing.T, string){"xattr": setXattr, "acl": setACL} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			set(t, filepath.Join(w.root, pathA))
			e, _ := w.engine(t)
			err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan())
			if !errors.Is(err, engine.ErrUnsupportedMetadata) {
				t.Fatalf("err = %v, want ErrUnsupportedMetadata", err)
			}
			w.assertState(t, originalState(), "after refusal")
			if r := w.record(t); r != nil {
				t.Fatalf("intent written for a refused target: %+v", r)
			}
		})
	}
}

// TestRefusesTargetGainingMetadataMidApply: a target that gains an
// extended attribute after the backup is refused before it is replaced,
// and the run is restored from the backup, which had none.
func TestRefusesTargetGainingMetadataMidApply(t *testing.T) {
	w := newWorld(t)
	e, _ := w.engine(t)
	u := &testUnit{root: w.root, afterRun: func(context.Context) error {
		setXattr(t, filepath.Join(w.root, pathB))
		return nil
	}}
	err := e.Apply(context.Background(), u, testPlan())
	if !errors.Is(err, engine.ErrUnsupportedMetadata) || !errors.Is(err, engine.ErrApplyFailed) {
		t.Fatalf("err = %v, want ErrUnsupportedMetadata and a restore", err)
	}
	w.assertState(t, originalState(), "after restore")
}
