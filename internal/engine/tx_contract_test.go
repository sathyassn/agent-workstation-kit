package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
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
			Xattrs:       []XattrEntry{{Name: "user.expected", Value: []byte("v1")}},
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
	a := []XattrEntry{{Name: "b", Value: []byte("2")}, {Name: "a", Value: []byte("1")}}
	b := []XattrEntry{{Name: "a", Value: []byte("1")}, {Name: "b", Value: []byte("2")}}
	if !equalXattrs(a, b) {
		t.Fatal("equalXattrs failed on differently ordered equal slices")
	}
	if equalXattrs(a, []XattrEntry{{Name: "a", Value: []byte("diff")}}) {
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
