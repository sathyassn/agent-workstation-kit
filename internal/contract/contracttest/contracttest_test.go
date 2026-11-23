package contracttest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

type fakeReporter struct {
	errors []string
}

func (f *fakeReporter) Helper() {}

func (f *fakeReporter) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func (f *fakeReporter) hasErrorContaining(sub string) bool {
	for _, msg := range f.errors {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

func TestGoodFakesPassContractTests(t *testing.T) {
	t.Parallel()

	t.Run("FileUnit", func(t *testing.T) {
		t.Parallel()
		u := NewFileUnit(contract.UnitShell, "/etc/agent-workstation-kit/shell.conf", []byte("export PATH=/usr/bin\n"))
		TestUnit(t, u)
	})

	t.Run("FakeDeclarativeTx", func(t *testing.T) {
		t.Parallel()
		TestDeclarativeTx(t, func(d contract.Description) contract.DeclarativeTx {
			return NewFakeDeclarativeTx(d, NewFakeSink(64))
		})
	})

	t.Run("FakeTx", func(t *testing.T) {
		t.Parallel()
		TestTx(t, func(d contract.Description) contract.Tx {
			return NewFakeTx(d, NewFakeSink(64))
		})
	})

	t.Run("FakeSink", func(t *testing.T) {
		t.Parallel()
		sink := NewFakeSink(16)
		TestSink(t, sink)
		if sink.Dropped() == 0 {
			t.Fatal("bounded FakeSink should have dropped excess events without blocking")
		}
		if len(sink.Events()) != 16 {
			t.Fatalf("FakeSink kept %d events, want 16", len(sink.Events()))
		}
	})

	t.Run("FakeStateProvider", func(t *testing.T) {
		t.Parallel()
		p := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
			declaredState: {Value: "lab-ws-01"},
		})
		TestStateProvider(t, p, declaredState, func(ctx context.Context) error {
			return p.Write(ctx, declaredState, contract.State{Value: "acme-ws-001"})
		})
	})
}

func TestFakeDeclarativeTxOperations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	desc := standardTestDescription()
	sink := NewFakeSink(32)
	tx := NewFakeDeclarativeTx(desc, sink)
	tx.SetTemplate(sampleTemplateID, "host={{host}}\n")
	tx.SeedFile(declaredPath, []byte("# header\n"), defaultFilePerm, contract.Owner{})

	if err := tx.EnsureBlock(ctx, declaredPath, contract.ManagedBlock{
		ID:    sampleBlockID,
		Begin: "# begin wskit-block",
		End:   "# end wskit-block",
		Body:  "value=1",
	}); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	entry, ok := tx.File(declaredPath)
	if !ok || !strings.Contains(string(entry.Data), "# header\n") || !strings.Contains(string(entry.Data), "value=1") {
		t.Fatalf("unexpected file content after EnsureBlock: %q", entry.Data)
	}

	if err := tx.RemoveBlock(ctx, declaredPath, sampleBlockID); err != nil {
		t.Fatalf("RemoveBlock: %v", err)
	}
	entry, _ = tx.File(declaredPath)
	if strings.Contains(string(entry.Data), "value=1") || !strings.Contains(string(entry.Data), "# header") {
		t.Fatalf("unexpected file content after RemoveBlock: %q", entry.Data)
	}

	if err := tx.RenderTemplate(ctx, declaredPath, sampleTemplateID, contract.TemplateVars{"host": "acme-ws-001"}, defaultFilePerm, contract.Owner{}); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	entry, _ = tx.File(declaredPath)
	if string(entry.Data) != "host=acme-ws-001\n" {
		t.Fatalf("rendered template = %q, want %q", entry.Data, "host=acme-ws-001\n")
	}

	if err := tx.Package(ctx, contract.PackageState{Name: "zsh", Present: true}); err != nil {
		t.Fatalf("Package: %v", err)
	}
	if err := tx.Service(ctx, contract.ServiceState{Name: "ssh", Enabled: true, Running: true}); err != nil {
		t.Fatalf("Service: %v", err)
	}
	if err := tx.UserGroup(ctx, contract.UserGroupState{User: "agent-01", Group: "users", Present: true}); err != nil {
		t.Fatalf("UserGroup: %v", err)
	}
	res, err := tx.Check(ctx, contract.CheckSpec{Name: contract.CheckName("sshd-test")})
	if err != nil || !res.OK {
		t.Fatalf("Check = %+v, err=%v", res, err)
	}

	changes := tx.Changes()
	if len(changes) == 0 || changes[0].Backup == "" {
		t.Fatalf("expected recorded changes with backup ref, got %+v", changes)
	}
	if _, ok := tx.Backup(changes[0].Backup); !ok {
		t.Fatalf("backup %q not found", changes[0].Backup)
	}
}

type validatingInputsUnit struct{ *FileUnit }

func (u *validatingInputsUnit) Plan(ctx context.Context, in contract.Inputs) (contract.Plan, error) {
	if !in.FormFactor.Valid() {
		return contract.Plan{}, fmt.Errorf("invalid form factor %q", in.FormFactor)
	}
	if !in.Topology.Valid() {
		return contract.Plan{}, fmt.Errorf("invalid topology %q", in.Topology)
	}
	return u.FileUnit.Plan(ctx, in)
}

func TestCheckUnitPassesValidFormFactorAndTopology(t *testing.T) {
	t.Parallel()

	u := &validatingInputsUnit{NewFileUnit(contract.UnitShell, declaredPath, []byte("ok\n"))}
	rep := &fakeReporter{}
	checkUnit(rep, u)
	if len(rep.errors) > 0 {
		t.Fatalf("checkUnit passed invalid FormFactor/Topology: %v", rep.errors)
	}
}

func TestFakeDeclarativeTxRefusesUndeclaredResourceTargets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	desc := contract.Description{
		Unit:  contract.UnitPackages,
		Scope: contract.ScopeRoot,
		OwnedStateKeys: []contract.StateKey{
			"package:zsh",
			"service:ssh",
			"user-group:agent-01:users",
		},
	}
	tx := NewFakeDeclarativeTx(desc, nil)

	assertUndeclared := func(op, wantTarget string, err error) {
		t.Helper()
		var targetErr *contract.UndeclaredTargetError
		if !errors.As(err, &targetErr) || targetErr.Kind != contract.TargetStateKey || targetErr.Target != wantTarget {
			t.Errorf("%s undeclared err = %v, want UndeclaredTargetError{state-key, %q}", op, err, wantTarget)
		}
	}

	assertUndeclared("Package", "package:curl", tx.Package(ctx, contract.PackageState{Name: "curl", Present: true}))
	assertUndeclared("Service", "service:cron", tx.Service(ctx, contract.ServiceState{Name: "cron", Enabled: true}))
	assertUndeclared("UserGroup", "user-group:agent-01:sudo", tx.UserGroup(ctx, contract.UserGroupState{User: "agent-01", Group: "sudo", Present: true}))

	if err := tx.Package(ctx, contract.PackageState{Name: "zsh", Present: true}); err != nil {
		t.Fatalf("declared Package failed: %v", err)
	}
	if err := tx.Service(ctx, contract.ServiceState{Name: "ssh", Enabled: true, Running: true}); err != nil {
		t.Fatalf("declared Service failed: %v", err)
	}
	if err := tx.UserGroup(ctx, contract.UserGroupState{User: "agent-01", Group: "users", Present: true}); err != nil {
		t.Fatalf("declared UserGroup failed: %v", err)
	}

	changes := tx.Changes()
	if len(changes) != 3 {
		t.Fatalf("got %d recorded changes, want 3", len(changes))
	}
	for _, ch := range changes {
		if ch.Backup == "" {
			t.Errorf("change %q missing backup reference", ch.Target)
		} else if _, ok := tx.Backup(ch.Backup); !ok {
			t.Errorf("change %q backup %q not stored", ch.Target, ch.Backup)
		}
	}
}

type contractOnlyStateProvider struct {
	inner contract.StateProvider
}

func (p *contractOnlyStateProvider) Name() contract.StateProviderName { return p.inner.Name() }
func (p *contractOnlyStateProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	return p.inner.Read(ctx, key)
}
func (p *contractOnlyStateProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	return p.inner.Write(ctx, key, want)
}
func (p *contractOnlyStateProvider) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	return p.inner.Snapshot(ctx)
}
func (p *contractOnlyStateProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	return p.inner.Restore(ctx, s)
}

func TestFakeTxSetStateUsesContractMethodsOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tx := NewFakeTx(standardTestDescription(), nil)
	raw := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
		declaredState: {Value: "lab-ws-01"},
	})
	provider := &contractOnlyStateProvider{inner: raw}

	want := contract.State{Value: "acme-ws-001"}
	if err := tx.SetState(ctx, provider, declaredState, want); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	got, err := provider.Read(ctx, declaredState)
	if err != nil {
		t.Fatalf("Read after SetState: %v", err)
	}
	if got != want {
		t.Fatalf("Read after SetState = %+v, want %+v", got, want)
	}
}

func TestFakeDeclarativeTxRemoveBlockExactMarkers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tx := NewFakeDeclarativeTx(standardTestDescription(), nil)
	initial := strings.Join([]string{
		"# unrelated line mentioning wskit-block stays untouched",
		"# begin wskit-block-other",
		"other=1",
		"# end wskit-block-other",
		"prefix # begin wskit-block not a real marker",
		"",
	}, "\n")
	tx.SeedFile(declaredPath, []byte(initial), defaultFilePerm, contract.Owner{})

	if err := tx.EnsureBlock(ctx, declaredPath, contract.ManagedBlock{
		ID:    sampleBlockID,
		Begin: "# begin wskit-block",
		End:   "# end wskit-block",
		Body:  "managed=1",
	}); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	if err := tx.RemoveBlock(ctx, declaredPath, sampleBlockID); err != nil {
		t.Fatalf("RemoveBlock: %v", err)
	}
	entry, ok := tx.File(declaredPath)
	if !ok {
		t.Fatal("declared file missing after RemoveBlock")
	}
	if string(entry.Data) != initial {
		t.Fatalf("content after RemoveBlock =\n%q\nwant exact initial:\n%q", string(entry.Data), initial)
	}
}

type brokenMutateBeforeRefuseWriteTx struct{ *FakeTx }

func (b *brokenMutateBeforeRefuseWriteTx) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode, owner contract.Owner) error {
	if path == undeclaredPath {
		b.SeedFile(path, data, perm, owner)
		return &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	return b.FakeTx.WriteFile(ctx, path, data, perm, owner)
}

type brokenNoOpSetStateTx struct{ *FakeTx }

func (b *brokenNoOpSetStateTx) SetState(ctx context.Context, p contract.StateProvider, key contract.StateKey, want contract.State) error {
	if key == undeclaredState {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	// Record a change without actually updating p.
	snap, err := p.Snapshot(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	backup := b.recordBackupLocked(snap.Data)
	b.changes = append(b.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: string(p.Name()) + ":" + string(key),
			Action: contract.ActionSet,
			After:  digestBytes([]byte(want.Value)),
			Risk:   contract.RiskMedium,
		},
		Backup: backup,
	})
	b.mu.Unlock()
	return nil
}

func TestBrokenMutateBeforeRefuseAndNoOpSetStateFailContractCheck(t *testing.T) {
	t.Parallel()

	t.Run("mutates backing state before refusing WriteFile", func(t *testing.T) {
		t.Parallel()
		rep := &fakeReporter{}
		checkTx(rep, func(d contract.Description) contract.Tx {
			return &brokenMutateBeforeRefuseWriteTx{NewFakeTx(d, nil)}
		})
		if !rep.hasErrorContaining("mutated backing state") {
			t.Fatalf("expected backing-state mutation failure, got %v", rep.errors)
		}
	})

	t.Run("SetState does not persist value to provider", func(t *testing.T) {
		t.Parallel()
		rep := &fakeReporter{}
		checkTx(rep, func(d contract.Description) contract.Tx {
			return &brokenNoOpSetStateTx{NewFakeTx(d, nil)}
		})
		if !rep.hasErrorContaining("StateProvider.Read") {
			t.Fatalf("expected StateProvider.Read mismatch failure, got %v", rep.errors)
		}
	})
}

// --- Deliberately broken fakes proving every contract check can fail ---

type brokenAllowUndeclaredWriteTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredWriteTx) WriteFile(_ context.Context, _ string, _ []byte, _ fs.FileMode, _ contract.Owner) error {
	return nil
}

type brokenAllowUndeclaredRenderTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredRenderTx) RenderTemplate(_ context.Context, _ string, _ contract.TemplateID, _ contract.TemplateVars, _ fs.FileMode, _ contract.Owner) error {
	return nil
}

type brokenAllowUndeclaredEnsureBlockTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredEnsureBlockTx) EnsureBlock(_ context.Context, _ string, _ contract.ManagedBlock) error {
	return nil
}

type brokenAllowUndeclaredRemoveBlockTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredRemoveBlockTx) RemoveBlock(_ context.Context, _ string, _ contract.BlockID) error {
	return nil
}

type brokenAllowUndeclaredRemoveTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredRemoveTx) Remove(_ context.Context, _ string) error {
	return nil
}

type brokenAllowUndeclaredPackageTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredPackageTx) Package(_ context.Context, _ contract.PackageState) error {
	return nil
}

type brokenAllowUndeclaredServiceTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredServiceTx) Service(_ context.Context, _ contract.ServiceState) error {
	return nil
}

type brokenAllowUndeclaredUserGroupTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredUserGroupTx) UserGroup(_ context.Context, _ contract.UserGroupState) error {
	return nil
}

type brokenMutateBeforeRefusePackageTx struct{ *FakeTx }

func (b *brokenMutateBeforeRefusePackageTx) Package(ctx context.Context, want contract.PackageState) error {
	if want.Name == undeclaredPackage {
		b.SeedPackage(want)
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(undeclaredPackageKey)}
	}
	return b.FakeTx.Package(ctx, want)
}

type brokenMutateBeforeRefuseStateTx struct{ *FakeTx }

func (b *brokenMutateBeforeRefuseStateTx) SetState(ctx context.Context, p contract.StateProvider, key contract.StateKey, want contract.State) error {
	if key == undeclaredState {
		if err := p.Restore(ctx, contract.Snapshot{Provider: p.Name(), Data: []byte(`{"hostname.undeclared":{"value":"leaked"}}`)}); err != nil {
			return err
		}
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	return b.FakeTx.SetState(ctx, p, key, want)
}

type brokenMutateBeforeRefuseRunTx struct{ *FakeTx }

func (b *brokenMutateBeforeRefuseRunTx) Run(ctx context.Context, c contract.Command) (contract.CommandResult, error) {
	for _, k := range c.EnvKeys {
		if k == undeclaredEnvKey {
			b.runMu.Lock()
			b.commands = append(b.commands, c)
			b.runMu.Unlock()
			return contract.CommandResult{}, &contract.UndeclaredTargetError{Kind: contract.TargetEnvKey, Target: k}
		}
	}
	return b.FakeTx.Run(ctx, c)
}

type brokenUntypedErrorWriteTx struct{ *FakeTx }

func (b *brokenUntypedErrorWriteTx) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode, owner contract.Owner) error {
	if path == undeclaredPath {
		return errors.New("plain untyped refusal")
	}
	return b.FakeTx.WriteFile(ctx, path, data, perm, owner)
}

type brokenAllowUndeclaredStateTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredStateTx) SetState(_ context.Context, _ contract.StateProvider, _ contract.StateKey, _ contract.State) error {
	return nil
}

type brokenNoStateReceiptTx struct{ *FakeTx }

func (b *brokenNoStateReceiptTx) SetState(_ context.Context, _ contract.StateProvider, key contract.StateKey, _ contract.State) error {
	if key == undeclaredState {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	return nil
}

type brokenUntypedErrorStateTx struct{ *FakeTx }

func (b *brokenUntypedErrorStateTx) SetState(ctx context.Context, p contract.StateProvider, key contract.StateKey, want contract.State) error {
	if key == undeclaredState {
		return errors.New("untyped state refusal")
	}
	return b.FakeTx.SetState(ctx, p, key, want)
}

type brokenAllowUndeclaredEnvTx struct{ *FakeTx }

func (b *brokenAllowUndeclaredEnvTx) Run(_ context.Context, _ contract.Command) (contract.CommandResult, error) {
	return contract.CommandResult{}, nil
}

type brokenNoReceiptTx struct{ *FakeTx }

func (b *brokenNoReceiptTx) Changes() []contract.AppliedChange {
	return nil
}

type brokenReversedEmitTx struct{ *FakeTx }

func (b *brokenReversedEmitTx) Events() []contract.Event {
	ev := b.FakeTx.Events()
	for i, j := 0, len(ev)-1; i < j; i, j = i+1, j-1 {
		ev[i], ev[j] = ev[j], ev[i]
	}
	return ev
}

type brokenBlockingSink struct {
	unblock <-chan struct{}
}

func (s *brokenBlockingSink) Deliver(_ contract.Event) {
	<-s.unblock
}

type brokenEmptyNameProvider struct{ *FakeStateProvider }

func (p *brokenEmptyNameProvider) Name() contract.StateProviderName { return "" }

type brokenNoRestoreProvider struct{ *FakeStateProvider }

func (p *brokenNoRestoreProvider) Restore(_ context.Context, _ contract.Snapshot) error {
	return nil
}

type brokenNoWriteProvider struct{ *FakeStateProvider }

func (p *brokenNoWriteProvider) Write(_ context.Context, _ contract.StateKey, _ contract.State) error {
	return nil
}

type brokenAcceptWrongProvider struct{ *FakeStateProvider }

func (p *brokenAcceptWrongProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	s.Provider = p.Name()
	return p.FakeStateProvider.Restore(ctx, s)
}

type brokenInvalidIDUnit struct{ *FileUnit }

func (u *brokenInvalidIDUnit) ID() contract.UnitID { return contract.UnitID("not-a-unit") }

type brokenMismatchedDescribeUnit struct{ *FileUnit }

func (u *brokenMismatchedDescribeUnit) Describe(ctx context.Context) (contract.Description, error) {
	d, err := u.FileUnit.Describe(ctx)
	d.Unit = contract.UnitSSH
	return d, err
}

type brokenUndeclaredApplyUnit struct{ *FileUnit }

func (u *brokenUndeclaredApplyUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return tx.WriteFile(ctx, undeclaredPath, []byte("rogue"), defaultFilePerm, contract.Owner{})
}

type brokenInvalidVerifyUnit struct{ *FileUnit }

func (u *brokenInvalidVerifyUnit) Verify(_ context.Context, _ contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: u.ID(), Outcome: contract.Outcome("succeeded")}, nil
}

func TestContractChecksCatchBrokenTxFakes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		newTx   func(contract.Description) contract.Tx
		wantErr string
	}{
		{
			name:    "allows undeclared WriteFile",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredWriteTx{NewFakeTx(d, nil)} },
			wantErr: "WriteFile on undeclared path",
		},
		{
			name:    "allows undeclared RenderTemplate",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredRenderTx{NewFakeTx(d, nil)} },
			wantErr: "RenderTemplate on undeclared path",
		},
		{
			name: "allows undeclared EnsureBlock",
			newTx: func(d contract.Description) contract.Tx {
				return &brokenAllowUndeclaredEnsureBlockTx{NewFakeTx(d, nil)}
			},
			wantErr: "EnsureBlock on undeclared path",
		},
		{
			name: "allows undeclared RemoveBlock",
			newTx: func(d contract.Description) contract.Tx {
				return &brokenAllowUndeclaredRemoveBlockTx{NewFakeTx(d, nil)}
			},
			wantErr: "RemoveBlock on undeclared path",
		},
		{
			name:    "allows undeclared Remove",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredRemoveTx{NewFakeTx(d, nil)} },
			wantErr: "Remove on undeclared path",
		},
		{
			name:    "allows undeclared Package",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredPackageTx{NewFakeTx(d, nil)} },
			wantErr: "Package on undeclared state-key",
		},
		{
			name:    "allows undeclared Service",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredServiceTx{NewFakeTx(d, nil)} },
			wantErr: "Service on undeclared state-key",
		},
		{
			name:    "allows undeclared UserGroup",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredUserGroupTx{NewFakeTx(d, nil)} },
			wantErr: "UserGroup on undeclared state-key",
		},
		{
			name: "mutates file before refusing WriteFile",
			newTx: func(d contract.Description) contract.Tx {
				return &brokenMutateBeforeRefuseWriteTx{NewFakeTx(d, nil)}
			},
			wantErr: "WriteFile mutated backing state",
		},
		{
			name: "mutates package before refusing Package",
			newTx: func(d contract.Description) contract.Tx {
				return &brokenMutateBeforeRefusePackageTx{NewFakeTx(d, nil)}
			},
			wantErr: "Package mutated backing state",
		},
		{
			name: "mutates provider before refusing SetState",
			newTx: func(d contract.Description) contract.Tx {
				return &brokenMutateBeforeRefuseStateTx{NewFakeTx(d, nil)}
			},
			wantErr: "SetState mutated backing state",
		},
		{
			name: "records command before refusing Run",
			newTx: func(d contract.Description) contract.Tx {
				return &brokenMutateBeforeRefuseRunTx{NewFakeTx(d, nil)}
			},
			wantErr: "Run mutated backing state",
		},
		{
			name:    "SetState does not update provider",
			newTx:   func(d contract.Description) contract.Tx { return &brokenNoOpSetStateTx{NewFakeTx(d, nil)} },
			wantErr: "StateProvider.Read",
		},
		{
			name:    "untyped error on undeclared WriteFile",
			newTx:   func(d contract.Description) contract.Tx { return &brokenUntypedErrorWriteTx{NewFakeTx(d, nil)} },
			wantErr: "not *contract.UndeclaredTargetError",
		},
		{
			name:    "allows undeclared SetState",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredStateTx{NewFakeTx(d, nil)} },
			wantErr: "SetState on undeclared state-key",
		},
		{
			name:    "omits SetState from receipt",
			newTx:   func(d contract.Description) contract.Tx { return &brokenNoStateReceiptTx{NewFakeTx(d, nil)} },
			wantErr: "SetState on declared key",
		},
		{
			name:    "untyped error on undeclared SetState",
			newTx:   func(d contract.Description) contract.Tx { return &brokenUntypedErrorStateTx{NewFakeTx(d, nil)} },
			wantErr: "not *contract.UndeclaredTargetError",
		},
		{
			name:    "allows undeclared env key in Run",
			newTx:   func(d contract.Description) contract.Tx { return &brokenAllowUndeclaredEnvTx{NewFakeTx(d, nil)} },
			wantErr: "Run on undeclared env-key",
		},
		{
			name:    "omits mutation from receipt",
			newTx:   func(d contract.Description) contract.Tx { return &brokenNoReceiptTx{NewFakeTx(d, nil)} },
			wantErr: "not recorded for the receipt",
		},
		{
			name:    "reorders emitted events",
			newTx:   func(d contract.Description) contract.Tx { return &brokenReversedEmitTx{NewFakeTx(d, nil)} },
			wantErr: "Emit must preserve order",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep := &fakeReporter{}
			checkTx(rep, tc.newTx)
			if !rep.hasErrorContaining(tc.wantErr) {
				t.Fatalf("expected failure containing %q, got %v", tc.wantErr, rep.errors)
			}
		})
	}
}

func TestContractChecksCatchBlockingSink(t *testing.T) {
	t.Parallel()

	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	rep := &fakeReporter{}
	checkSink(rep, &brokenBlockingSink{unblock: unblock}, 20*time.Millisecond)
	if !rep.hasErrorContaining("Sink.Deliver blocked") {
		t.Fatalf("expected blocking sink failure, got %v", rep.errors)
	}
}

func TestContractChecksCatchBrokenStateProviders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		make    func() (contract.StateProvider, func(context.Context) error)
		wantErr string
	}{
		{
			name: "empty provider name",
			make: func() (contract.StateProvider, func(context.Context) error) {
				base := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
					declaredState: {Value: "lab-ws-01"},
				})
				return &brokenEmptyNameProvider{base}, func(ctx context.Context) error {
					return base.Write(ctx, declaredState, contract.State{Value: "changed"})
				}
			},
			wantErr: "Name() must not be empty",
		},
		{
			name: "restore does not revert mutation",
			make: func() (contract.StateProvider, func(context.Context) error) {
				base := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
					declaredState: {Value: "lab-ws-01"},
				})
				return &brokenNoRestoreProvider{base}, func(ctx context.Context) error {
					return base.Write(ctx, declaredState, contract.State{Value: "changed"})
				}
			},
			wantErr: "state after Restore",
		},
		{
			name: "write does not change state",
			make: func() (contract.StateProvider, func(context.Context) error) {
				base := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
					declaredState: {Value: "lab-ws-01"},
				})
				return &brokenNoWriteProvider{base}, func(ctx context.Context) error {
					return base.Write(ctx, declaredState, contract.State{Value: "changed"})
				}
			},
			wantErr: "state after Write",
		},
		{
			name: "restore accepts wrong provider snapshot",
			make: func() (contract.StateProvider, func(context.Context) error) {
				base := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
					declaredState: {Value: "lab-ws-01"},
				})
				return &brokenAcceptWrongProvider{base}, func(ctx context.Context) error {
					return base.Write(ctx, declaredState, contract.State{Value: "changed"})
				}
			},
			wantErr: "accepted snapshot from wrong provider",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, mutate := tc.make()
			rep := &fakeReporter{}
			checkStateProvider(rep, p, declaredState, mutate)
			if !rep.hasErrorContaining(tc.wantErr) {
				t.Fatalf("expected failure containing %q, got %v", tc.wantErr, rep.errors)
			}
		})
	}
}

func TestContractChecksCatchBrokenUnits(t *testing.T) {
	t.Parallel()

	newBase := func() *FileUnit {
		return NewFileUnit(contract.UnitShell, declaredPath, []byte("ok\n"))
	}

	cases := []struct {
		name    string
		unit    contract.Unit
		wantErr string
	}{
		{
			name:    "invalid unit ID",
			unit:    &brokenInvalidIDUnit{newBase()},
			wantErr: "not a valid UnitID",
		},
		{
			name:    "mismatched Describe.Unit",
			unit:    &brokenMismatchedDescribeUnit{newBase()},
			wantErr: "Describe.Unit",
		},
		{
			name:    "Apply writes undeclared path",
			unit:    &brokenUndeclaredApplyUnit{newBase()},
			wantErr: "Apply failed",
		},
		{
			name:    "Verify returns invalid Outcome",
			unit:    &brokenInvalidVerifyUnit{newBase()},
			wantErr: "Verify.Outcome",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep := &fakeReporter{}
			checkUnit(rep, tc.unit)
			if !rep.hasErrorContaining(tc.wantErr) {
				t.Fatalf("expected failure containing %q, got %v", tc.wantErr, rep.errors)
			}
		})
	}
}
