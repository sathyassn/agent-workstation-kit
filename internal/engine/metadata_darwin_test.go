//go:build darwin

package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// sipSimulatingMetaOps runs darwin's real xattr classification over an injected
// macOS kernel attribute (com.apple.macl or com.apple.rootless) plus a normal
// user xattr, and delegates Apply to real darwin applyFileMetadata (which calls
// real fsetxattr on APFS, returning EPERM if a protected attribute is not
// excluded or refused).
type sipSimulatingMetaOps struct {
	injectedName string
}

func (s sipSimulatingMetaOps) Read(f *os.File) (FileMetadata, error) {
	m, err := readFileMetadata(f)
	if err != nil {
		return FileMetadata{}, err
	}
	name := []byte(s.injectedName)
	skip, err := classifyDarwinXattr(f.Name(), name)
	if err != nil {
		return FileMetadata{}, err
	}
	if skip {
		if isDarwinDroppedXattr(name) {
			m.DroppedXattrs = append(m.DroppedXattrs, s.injectedName)
		}
		return m, nil
	}
	m.XattrsStatus = MetadataCaptured
	m.Xattrs = append(m.Xattrs, XattrEntry{Name: name, Value: []byte{1, 2, 3, 4}})
	return m, nil
}

func (s sipSimulatingMetaOps) Apply(f *os.File, m FileMetadata) error {
	return applyFileMetadata(f, m)
}

// TestDarwinSIPProtectedXattrsNeverWedgeRecovery is M5:
//   - com.apple.macl is OS-attached by macOS TCC on ordinary user files, so it
//     is excluded consistently across backup, apply and verify and restore
//     completes cleanly (never wedging at exit 4).
//   - com.apple.rootless marks a SIP-protected target, so backup refuses it
//     with ErrProtectedMetadata (exit 2) before writing intent or mutating
//     anything (never entering or wedging recovery).
func TestDarwinSIPProtectedXattrsNeverWedgeRecovery(t *testing.T) {
	t.Run("com.apple.macl excluded consistently so restore completes cleanly", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-sip-macl.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-sip-macl")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			metaOps:     sipSimulatingMetaOps{injectedName: "com.apple.macl"},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: true}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
		if !errors.Is(err, ErrApplyFailed) || ExitCode(err) == contract.ExitRecoveryBlocks {
			t.Fatalf("Apply err = %v (exit %d), want clean ErrApplyFailed (never exit 4)", err, ExitCode(err))
		}
		rec, rerr := e.readRecord()
		if rerr != nil || rec == nil || rec.Status != StatusIdle {
			t.Fatalf("record = %+v, err=%v; want StatusIdle", rec, rerr)
		}
		m, merr := e.readManifest(rec.RunID)
		if merr != nil {
			t.Fatal(merr)
		}
		for _, xa := range m.Entries[0].Xattrs {
			if bytes.Equal(xa.Name, []byte("com.apple.macl")) {
				t.Fatalf("manifest recorded excluded com.apple.macl: %+v", m.Entries[0].Xattrs)
			}
		}
	})

	t.Run("com.apple.rootless refused at backup exit 2 before intent", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-sip-rootless.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-sip-rootless")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			metaOps:     sipSimulatingMetaOps{injectedName: "com.apple.rootless"},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: true}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
		if !errors.Is(err, ErrProtectedMetadata) || ExitCode(err) != contract.ExitRefused {
			t.Fatalf("Apply err = %v (exit %d), want ErrProtectedMetadata (exit 2)", err, ExitCode(err))
		}
		rec, rerr := e.readRecord()
		if rerr != nil || rec != nil {
			t.Fatalf("intent written for SIP-protected target: %+v, %v", rec, rerr)
		}
	})

	t.Run("applyFileMetadata skips com.apple.macl and refuses com.apple.rootless", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "sip-apply-*")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if err := applyFileMetadata(f, FileMetadata{
			XattrsStatus: MetadataCaptured,
			Xattrs:       []XattrEntry{{Name: []byte("com.apple.macl"), Value: []byte{1, 2}}},
		}); err != nil {
			t.Fatalf("applyFileMetadata with com.apple.macl failed: %v", err)
		}
		if err := applyFileMetadata(f, FileMetadata{
			XattrsStatus: MetadataCaptured,
			Xattrs:       []XattrEntry{{Name: []byte("com.apple.rootless"), Value: []byte{1}}},
		}); !errors.Is(err, ErrProtectedMetadata) {
			t.Fatalf("applyFileMetadata with com.apple.rootless = %v, want ErrProtectedMetadata", err)
		}
	})
}

// TestDarwinMACLDropSurfacedInEventsAndReceiptWithoutBlocking is R6: replacing
// a target carrying com.apple.macl must succeed without blocking or failing
// verify, surface the drop in events, and record it in the receipt.
func TestDarwinMACLDropSurfacedInEventsAndReceiptWithoutBlocking(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-macl-drop.conf"
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := contracttest.NewFakeSink(64)
	table := platformtest.NewProcessTable("boot-macl-drop")
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: platformtest.NewContainment(table),
		Inspector:   table.Inspector(table.Spawn()),
		Sink:        sink,
		metaOps:     sipSimulatingMetaOps{injectedName: "com.apple.macl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"}); err != nil {
		t.Fatalf("Apply on file with com.apple.macl failed: %v", err)
	}
	var sawBackupWarning, sawApplyWarning bool
	for _, ev := range sink.Events() {
		if ev.Kind == contract.EventWarning && strings.Contains(ev.Text, "com.apple.macl") && ev.Data[contract.DataPath] == target {
			switch ev.Step {
			case contract.StepBackup:
				sawBackupWarning = true
			case contract.StepApply:
				sawApplyWarning = true
			}
		}
	}
	if !sawBackupWarning || !sawApplyWarning {
		t.Fatalf("events did not surface com.apple.macl drop in backup (%v) and apply (%v): %+v", sawBackupWarning, sawApplyWarning, sink.Events())
	}
	rec, err := e.readRecord()
	if err != nil || rec == nil {
		t.Fatalf("readRecord = %v, %v", rec, err)
	}
	m, err := e.readManifest(rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 || len(m.Entries[0].DroppedXattrs) != 1 || m.Entries[0].DroppedXattrs[0] != "com.apple.macl" {
		t.Fatalf("manifest DroppedXattrs = %+v, want [com.apple.macl]", m.Entries[0].DroppedXattrs)
	}
	rc, err := e.readReceipt(rec.RunID)
	if err != nil || rc == nil {
		t.Fatalf("readReceipt = %v, %v", rc, err)
	}
	var sawReceiptDrop bool
	for _, ch := range rc.Changes {
		if ch.Target == target+"#xattr:com.apple.macl" && ch.Action == contract.ActionRemove {
			sawReceiptDrop = true
		}
	}
	if !sawReceiptDrop {
		t.Fatalf("receipt changes did not record dropped com.apple.macl: %+v", rc.Changes)
	}
}
