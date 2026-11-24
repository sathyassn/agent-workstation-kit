package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/darwin"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/linux"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// platformNotFoundProvider adapts contracttest.FakeStateProvider so absent
// keys report platform.ErrStateKeyNotFound, as real platform providers do.
type platformNotFoundProvider struct {
	*contracttest.FakeStateProvider
}

func (p *platformNotFoundProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	st, err := p.FakeStateProvider.Read(ctx, key)
	if errors.Is(err, contracttest.ErrStateKeyNotFound) {
		return contract.State{}, platform.ErrStateKeyNotFound
	}
	return st, err
}

// engineTxInspector adapts a real *tx to contracttest.TxInspector:
//   - Backup lives only here, never on production *tx (L5);
//   - SetState registers the contract suite's per-check provider under provMu
//     only for declared keys so M2's registration guard runs unmodified;
//   - Service and UserGroup call real *tx first (so undeclared refusals run on
//     real *tx) and stub only ErrUnsupported on declared keys (M5) so the
//     remaining contract checks execute against real *tx.
type engineTxInspector struct {
	*tx
	root    string
	journal string
	cont    *platformtest.Containment
	pkgs    *platformNotFoundProvider

	stubMu      sync.Mutex
	stubChanges []contract.AppliedChange
	stubBackups map[contract.BackupRef][]byte
}

var _ contracttest.TxInspector = (*engineTxInspector)(nil)

func (i *engineTxInspector) SetState(ctx context.Context, p contract.StateProvider, key contract.StateKey, want contract.State) error {
	if p != nil && i.keys[key] {
		i.e.provMu.Lock()
		if i.e.opts.Providers == nil {
			i.e.opts.Providers = map[contract.StateProviderName]contract.StateProvider{}
		}
		i.e.opts.Providers[p.Name()] = p
		i.e.provMu.Unlock()
	}
	return i.tx.SetState(ctx, p, key, want)
}

func (i *engineTxInspector) Service(ctx context.Context, want contract.ServiceState) error {
	err := i.tx.Service(ctx, want)
	if !errors.Is(err, ErrUnsupported) {
		return err
	}
	i.recordStub(keyService+want.Name, want)
	return nil
}

func (i *engineTxInspector) UserGroup(ctx context.Context, want contract.UserGroupState) error {
	err := i.tx.UserGroup(ctx, want)
	if !errors.Is(err, ErrUnsupported) {
		return err
	}
	i.recordStub(keyUserGroup+want.User+":"+want.Group, want)
	return nil
}

func (i *engineTxInspector) recordStub(target string, payload any) {
	raw, _ := json.Marshal(payload)
	ref := contract.BackupRef(string(i.m.RunID) + ":stub:" + target)
	i.stubMu.Lock()
	if i.stubBackups == nil {
		i.stubBackups = map[contract.BackupRef][]byte{}
	}
	i.stubBackups[ref] = raw
	i.stubChanges = append(i.stubChanges, contract.AppliedChange{
		Change: contract.Change{
			Target: target,
			Action: contract.ActionSet,
			After:  digest(raw),
			Risk:   contract.RiskMedium,
		},
		Backup: ref,
	})
	i.stubMu.Unlock()
}

func (i *engineTxInspector) Changes() []contract.AppliedChange {
	out := i.tx.Changes()
	i.stubMu.Lock()
	out = append(out, i.stubChanges...)
	i.stubMu.Unlock()
	return out
}

func (i *engineTxInspector) Backup(ref contract.BackupRef) ([]byte, bool) {
	i.stubMu.Lock()
	if data, ok := i.stubBackups[ref]; ok {
		i.stubMu.Unlock()
		return bytes.Clone(data), true
	}
	i.stubMu.Unlock()

	prefix := string(i.m.RunID) + ":"
	suffix, ok := strings.CutPrefix(string(ref), prefix)
	if !ok || suffix == "" {
		return nil, false
	}
	fileName := suffix
	if en, found := i.m.entry(suffix); found {
		if en.Data == "" {
			return nil, false
		}
		fileName = en.Data
	}
	data, err := readFileAt(filepath.Join(i.e.backupDir(i.m.RunID), fileName))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (i *engineTxInspector) BackingState() []byte {
	files := map[string][]byte{}
	_ = filepath.WalkDir(i.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(i.root, p)
		if relErr != nil {
			return nil
		}
		data, readErr := os.ReadFile(p) //nolint:gosec // test temp root
		if readErr == nil {
			files[rel] = data
		}
		return nil
	})
	var pkgSnap []byte
	if snap, err := i.pkgs.Snapshot(context.Background()); err == nil {
		pkgSnap = snap.Data
	}
	payload := struct {
		Files    map[string][]byte        `json:"files"`
		Changes  []contract.AppliedChange `json:"changes"`
		Packages []byte                   `json:"packages"`
		Launched int                      `json:"launched"`
	}{
		Files:    files,
		Changes:  i.Changes(),
		Packages: pkgSnap,
		Launched: len(i.cont.Launched()),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return raw
}

func newContractTestEngineTx(t *testing.T, desc contract.Description) *engineTxInspector {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	journal := filepath.Join(dir, "journal")
	hostLock := filepath.Join(dir, "host.lock")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range desc.OwnedPaths {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	table := platformtest.NewProcessTable("boot-contract")
	cont := platformtest.NewContainment(table)
	pkgs := &platformNotFoundProvider{
		FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderPackages, map[contract.StateKey]contract.State{}),
	}
	providers := map[contract.StateProviderName]contract.StateProvider{
		contract.ProviderPackages: pkgs,
	}
	e, err := New(Options{
		JournalDir:  journal,
		HostLock:    hostLock,
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: cont,
		Inspector:   table.Inspector(table.Spawn()),
		Providers:   providers,
		LookupEnv:   func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.prepareJournal(); err != nil {
		t.Fatal(err)
	}
	runID := contract.RunID("rcontract0001")
	m, err := e.backup(context.Background(), runID, desc)
	if err != nil {
		t.Fatal(err)
	}
	rec := &Record{
		Status: StatusApplying,
		RunID:  runID,
		Unit:   desc.Unit,
		Scope:  contract.ScopeUser,
	}
	l := &locks{e: e}
	return &engineTxInspector{
		tx:      newTx(e, l, rec, desc, m),
		root:    root,
		journal: journal,
		cont:    cont,
		pkgs:    pkgs,
	}
}

func TestEngineTxPassesContractTests(t *testing.T) {
	t.Run("DeclarativeTx", func(t *testing.T) {
		contracttest.TestDeclarativeTx(t, func(d contract.Description) contract.DeclarativeTx {
			return newContractTestEngineTx(t, d)
		})
	})

	t.Run("Tx", func(t *testing.T) {
		contracttest.TestTx(t, func(d contract.Description) contract.Tx {
			return newContractTestEngineTx(t, d)
		})
	})
}

type failingWriteAndRestoreProvider struct {
	*contracttest.FakeStateProvider
	writeErr   error
	restoreErr error
}

func (p *failingWriteAndRestoreProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	if err := p.FakeStateProvider.Write(ctx, key, want); err != nil {
		return err
	}
	return p.writeErr
}

func (p *failingWriteAndRestoreProvider) Restore(_ context.Context, _ contract.Snapshot) error {
	return p.restoreErr
}

func TestEngineTxSetStateReportsRestoreFailure(t *testing.T) {
	key := contract.StateKey("hostname.static")
	desc := contract.Description{
		Unit:           contract.UnitMachineIdentity,
		Scope:          contract.ScopeRoot,
		OwnedStateKeys: []contract.StateKey{key},
	}
	insp := newContractTestEngineTx(t, desc)
	wantWriteErr := errors.New("injected write error")
	wantRestoreErr := errors.New("injected restore error")
	p := &failingWriteAndRestoreProvider{
		FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
			key: {Value: "lab-ws-01"},
		}),
		writeErr:   wantWriteErr,
		restoreErr: wantRestoreErr,
	}

	err := insp.SetState(context.Background(), p, key, contract.State{Value: "lab-ws-02"})
	for _, want := range []error{wantWriteErr, ErrStateRestoreFailed, wantRestoreErr} {
		if !errors.Is(err, want) {
			t.Errorf("SetState err = %v, want %v", err, want)
		}
	}
	if n := len(insp.Changes()); n != 0 {
		t.Fatalf("recorded %d receipt rows for a failed Write, want 0", n)
	}
}

type unsupportedMetadataOps struct{}

func (unsupportedMetadataOps) Read(_ *os.File) (FileMetadata, error) {
	return FileMetadata{
		XattrsStatus: MetadataUnsupported,
		ACLStatus:    MetadataUnsupported,
	}, nil
}

func (unsupportedMetadataOps) Apply(_ *os.File, _ FileMetadata) error { return nil }

func TestUnsupportedFilesystemRecordsMetadataAbsent(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-meta.conf"
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-meta")
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: platformtest.NewContainment(table),
		Inspector:   table.Inspector(table.Spawn()),
		metaOps:     unsupportedMetadataOps{},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := failingWriteUnit{path: target, fail: true}
	err = e.Apply(context.Background(), u, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
	if !errors.Is(err, ErrApplyFailed) {
		t.Fatalf("Apply err = %v, want ErrApplyFailed", err)
	}
	rec, err := e.readRecord()
	if err != nil || rec == nil {
		t.Fatalf("readRecord = %v, %v", rec, err)
	}
	m, err := e.readManifest(rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("manifest entries = %d, want 1", len(m.Entries))
	}
	en := m.Entries[0]
	if en.XattrsStatus != MetadataUnsupported || len(en.Xattrs) != 0 {
		t.Fatalf("xattrs = (%q, %v), want unsupported and absent", en.XattrsStatus, en.Xattrs)
	}
	if en.ACLStatus != MetadataUnsupported || en.ACL != nil {
		t.Fatalf("acl = (%q, %v), want unsupported and absent", en.ACLStatus, en.ACL)
	}
	got, err := os.ReadFile(filepath.Join(root, target)) //nolint:gosec // test temp root
	if err != nil || string(got) != "orig\n" {
		t.Fatalf("restored content = %q, %v; want orig", got, err)
	}
}

// dropMetaOnApplyOps captures real metadata on backup, drops it on Apply so
// restore verification observes a metadata mismatch and fails closed (exit 4).
type dropMetaOnApplyOps struct {
	captured FileMetadata
	reads    int
}

func (d *dropMetaOnApplyOps) Read(_ *os.File) (FileMetadata, error) {
	d.reads++
	if d.reads == 1 {
		return d.captured, nil
	}
	return FileMetadata{
		XattrsStatus: MetadataCaptured,
		ACLStatus:    MetadataCaptured,
	}, nil
}

func (d *dropMetaOnApplyOps) Apply(_ *os.File, _ FileMetadata) error { return nil }

func TestRestoreFailsClosedOnMetadataMismatch(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-mismatch.conf"
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-mismatch")
	ops := &dropMetaOnApplyOps{
		captured: FileMetadata{
			XattrsStatus: MetadataCaptured,
			Xattrs:       []XattrEntry{{Name: []byte("user.expected"), Value: []byte("v1")}},
			ACLStatus:    MetadataCaptured,
		},
	}
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: platformtest.NewContainment(table),
		Inspector:   table.Inspector(table.Spawn()),
		metaOps:     ops,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: true}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
	if !errors.Is(err, ErrRestoreFailed) || !errors.Is(err, ErrRestoreMismatch) || ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("err = %v (exit %d), want ErrRestoreFailed + ErrRestoreMismatch and exit 4", err, ExitCode(err))
	}
	rec, err := e.readRecord()
	if err != nil || rec == nil || rec.Status != StatusRestoring {
		t.Fatalf("record = %+v, err=%v; want StatusRestoring (unfinished)", rec, err)
	}
}

type failingWriteUnit struct {
	path string
	fail bool
}

func (u failingWriteUnit) ID() contract.UnitID { return contract.UnitShell }
func (u failingWriteUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{Unit: contract.UnitShell, Scope: contract.ScopeUser, OwnedPaths: []string{u.path}}, nil
}
func (u failingWriteUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"}, nil
}
func (u failingWriteUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	if err := tx.WriteFile(ctx, u.path, []byte("changed\n"), 0o644, contract.Owner{UID: os.Getuid(), GID: os.Getgid()}); err != nil {
		return err
	}
	if u.fail {
		return errors.New("forced failure after write")
	}
	return nil
}
func (u failingWriteUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitShell, Outcome: contract.OutcomeVerified}, nil
}
func (u failingWriteUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u failingWriteUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitShell}, nil
}

func TestEqualXattrsAndACLHelpers(t *testing.T) {
	a := []XattrEntry{{Name: []byte("b"), Value: []byte("2")}, {Name: []byte("a"), Value: []byte("1")}}
	b := []XattrEntry{{Name: []byte("a"), Value: []byte("1")}, {Name: []byte("b"), Value: []byte("2")}}
	if !equalXattrs(a, b) {
		t.Fatal("equalXattrs failed on differently ordered equal slices")
	}
	if equalXattrs(a, []XattrEntry{{Name: []byte("a"), Value: []byte("diff")}}) {
		t.Fatal("equalXattrs matched unequal slices")
	}
	if !equalACL(nil, nil) {
		t.Fatal("equalACL(nil, nil) = false")
	}
	aclA := &FileACL{Kind: ACLKindLinuxPOSIX, Access: []byte{1, 2, 3}}
	aclB := &FileACL{Kind: ACLKindLinuxPOSIX, Access: []byte{1, 2, 3}}
	if !equalACL(aclA, aclB) {
		t.Fatal("equalACL failed on equal ACLs")
	}
	if equalACL(aclA, &FileACL{Kind: ACLKindDarwinFilesec, Access: []byte{1, 2, 3}}) {
		t.Fatal("equalACL matched different kinds")
	}
	// Ensure bytes, slices and time imports are exercised cleanly.
	_ = bytes.Equal(nil, nil)
	_ = slices.Equal([]int{1}, []int{1})
	_ = time.Second
}

// TestPackageDBGuardRefusesSameDevInoAlias is H2: even when a declared path's
// spelling does not match any package-DB prefix (as with a bind mount or path
// alias), walking through a directory whose (dev, ino) matches a resolved
// package-DB root must be refused with ErrPackageDBPath (exit 2).
func TestPackageDBGuardRefusesSameDevInoAlias(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	aliasDir := filepath.Join(root, "mnt", "pkg-alias")
	if err := os.MkdirAll(aliasDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := "/mnt/pkg-alias/status"
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig-db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-pkgdb-ino")
	e, err := New(Options{
		JournalDir:          filepath.Join(dir, "journal"),
		HostLock:            filepath.Join(dir, "host.lock"),
		Root:                root,
		Scope:               contract.ScopeUser,
		Containment:         platformtest.NewContainment(table),
		Inspector:           table.Inspector(table.Spawn()),
		extraPackageDBPaths: []string{aliasDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
	if !errors.Is(err, ErrPackageDBPath) || ExitCode(err) != contract.ExitRefused {
		t.Fatalf("Apply on bind-mount/alias package-DB path = %v (exit %d), want ErrPackageDBPath (exit 2)", err, ExitCode(err))
	}
	rec, err := e.readRecord()
	if err != nil || rec != nil {
		t.Fatalf("intent written for package-DB alias: %+v, %v", rec, err)
	}
}

// TestBackupRefusesLstatFstatMismatchAndUsesOpenedFD is H3: backupOne must
// fstat the opened fd, refuse if dev/ino/type changed after lstat, and take
// every attribute (owner, mode, size, xattrs, ACLs, content) from the opened fd.
func TestBackupRefusesLstatFstatMismatchAndUsesOpenedFD(t *testing.T) {
	t.Run("inode swapped between lstat and open is refused", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-toctou.conf"
		realTarget := filepath.Join(root, target)
		if err := os.MkdirAll(filepath.Dir(realTarget), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realTarget, []byte("before-swap\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-toctou")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			afterLstat: func(_ string) {
				tmp := realTarget + ".swap"
				_ = os.WriteFile(tmp, []byte("swapped-inode\n"), 0o600)
				_ = os.Rename(tmp, realTarget)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
		if !errors.Is(err, ErrTargetChanged) || ExitCode(err) != contract.ExitRefused {
			t.Fatalf("Apply err = %v (exit %d), want ErrTargetChanged (exit 2)", err, ExitCode(err))
		}
		rec, rerr := e.readRecord()
		if rerr != nil || rec != nil {
			t.Fatalf("intent written after lstat/fstat mismatch: %+v, %v", rec, rerr)
		}
	})

	t.Run("file replaced by directory between lstat and open is refused", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-typeswap.conf"
		realTarget := filepath.Join(root, target)
		if err := os.MkdirAll(filepath.Dir(realTarget), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realTarget, []byte("before-dir\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-typeswap")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			afterLstat: func(_ string) {
				_ = os.Remove(realTarget)
				_ = os.Mkdir(realTarget, 0o700)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
		if !errors.Is(err, ErrTargetChanged) || ExitCode(err) != contract.ExitRefused {
			t.Fatalf("Apply err = %v (exit %d), want ErrTargetChanged (exit 2)", err, ExitCode(err))
		}
	})

	t.Run("mode comes from fstat on opened fd rather than earlier lstat", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-fstat-mode.conf"
		realTarget := filepath.Join(root, target)
		if err := os.MkdirAll(filepath.Dir(realTarget), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realTarget, []byte("same-inode\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-fstat")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			afterLstat: func(_ string) {
				// Chmod the exact same inode between lstat and open.
				_ = os.Chmod(realTarget, 0o640) //nolint:gosec // test temp file
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		rec, err := e.readRecord()
		if err != nil || rec == nil {
			t.Fatalf("readRecord = %v, %v", rec, err)
		}
		m, err := e.readManifest(rec.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Entries) != 1 || m.Entries[0].Mode != 0o640 {
			t.Fatalf("manifest Mode = %v, want 0640 from fstat on opened fd", m.Entries[0].Mode)
		}
	})
}

type memoryMetadataOps struct {
	mu      sync.Mutex
	current FileMetadata
	applied []XattrEntry
}

func (m *memoryMetadataOps) Read(_ *os.File) (FileMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current, nil
}

func (m *memoryMetadataOps) Apply(_ *os.File, meta FileMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applied = append([]XattrEntry(nil), meta.Xattrs...)
	m.current = meta
	return nil
}

// TestNonUTF8XattrNameRoundTripsThroughBackupAndRestore is M4 end-to-end:
// a non-UTF-8 extended attribute name (user.\xff\xfe) survives backup,
// manifest.json persistence on disk, restore Apply and post-restore verify.
func TestNonUTF8XattrNameRoundTripsThroughBackupAndRestore(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-nonutf8.conf"
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawName := []byte("user.\xff\xfe")
	rawVal := []byte("raw\x00\xff")
	ops := &memoryMetadataOps{
		current: FileMetadata{
			XattrsStatus: MetadataCaptured,
			Xattrs:       []XattrEntry{{Name: rawName, Value: rawVal}},
			ACLStatus:    MetadataCaptured,
		},
	}
	table := platformtest.NewProcessTable("boot-nonutf8")
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: platformtest.NewContainment(table),
		Inspector:   table.Inspector(table.Spawn()),
		metaOps:     ops,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: true}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
	if !errors.Is(err, ErrApplyFailed) {
		t.Fatalf("Apply err = %v, want ErrApplyFailed after lossless restore", err)
	}
	if len(ops.applied) != 1 || !bytes.Equal(ops.applied[0].Name, rawName) || !bytes.Equal(ops.applied[0].Value, rawVal) {
		t.Fatalf("applied xattrs = %+v, want exact raw name %x and value %x", ops.applied, rawName, rawVal)
	}
}

// TestPackageDBGuardRefusesSubfolderAndHardLinkTarget is R2:
//  1. Subdirectories under a package-DB root have their (dev, ino) recorded
//     at startup so a bind mount of a subfolder (e.g. /var/lib/dpkg/info) is
//     refused with ErrPackageDBPath.
//  2. Regular-file targets with link count (Nlink) > 1 are refused before any
//     backup or write (exit 2).
func TestPackageDBGuardRefusesSubfolderAndHardLinkTarget(t *testing.T) {
	t.Run("subfolder under package-DB root is recorded and refused", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		pkgRoot := filepath.Join(dir, "dpkg-root")
		pkgSub := filepath.Join(pkgRoot, "info", "nested")
		if err := os.MkdirAll(pkgSub, 0o750); err != nil {
			t.Fatal(err)
		}
		outsideDir := filepath.Join(dir, "outside")
		if err := os.MkdirAll(outsideDir, 0o750); err != nil {
			t.Fatal(err)
		}
		// Symlink inside pkgRoot must NOT be followed into outsideDir.
		if err := os.Symlink(outsideDir, filepath.Join(pkgRoot, "escape-link")); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-pkgdb-sub")
		e, err := New(Options{
			JournalDir:          filepath.Join(dir, "journal"),
			HostLock:            filepath.Join(dir, "host.lock"),
			Root:                root,
			Scope:               contract.ScopeUser,
			Containment:         platformtest.NewContainment(table),
			Inspector:           table.Inspector(table.Spawn()),
			extraPackageDBPaths: []string{pkgRoot},
		})
		if err != nil {
			t.Fatal(err)
		}
		var subSt, outSt unix.Stat_t
		if err := unix.Stat(pkgSub, &subSt); err != nil {
			t.Fatal(err)
		}
		if err := unix.Stat(outsideDir, &outSt); err != nil {
			t.Fatal(err)
		}
		if err := e.checkPackageDBStat(subSt, "/mnt/bind-info"); !errors.Is(err, ErrPackageDBPath) {
			t.Fatalf("checkPackageDBStat(subfolder) = %v, want ErrPackageDBPath", err)
		}
		if err := e.checkPackageDBStat(outSt, "/mnt/outside"); err != nil {
			t.Fatalf("symlinked outside dir was unexpectedly added to pkgDBIDs: %v", err)
		}
	})

	t.Run("hard-linked regular file target (Nlink > 1) is refused exit 2", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-hardlink.conf"
		realTarget := filepath.Join(root, target)
		if err := os.MkdirAll(filepath.Dir(realTarget), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realTarget, []byte("orig-hl\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(realTarget, filepath.Join(dir, "other-link")); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-hardlink")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
		if !errors.Is(err, ErrHardLinkTarget) || ExitCode(err) != contract.ExitRefused {
			t.Fatalf("Apply on hard-linked file = %v (exit %d), want ErrHardLinkTarget (exit 2)", err, ExitCode(err))
		}
		rec, rerr := e.readRecord()
		if rerr != nil || rec != nil {
			t.Fatalf("intent written for hard-linked target: %+v, %v", rec, rerr)
		}
		if got, rerr := os.ReadFile(realTarget); rerr != nil || string(got) != "orig-hl\n" { //nolint:gosec // test temp root
			t.Fatalf("hard-linked file mutated: %q, %v", got, rerr)
		}
	})
}

// TestBackupRefusesSwappedFIFOWithoutBlocking is R3: a FIFO swapped in between
// lstat and open must not block open() waiting for a writer; it must return
// immediately with ErrTargetChanged (exit 2).
func TestBackupRefusesSwappedFIFOWithoutBlocking(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-fifo.conf"
	realTarget := filepath.Join(root, target)
	if err := os.MkdirAll(filepath.Dir(realTarget), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realTarget, []byte("before-fifo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-fifo")
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: platformtest.NewContainment(table),
		Inspector:   table.Inspector(table.Spawn()),
		afterLstat: func(_ string) {
			_ = os.Remove(realTarget)
			_ = unix.Mkfifo(realTarget, 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrTargetChanged) || ExitCode(err) != contract.ExitRefused {
			t.Fatalf("Apply err = %v (exit %d), want ErrTargetChanged (exit 2)", err, ExitCode(err))
		}
	case <-time.After(time.Second):
		// Unblock any hung O_RDONLY FIFO open so the goroutine exits cleanly.
		if wfd, oerr := unix.Open(realTarget, unix.O_WRONLY|unix.O_NONBLOCK, 0); oerr == nil {
			_ = unix.Close(wfd)
		}
		t.Fatal("Apply hung opening swapped FIFO (missing O_NONBLOCK)")
	}
}

// TestContainedStateRunnerSeparatesStdoutAndStderrInternal is R7: running a
// hostname provider command through containedCommandRunner with concurrent
// stdout + stderr writes under -race parses ONLY stdout without stderr
// contamination.
func TestContainedStateRunnerSeparatesStdoutAndStderrInternal(t *testing.T) {
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-stdout-stderr")
	cont := platformtest.NewContainment(table)
	cont.SetRun(func(_ context.Context, spec platform.LaunchSpec) (contract.CommandResult, error) {
		var wg sync.WaitGroup
		wg.Go(func() {
			if spec.Output != nil {
				_, _ = spec.Output.Write([]byte("lab-ws-clean\n"))
			}
		})
		wg.Go(func() {
			errW := spec.Stderr
			if errW == nil {
				errW = spec.Output
			}
			if errW != nil {
				_, _ = errW.Write([]byte("warning: noisy stderr message\n"))
			}
		})
		wg.Wait()
		return contract.CommandResult{ExitCode: 0}, nil
	})
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: cont,
		Inspector:   table.Inspector(table.Spawn()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.prepareJournal(); err != nil {
		t.Fatal(err)
	}
	rec := &Record{Status: StatusApplying, RunID: "rstdoutsep", Unit: contract.UnitMachineIdentity, Scope: contract.ScopeUser}
	l := &locks{e: e}
	runner := e.containedStateRunner(l, rec, false, nil, nil, nil)
	res, err := runner.Run(ctx, platform.ExecSpec{
		Argv: []string{"/usr/sbin/scutil", "--get", "HostName"},
		Env:  []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"},
	})
	if err != nil {
		t.Fatalf("contained runner Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "lab-ws-clean" {
		t.Fatalf("Stdout = %q, want %q (must not contain stderr)", res.Stdout, "lab-ws-clean")
	}
	if !strings.Contains(res.Stderr, "noisy stderr message") {
		t.Fatalf("Stderr = %q, want diagnostic warning preserved", res.Stderr)
	}
	// Drive the real OS HostnameProvider through containedCommandRunner to verify
	// parsed hostname equals stdout only with zero stderr contamination.
	var hostProv contract.StateProvider
	if runtime.GOOS == "darwin" {
		hostProv = &darwin.HostnameProvider{}
	} else {
		hostProv = &linux.HostnameProvider{}
	}
	gotHost, err := hostProv.Read(platform.WithCommandRunner(ctx, runner), platform.HostnameKeyStatic)
	if err != nil || gotHost.Value != "lab-ws-clean" {
		t.Fatalf("HostnameProvider.Read via contained runner = %+v, %v; want lab-ws-clean", gotHost, err)
	}
}

// TestPackageDBScanPreservesLaterRootsWhenFirstHitsCap is T1:
//  1. Every existing package-DB root identity is recorded BEFORE descending
//     into any subdirectories, so later roots stay recorded even when an
//     earlier root hits maxPackageDBWalkDirs.
//  2. Hitting the cap fails closed with ErrPackageDBScan (exit 2) rather than
//     silently skipping the rest of the tree.
func TestPackageDBScanPreservesLaterRootsWhenFirstHitsCap(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root1 := filepath.Join(dir, "root1")
	for _, sub := range []string{"a", "b", "c", "d", "e"} {
		if err := os.MkdirAll(filepath.Join(root1, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	root2 := filepath.Join(dir, "root2")
	if err := os.MkdirAll(root2, 0o750); err != nil {
		t.Fatal(err)
	}

	// Direct resolvePackageDBIDs check: root1 hits maxDirs=2, yet root2 is
	// already recorded AND ErrPackageDBScan is returned (never silent skip).
	ids, scanErr := resolvePackageDBIDs([]string{root1, root2}, nil, 2, nil, false)
	if !errors.Is(scanErr, ErrPackageDBScan) {
		t.Fatalf("resolvePackageDBIDs err = %v, want ErrPackageDBScan", scanErr)
	}
	var st1, st2 unix.Stat_t
	if err := unix.Stat(root1, &st1); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(root2, &st2); err != nil {
		t.Fatal(err)
	}
	if _, ok := ids[platform.FileIDFromStat(st1)]; !ok {
		t.Fatalf("first root %q missing from ids", root1)
	}
	if _, ok := ids[platform.FileIDFromStat(st2)]; !ok {
		t.Fatalf("later root %q was silently skipped after root1 hit cap (ids=%d)", root2, len(ids))
	}

	// Engine.New fails closed with ErrPackageDBScan (exit 2) when scan hits cap.
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-pkgdb-cap")
	_, err = New(Options{
		JournalDir:          filepath.Join(dir, "journal"),
		HostLock:            filepath.Join(dir, "host.lock"),
		Root:                root,
		Scope:               contract.ScopeUser,
		Containment:         platformtest.NewContainment(table),
		Inspector:           table.Inspector(table.Spawn()),
		extraPackageDBPaths: []string{root1, root2},
		maxPackageDBDirs:    2,
	})
	if !errors.Is(err, ErrPackageDBScan) || ExitCode(err) != contract.ExitRefused {
		t.Fatalf("New with capped package-DB scan = %v (exit %d), want ErrPackageDBScan (exit 2)", err, ExitCode(err))
	}
}

// TestPackageDBScanPermissionDeniedFailsClosed is F1 + G1 (root mode): when
// running as root (via the isRoot seam), a permission-denied (EACCES/EPERM)
// subtree inside an existing package-DB root must fail closed with
// ErrPackageDBScan (exit 2) rather than silently skipping the subtree.
func TestPackageDBScanPermissionDeniedFailsClosed(t *testing.T) {
	t.Run("root_seam_eacces_and_eperm", func(t *testing.T) {
		for _, permErr := range []unix.Errno{unix.EACCES, unix.EPERM} {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			pkgRoot := filepath.Join(dir, "pkgdb")
			locked := filepath.Join(pkgRoot, "locked")
			if err := os.MkdirAll(locked, 0o750); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "root")
			if err := os.MkdirAll(root, 0o750); err != nil {
				t.Fatal(err)
			}
			injectPerm := func(path string) (*os.File, error) {
				if path == locked {
					return nil, &os.PathError{Op: "open", Path: path, Err: permErr}
				}
				return platform.OpenDirNoFollow(path)
			}
			table := platformtest.NewProcessTable("boot-pkgdb-root-perm")
			_, err = New(Options{
				JournalDir:          filepath.Join(dir, "journal"),
				HostLock:            filepath.Join(dir, "host.lock"),
				Root:                root,
				Scope:               contract.ScopeUser,
				Containment:         platformtest.NewContainment(table),
				Inspector:           table.Inspector(table.Spawn()),
				extraPackageDBPaths: []string{pkgRoot},
				openPackageDBDir:    injectPerm,
				isRoot:              func() bool { return true },
			})
			if !errors.Is(err, ErrPackageDBScan) || ExitCode(err) != contract.ExitRefused {
				t.Fatalf("root New with %v package-DB subtree = %v (exit %d), want ErrPackageDBScan (exit 2)", permErr, err, ExitCode(err))
			}
		}
	})

	t.Run("root_seam_0000_dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as real root bypasses 0o000 directory read permissions")
		}
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		pkgRoot := filepath.Join(dir, "pkgdb")
		locked := filepath.Join(pkgRoot, "locked")
		if err := os.MkdirAll(locked, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // restore directory permissions for t.TempDir cleanup

		root := filepath.Join(dir, "root")
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-pkgdb-root-0000")
		_, err = New(Options{
			JournalDir:          filepath.Join(dir, "journal"),
			HostLock:            filepath.Join(dir, "host.lock"),
			Root:                root,
			Scope:               contract.ScopeUser,
			Containment:         platformtest.NewContainment(table),
			Inspector:           table.Inspector(table.Spawn()),
			extraPackageDBPaths: []string{pkgRoot},
			isRoot:              func() bool { return true },
		})
		if !errors.Is(err, ErrPackageDBScan) || ExitCode(err) != contract.ExitRefused {
			t.Fatalf("root New with 0o000 package-DB subtree = %v (exit %d), want ErrPackageDBScan (exit 2)", err, ExitCode(err))
		}
	})
}

// TestPackageDBScanEMFILEFailsClosed is F1 (part 2): descriptor exhaustion
// (EMFILE) while scanning a package-DB root must fail closed with
// ErrPackageDBScan (exit 2) rather than silently skipping the subtree.
func TestPackageDBScanEMFILEFailsClosed(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkgRoot := filepath.Join(dir, "pkgdb")
	sub := filepath.Join(pkgRoot, "sub")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	injectEMFILE := func(path string) (*os.File, error) {
		if path == sub {
			return nil, &os.PathError{Op: "open", Path: path, Err: unix.EMFILE}
		}
		return platform.OpenDirNoFollow(path)
	}
	table := platformtest.NewProcessTable("boot-pkgdb-emfile")
	_, err = New(Options{
		JournalDir:          filepath.Join(dir, "journal"),
		HostLock:            filepath.Join(dir, "host.lock"),
		Root:                root,
		Scope:               contract.ScopeUser,
		Containment:         platformtest.NewContainment(table),
		Inspector:           table.Inspector(table.Spawn()),
		extraPackageDBPaths: []string{pkgRoot},
		openPackageDBDir:    injectEMFILE,
	})
	if !errors.Is(err, ErrPackageDBScan) || ExitCode(err) != contract.ExitRefused {
		t.Fatalf("New with EMFILE during package-DB scan = %v (exit %d), want ErrPackageDBScan (exit 2)", err, ExitCode(err))
	}
}

// TestPackageDBScanUnprivilegedRecordsUnreadableSubdir is G1 (unprivileged):
// when not running as root, a 0o000 (unreadable) subdirectory inside a
// package-DB root (such as Ubuntu's 0700 _apt partial directories) must not
// fail startup; its (dev, ino) is recorded so any target beneath it is refused
// with ErrPackageDBPath (exit 2), and sibling subtrees continue scanning.
func TestPackageDBScanUnprivilegedRecordsUnreadableSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses 0o000 directory read permissions")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	pkgRoot := filepath.Join(root, "pkgdb")
	locked := filepath.Join(pkgRoot, "partial")
	sibling := filepath.Join(pkgRoot, "lists", "nested")
	if err := os.MkdirAll(locked, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // restore directory permissions for t.TempDir cleanup

	table := platformtest.NewProcessTable("boot-pkgdb-unpriv")
	e, err := New(Options{
		JournalDir:          filepath.Join(dir, "journal"),
		HostLock:            filepath.Join(dir, "host.lock"),
		Root:                root,
		Scope:               contract.ScopeUser,
		Containment:         platformtest.NewContainment(table),
		Inspector:           table.Inspector(table.Spawn()),
		extraPackageDBPaths: []string{pkgRoot},
		isRoot:              func() bool { return false },
	})
	if err != nil {
		t.Fatalf("unprivileged New with 0o000 package-DB subdir failed: %v", err)
	}

	var lockedSt, siblingSt unix.Stat_t
	if err := unix.Lstat(locked, &lockedSt); err != nil {
		t.Fatal(err)
	}
	if err := unix.Lstat(sibling, &siblingSt); err != nil {
		t.Fatal(err)
	}
	if err := e.checkPackageDBStat(lockedSt, "/mnt/bind-partial"); !errors.Is(err, ErrPackageDBPath) || ExitCode(err) != contract.ExitRefused {
		t.Fatalf("checkPackageDBStat(unreadable subdir) = %v (exit %d), want ErrPackageDBPath (exit 2)", err, ExitCode(err))
	}
	if err := e.checkPackageDBStat(siblingSt, "/mnt/bind-sibling"); !errors.Is(err, ErrPackageDBPath) || ExitCode(err) != contract.ExitRefused {
		t.Fatalf("checkPackageDBStat(sibling subdir) = %v (exit %d), want ErrPackageDBPath (exit 2)", err, ExitCode(err))
	}

	// Any target path beneath the unreadable subdir must be refused with ErrPackageDBPath (exit 2).
	target := "/pkgdb/partial/archive.deb"
	err = e.Apply(context.Background(), failingWriteUnit{path: target, fail: false}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"})
	if !errors.Is(err, ErrPackageDBPath) || ExitCode(err) != contract.ExitRefused {
		t.Fatalf("Apply under unreadable package-DB subdir = %v (exit %d), want ErrPackageDBPath (exit 2)", err, ExitCode(err))
	}
}
