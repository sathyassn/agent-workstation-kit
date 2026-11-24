package engine_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func xattrNameEmpty() string {
	if runtime.GOOS == "darwin" {
		return "com.example.wskit-empty"
	}
	return "user.wskit-empty"
}

// failOrSkipInCI fails the test when CI is set so an unsupported runner
// filesystem never passes AC-3 silently; outside CI it skips.
func failOrSkipInCI(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf(format, args...)
		return
	}
	t.Skipf(format, args...)
}

func setXattrValue(t *testing.T, path, name string, val []byte) {
	t.Helper()
	if err := unix.Setxattr(path, name, val, 0); err != nil {
		failOrSkipInCI(t, "extended attributes not supported here: %v", err)
	}
}

func getXattrValue(t *testing.T, path, name string) ([]byte, bool) {
	t.Helper()
	n, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return nil, false
	}
	if n == 0 {
		return []byte{}, true
	}
	buf := make([]byte, n)
	n, err = unix.Getxattr(path, name, buf)
	if err != nil {
		t.Fatalf("getxattr %q on %q: %v", name, path, err)
	}
	return buf[:n], true
}

// buildLinuxPOSIXACL constructs a valid minimal+extended POSIX ACL binary
// representation (version 2) granting read to the current UID alongside
// standard owner/group/mask/other entries.
func buildLinuxPOSIXACL(uid uint32) []byte {
	// struct posix_acl_xattr_header { u32 a_version = 2; }
	// struct posix_acl_xattr_entry  { u16 e_tag; u16 e_perm; u32 e_id; }
	const (
		aclVersion2 = 2
		tagUserObj  = 0x01
		tagUser     = 0x02
		tagGroupObj = 0x04
		tagMask     = 0x10
		tagOther    = 0x20
		undefinedID = ^uint32(0)
		permRW      = 6
		permR       = 4
		permNone    = 0
	)
	type entry struct {
		tag  uint16
		perm uint16
		id   uint32
	}
	entries := []entry{
		{tagUserObj, permRW, undefinedID},
		{tagUser, permR, uid},
		{tagGroupObj, permNone, undefinedID},
		{tagMask, permR, undefinedID},
		{tagOther, permNone, undefinedID},
	}
	buf := make([]byte, 4+len(entries)*8)
	binary.LittleEndian.PutUint32(buf[0:4], aclVersion2)
	for i, e := range entries {
		off := 4 + i*8
		binary.LittleEndian.PutUint16(buf[off:off+2], e.tag)
		binary.LittleEndian.PutUint16(buf[off+2:off+4], e.perm)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], e.id)
	}
	return buf
}

// setACL attaches a real OS ACL to path on both macOS and Linux.
func setACL(t *testing.T, path string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		// #nosec G204 -- fixed system tool on a test temp file.
		if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read", path).CombinedOutput(); err != nil {
			failOrSkipInCI(t, "cannot set a macOS ACL here: %v %s", err, out)
		}
	case "linux":
		acl := buildLinuxPOSIXACL(uint32(os.Getuid())) //nolint:gosec // current test UID is non-negative
		if err := unix.Setxattr(path, "system.posix_acl_access", acl, 0); err != nil {
			failOrSkipInCI(t, "POSIX ACLs not supported here: %v", err)
		}
	default:
		failOrSkipInCI(t, "unsupported OS for ACL fixture")
	}
}

// readACLText returns a comparable string representation of path's ACL.
func readACLText(t *testing.T, path string) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		// #nosec G204 -- fixed system tool on a test temp file.
		out, err := exec.Command("/bin/ls", "-le", path).CombinedOutput()
		if err != nil {
			t.Fatalf("ls -le %q: %v %s", path, err, out)
		}
		// Keep only the ACL lines (lines after the first stat line).
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if len(lines) <= 1 {
			return ""
		}
		return strings.Join(lines[1:], "\n")
	case "linux":
		val, ok := getXattrValue(t, path, "system.posix_acl_access")
		if !ok {
			return ""
		}
		return string(val)
	default:
		return ""
	}
}

// TestExtendedMetadataLiftsRefusalAndRestoresIdentically is AC-3:
// targets carrying extended attributes or ACLs are no longer refused; when
// modified and reverted after a failure, both content and full metadata
// (mode, owner, xattrs and ACLs) return identically.
func TestExtendedMetadataLiftsRefusalAndRestoresIdentically(t *testing.T) {
	for _, tc := range []struct {
		name     string
		applyACL bool
		applyXA  bool
	}{
		{"xattrs only", false, true},
		{"acl only", true, false},
		{"xattrs and acl", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			target := filepath.Join(w.root, pathA)
			wantXattr := []byte("wskit-meta-value\x00binary")
			if tc.applyXA {
				setXattrValue(t, target, xattrName(), wantXattr)
				setXattrValue(t, target, xattrNameEmpty(), []byte{})
			}
			var wantACL string
			if tc.applyACL {
				setACL(t, target)
				wantACL = readACLText(t, target)
				if wantACL == "" {
					t.Fatal("ACL fixture did not attach an ACL")
				}
			}

			// 1. Failing apply replaces pathA (dropping xattrs/ACL) then fails;
			// restore must bring back exact content, mode, xattrs and ACL.
			e, _ := w.engine(t)
			u := &testUnit{root: w.root, afterRun: func(context.Context) error {
				// Confirm WriteFile actually replaced the file before failure.
				if data, err := os.ReadFile(target); err != nil || string(data) != "new-a\n" { //nolint:gosec // test temp root
					t.Fatalf("target during apply = %q, %v; want new-a", data, err)
				}
				return errNotRestored
			}}
			err := e.Apply(context.Background(), u, testPlan())
			if !errors.Is(err, engine.ErrApplyFailed) || !errors.Is(err, errNotRestored) {
				t.Fatalf("Apply err = %v, want ErrApplyFailed wrapping errNotRestored", err)
			}
			w.assertState(t, originalState(), "after failed apply restore")

			if tc.applyXA {
				got, ok := getXattrValue(t, target, xattrName())
				if !ok || !bytes.Equal(got, wantXattr) {
					t.Fatalf("xattr %q after restore = %q (ok=%v), want %q", xattrName(), got, ok, wantXattr)
				}
				gotEmpty, ok := getXattrValue(t, target, xattrNameEmpty())
				if !ok || len(gotEmpty) != 0 {
					t.Fatalf("empty xattr after restore = %q (ok=%v), want empty present", gotEmpty, ok)
				}
			}
			if tc.applyACL {
				gotACL := readACLText(t, target)
				if gotACL != wantACL {
					t.Fatalf("ACL after restore =\n%q\nwant:\n%q", gotACL, wantACL)
				}
			}

			// 2. A succeeding apply on the same file with xattrs/ACL is accepted
			// (the TSK-013 refusal is lifted).
			e2, _ := w.engine(t)
			if err := e2.Apply(context.Background(), &testUnit{root: w.root}, testPlan()); err != nil {
				t.Fatalf("Apply on file with extended metadata failed: %v", err)
			}
			w.assertState(t, appliedState(), "after succeeding apply")
		})
	}
}

// TestEnsureBlockPreservesExtendedMetadata verifies that managed-block edits
// keep an existing file's extended attributes and ACLs intact.
func TestEnsureBlockPreservesExtendedMetadata(t *testing.T) {
	w := newWorld(t)
	target := filepath.Join(w.root, pathB)
	wantVal := []byte("keep-me")
	setXattrValue(t, target, xattrName(), wantVal)
	setACL(t, target)
	wantACL := readACLText(t, target)

	e, _ := w.engine(t)
	if err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, ok := getXattrValue(t, target, xattrName())
	if !ok || !bytes.Equal(got, wantVal) {
		t.Fatalf("xattr after EnsureBlock = %q (ok=%v), want %q", got, ok, wantVal)
	}
	if gotACL := readACLText(t, target); gotACL != wantACL {
		t.Fatalf("ACL after EnsureBlock =\n%q\nwant:\n%q", gotACL, wantACL)
	}
}

// TestDarwinProvenanceNotRecordedInManifest verifies the macOS
// com.apple.provenance exemption stays intact after lifting the refusal.
func TestDarwinProvenanceNotRecordedInManifest(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("com.apple.provenance is macOS-specific")
	}
	w := newWorld(t)
	e, _ := w.engine(t)
	if err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rec := w.record(t)
	data, err := os.ReadFile(filepath.Join(w.journal, "backups", string(rec.RunID), "manifest.json")) //nolint:gosec // test temp journal
	if err != nil {
		t.Fatal(err)
	}
	var m engine.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, en := range m.Entries {
		for _, xa := range en.Xattrs {
			if xa.Name == "com.apple.provenance" {
				t.Fatalf("manifest recorded exempted com.apple.provenance on %q", en.Path)
			}
		}
	}
}

// setInheritableDirACL attaches an inheritable ACL to dir so newly created
// files inside dir automatically inherit an ACE.
func setInheritableDirACL(t *testing.T, dir string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		// #nosec G204 -- fixed system tool on a test temp folder.
		if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read,file_inherit", dir).CombinedOutput(); err != nil {
			failOrSkipInCI(t, "cannot set inheritable macOS ACL here: %v %s", err, out)
		}
	case "linux":
		acl := buildLinuxPOSIXACL(uint32(os.Getuid())) //nolint:gosec // current test UID is non-negative
		if err := unix.Setxattr(dir, "system.posix_acl_default", acl, 0); err != nil {
			failOrSkipInCI(t, "POSIX default ACLs not supported here: %v", err)
		}
	default:
		failOrSkipInCI(t, "unsupported OS for inheritable ACL fixture")
	}
}

// TestRestoreClearsInheritedDirACL is M1: a file that originally had no ACL
// lives inside a directory that later gains an inheritable ACE. When an apply
// fails and restore creates a fresh temp file in that directory, restore must
// clear the inherited ACL so the restored file matches the manifest identically.
func TestRestoreClearsInheritedDirACL(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.root, filepath.Dir(pathA))
	target := filepath.Join(w.root, pathA)

	// Confirm target initially has no ACL, then add an inheritable ACE to its folder.
	if got := readACLText(t, target); got != "" {
		t.Fatalf("target unexpectedly started with ACL: %q", got)
	}
	setInheritableDirACL(t, dir)

	e, _ := w.engine(t)
	u := &testUnit{root: w.root, afterRun: func(context.Context) error { return errNotRestored }}
	err := e.Apply(context.Background(), u, testPlan())
	if !errors.Is(err, engine.ErrApplyFailed) || !errors.Is(err, errNotRestored) {
		t.Fatalf("Apply err = %v (exit %d), want ErrApplyFailed wrapping errNotRestored", err, engine.ExitCode(err))
	}
	w.assertState(t, originalState(), "after restore in dir with inherited ACL")
	if got := readACLText(t, target); got != "" {
		t.Fatalf("restored target kept inherited ACL:\n%s", got)
	}
}

type mockTB struct {
	testing.TB
	failed  bool
	skipped bool
}

func (m *mockTB) Helper()               {}
func (m *mockTB) Fatalf(string, ...any) { m.failed = true }
func (m *mockTB) Skipf(string, ...any)  { m.skipped = true }

// TestFailOrSkipInCI is H1: when CI is set, missing xattr or ACL support
// fails the test rather than silently skipping it.
func TestFailOrSkipInCI(t *testing.T) {
	t.Run("CI set fails instead of skipping", func(t *testing.T) {
		t.Setenv("CI", "true")
		m := &mockTB{TB: t}
		failOrSkipInCI(m, "unsupported")
		if !m.failed || m.skipped {
			t.Fatalf("failed=%v skipped=%v, want failed=true skipped=false", m.failed, m.skipped)
		}
	})
	t.Run("CI unset skips", func(t *testing.T) {
		t.Setenv("CI", "")
		m := &mockTB{TB: t}
		failOrSkipInCI(m, "unsupported")
		if m.failed || !m.skipped {
			t.Fatalf("failed=%v skipped=%v, want failed=false skipped=true", m.failed, m.skipped)
		}
	})
}
