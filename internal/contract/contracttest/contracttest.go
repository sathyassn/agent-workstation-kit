package contracttest

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

const (
	declaredPath           = "/etc/agent-workstation-kit/declared.conf"
	undeclaredPath         = "/etc/agent-workstation-kit/undeclared.conf"
	declaredState          = contract.StateKey("hostname.pretty")
	undeclaredState        = contract.StateKey("hostname.undeclared")
	declaredPackage        = "zsh"
	undeclaredPackage      = "undeclared-pkg"
	declaredPackageKey     = contract.StateKey(targetPrefixPackage + declaredPackage)
	undeclaredPackageKey   = contract.StateKey(targetPrefixPackage + undeclaredPackage)
	declaredService        = "ssh"
	undeclaredService      = "undeclared-svc"
	declaredServiceKey     = contract.StateKey(targetPrefixService + declaredService)
	undeclaredServiceKey   = contract.StateKey(targetPrefixService + undeclaredService)
	declaredUser           = "agent-01"
	declaredGroup          = "users"
	undeclaredGroup        = "undeclared-grp"
	declaredUserGroupKey   = contract.StateKey(targetPrefixUserGroup + declaredUser + ":" + declaredGroup)
	undeclaredUserGroupKey = contract.StateKey(targetPrefixUserGroup + declaredUser + ":" + undeclaredGroup)
	declaredEnvKey         = "WSKIT_DECLARED_ENV"
	undeclaredEnvKey       = "WSKIT_UNDECLARED_ENV"
	sampleTemplateID       = contract.TemplateID("sample-tpl")
	sampleBlockID          = contract.BlockID("wskit-block")
	defaultSinkWait        = 250 * time.Millisecond
	sinkBurstCount         = 512
	emitOrderCount         = 8
)

type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// TestUnit verifies that u obeys the contract.Unit lifecycle: valid ID and
// Description, deterministic Plan, Apply touching only declared targets,
// Verify, Status and Revert.
func TestUnit(t *testing.T, u contract.Unit) {
	t.Helper()
	checkUnit(t, u)
}

// TestDeclarativeTx verifies that transactions produced by newTx refuse
// undeclared paths with *contract.UndeclaredTargetError, record declared
// mutations for the receipt and keep emitted events in order.
func TestDeclarativeTx(t *testing.T, newTx func(contract.Description) contract.DeclarativeTx) {
	t.Helper()
	checkDeclarativeTx(t, newTx)
}

// TestTx verifies that transactions produced by newTx obey every DeclarativeTx
// invariant plus undeclared state-key and environment-key refusals and receipt
// recording for SetState.
func TestTx(t *testing.T, newTx func(contract.Description) contract.Tx) {
	t.Helper()
	checkTx(t, newTx)
}

// TestSink verifies that s.Deliver never blocks under a burst of events.
func TestSink(t *testing.T, s contract.Sink) {
	t.Helper()
	checkSink(t, s, defaultSinkWait)
}

// TestStateProvider verifies that p reports a non-empty Name, snapshots and
// restores state across a mutation, and rejects a snapshot from another
// provider.
func TestStateProvider(
	t *testing.T,
	p contract.StateProvider,
	key contract.StateKey,
	mutate func(ctx context.Context) error,
) {
	t.Helper()
	checkStateProvider(t, p, key, mutate)
}

func checkUnit(r reporter, u contract.Unit) {
	r.Helper()

	ctx := context.Background()
	id := u.ID()
	if !id.Valid() {
		r.Errorf("unit ID %q is not a valid UnitID", id)
		return
	}

	desc, err := u.Describe(ctx)
	if err != nil {
		r.Errorf("Describe failed: %v", err)
		return
	}
	if desc.Unit != id {
		r.Errorf("Describe.Unit = %q, want %q", desc.Unit, id)
	}
	if !desc.Scope.Valid() {
		r.Errorf("Describe.Scope %q is not valid", desc.Scope)
	}
	for _, dep := range desc.DependsOn {
		if !dep.Valid() {
			r.Errorf("Describe.DependsOn contains invalid UnitID %q", dep)
		}
	}

	in := contract.Inputs{
		Config: map[contract.ConfigKey]string{},
		Platform: contract.PlatformFacts{
			OS:        "linux",
			Arch:      "amd64",
			OSVersion: "24.04",
		},
		FormFactor: contract.FormFactorFixed,
		Topology:   contract.TopologySeparate,
	}
	plan, err := u.Plan(ctx, in)
	if err != nil {
		r.Errorf("Plan failed: %v", err)
		return
	}
	if plan.Unit != id {
		r.Errorf("Plan.Unit = %q, want %q", plan.Unit, id)
	}
	if plan.ID == "" {
		r.Errorf("Plan.ID must not be empty")
	}
	if plan.Digest == "" {
		r.Errorf("Plan.Digest must not be empty")
	}

	sink := NewFakeSink(sinkBurstCount)
	tx := NewFakeTx(desc, sink)
	if err := u.Apply(ctx, tx, plan); err != nil {
		r.Errorf("Apply failed: %v", err)
		return
	}

	res, err := u.Verify(ctx, plan)
	if err != nil {
		r.Errorf("Verify failed: %v", err)
		return
	}
	if res.Unit != id {
		r.Errorf("Verify.Unit = %q, want %q", res.Unit, id)
	}
	if !res.Outcome.Valid() {
		r.Errorf("Verify.Outcome %q is not valid", res.Outcome)
	}

	st, err := u.Status(ctx)
	if err != nil {
		r.Errorf("Status failed: %v", err)
		return
	}
	if st.Unit != id {
		r.Errorf("Status.Unit = %q, want %q", st.Unit, id)
	}

	receipt := tx.Receipt(contract.RunID("run-contract-test"), plan.Digest, res.Outcome)
	if err := u.Revert(ctx, tx, receipt); err != nil {
		r.Errorf("Revert failed: %v", err)
	}
}

func standardTestDescription() contract.Description {
	return contract.Description{
		Unit:         contract.UnitShell,
		Scope:        contract.ScopeUser,
		DependsOn:    []contract.UnitID{},
		Capabilities: []contract.Capability{},
		OwnedPaths:   []string{declaredPath},
		OwnedStateKeys: []contract.StateKey{
			declaredState,
			declaredPackageKey,
			declaredServiceKey,
			declaredUserGroupKey,
		},
		Irreversible: []string{},
		DeclaredEnv:  []string{declaredEnvKey},
	}
}

// recordingStateProvider wraps the fake provider for checkTx. It records
// each contract call in order and the snapshots it hands out, and can make
// Write change the key and then fail, so the check can see whether a Tx
// snapshots before writing and restores after a failed write.
type recordingStateProvider struct {
	inner *FakeStateProvider

	mu        sync.Mutex
	calls     []string
	snapshots []contract.Snapshot
	failWrite bool
}

var errInjectedWrite = errors.New("injected write failure")

func (p *recordingStateProvider) record(call string) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
}

// reset clears the call log and returns what it held.
func (p *recordingStateProvider) reset() ([]string, []contract.Snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls, snaps := p.calls, p.snapshots
	p.calls, p.snapshots = nil, nil
	return calls, snaps
}

func (p *recordingStateProvider) setFailWrite(fail bool) {
	p.mu.Lock()
	p.failWrite = fail
	p.mu.Unlock()
}

func (p *recordingStateProvider) Name() contract.StateProviderName { return p.inner.Name() }
func (p *recordingStateProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	p.record(callRead)
	return p.inner.Read(ctx, key)
}
func (p *recordingStateProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	p.record(callWrite)
	if err := p.inner.Write(ctx, key, want); err != nil {
		return err
	}
	p.mu.Lock()
	fail := p.failWrite
	p.mu.Unlock()
	if fail {
		return errInjectedWrite
	}
	return nil
}
func (p *recordingStateProvider) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	p.record(callSnapshot)
	snap, err := p.inner.Snapshot(ctx)
	if err == nil {
		p.mu.Lock()
		p.snapshots = append(p.snapshots, snap)
		p.mu.Unlock()
	}
	return snap, err
}
func (p *recordingStateProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	p.record(callRestore)
	return p.inner.Restore(ctx, s)
}

const (
	callRead     = "Read"
	callWrite    = "Write"
	callSnapshot = "Snapshot"
	callRestore  = "Restore"
)

func indexOf(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}
	return -1
}

func checkDeclarativeTx(r reporter, newTx func(contract.Description) contract.DeclarativeTx) {
	r.Helper()

	ctx := context.Background()
	desc := standardTestDescription()
	tx := newTx(desc)

	inspector, ok := tx.(TxInspector)
	if !ok {
		r.Errorf("DeclarativeTx %T must implement TxInspector for contract verification", tx)
		return
	}

	assertRefusedUnchanged(r, "WriteFile", inspector, contract.TargetPath, undeclaredPath, func() error {
		return tx.WriteFile(ctx, undeclaredPath, []byte("x"), defaultFilePerm, contract.Owner{})
	})
	assertRefusedUnchanged(r, "RenderTemplate", inspector, contract.TargetPath, undeclaredPath, func() error {
		return tx.RenderTemplate(ctx, undeclaredPath, sampleTemplateID, contract.TemplateVars{}, defaultFilePerm, contract.Owner{})
	})
	assertRefusedUnchanged(r, "EnsureBlock", inspector, contract.TargetPath, undeclaredPath, func() error {
		return tx.EnsureBlock(ctx, undeclaredPath, contract.ManagedBlock{ID: sampleBlockID, Begin: "# begin", End: "# end", Body: "x"})
	})
	assertRefusedUnchanged(r, "RemoveBlock", inspector, contract.TargetPath, undeclaredPath, func() error {
		return tx.RemoveBlock(ctx, undeclaredPath, sampleBlockID)
	})
	assertRefusedUnchanged(r, "Remove", inspector, contract.TargetPath, undeclaredPath, func() error {
		return tx.Remove(ctx, undeclaredPath)
	})
	assertRefusedUnchanged(r, "Package", inspector, contract.TargetStateKey, string(undeclaredPackageKey), func() error {
		return tx.Package(ctx, contract.PackageState{Name: undeclaredPackage, Present: true})
	})
	assertRefusedUnchanged(r, "Service", inspector, contract.TargetStateKey, string(undeclaredServiceKey), func() error {
		return tx.Service(ctx, contract.ServiceState{Name: undeclaredService, Enabled: true, Running: true})
	})
	assertRefusedUnchanged(r, "UserGroup", inspector, contract.TargetStateKey, string(undeclaredUserGroupKey), func() error {
		return tx.UserGroup(ctx, contract.UserGroupState{User: declaredUser, Group: undeclaredGroup, Present: true})
	})

	if changes := inspector.Changes(); len(changes) != 0 {
		r.Errorf("refused undeclared mutations recorded %d changes, want 0", len(changes))
	}

	if err := tx.WriteFile(ctx, declaredPath, []byte("hello\n"), defaultFilePerm, contract.Owner{}); err != nil {
		r.Errorf("WriteFile on declared path failed: %v", err)
	}
	assertRecordedChange(r, "WriteFile", inspector.Changes(), declaredPath, false)

	if err := tx.Package(ctx, contract.PackageState{Name: declaredPackage, Present: true}); err != nil {
		r.Errorf("Package on declared target failed: %v", err)
	}
	assertRecordedChange(r, "Package", inspector.Changes(), string(declaredPackageKey), true)

	if err := tx.Service(ctx, contract.ServiceState{Name: declaredService, Enabled: true, Running: true}); err != nil {
		r.Errorf("Service on declared target failed: %v", err)
	}
	assertRecordedChange(r, "Service", inspector.Changes(), string(declaredServiceKey), true)

	if err := tx.UserGroup(ctx, contract.UserGroupState{User: declaredUser, Group: declaredGroup, Present: true}); err != nil {
		r.Errorf("UserGroup on declared target failed: %v", err)
	}
	assertRecordedChange(r, "UserGroup", inspector.Changes(), string(declaredUserGroupKey), true)

	checkEmitOrder(r, tx, inspector)
}

func checkTx(r reporter, newTx func(contract.Description) contract.Tx) {
	r.Helper()

	checkDeclarativeTx(r, func(d contract.Description) contract.DeclarativeTx {
		return newTx(d)
	})

	ctx := context.Background()
	desc := standardTestDescription()
	tx := newTx(desc)

	inspector, ok := tx.(TxInspector)
	if !ok {
		r.Errorf("Tx %T must implement TxInspector for contract verification", tx)
		return
	}

	initialState := contract.State{Value: "lab-ws-01"}
	provider := &recordingStateProvider{inner: NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
		declaredState: initialState,
	})}

	beforeProvSnap, err := provider.Snapshot(ctx)
	if err != nil {
		r.Errorf("StateProvider.Snapshot before refused SetState failed: %v", err)
		return
	}
	provider.reset()
	assertRefusedUnchanged(r, "SetState", inspector, contract.TargetStateKey, string(undeclaredState), func() error {
		return tx.SetState(ctx, provider, undeclaredState, contract.State{Value: "bad"})
	})
	if calls, _ := provider.reset(); len(calls) != 0 {
		r.Errorf("SetState on undeclared key called the provider %v before refusing; want no provider call", calls)
	}
	afterProvSnap, err := provider.Snapshot(ctx)
	if err != nil {
		r.Errorf("StateProvider.Snapshot after refused SetState failed: %v", err)
		return
	}
	if !bytes.Equal(beforeProvSnap.Data, afterProvSnap.Data) {
		r.Errorf("SetState mutated backing state of provider despite returning refusal error")
	}

	beforeRun := inspector.BackingState()
	_, runErr := tx.Run(ctx, contract.Command{
		Argv:    []string{"/usr/bin/true"},
		EnvKeys: []string{undeclaredEnvKey},
	})
	assertUndeclaredError(r, "Run", contract.TargetEnvKey, undeclaredEnvKey, runErr)
	if !bytes.Equal(beforeRun, inspector.BackingState()) {
		r.Errorf("Run mutated backing state despite returning refusal error")
	}

	checkSetStateWriteFailure(ctx, r, tx, inspector, provider, initialState)

	provider.reset()
	beforeCount := len(inspector.Changes())
	wantState := contract.State{Value: "acme-ws-001"}
	if err := tx.SetState(ctx, provider, declaredState, wantState); err != nil {
		r.Errorf("SetState on declared key failed: %v", err)
	}
	calls, snaps := provider.reset()
	checkSnapshotBeforeWrite(ctx, r, provider, calls, snaps, initialState)

	changes := inspector.Changes()
	if len(changes) <= beforeCount {
		r.Errorf("SetState on declared key %q was not recorded for the receipt", declaredState)
	} else {
		assertRecordedChange(r, "SetState", changes[beforeCount:], string(provider.Name())+":"+string(declaredState), true)
	}

	// The restore check above put the key back; write it again so the
	// read-back below sees the Tx's own effect.
	if err := tx.SetState(ctx, provider, declaredState, wantState); err != nil {
		r.Errorf("SetState on declared key failed: %v", err)
	}
	gotState, err := provider.Read(ctx, declaredState)
	if err != nil {
		r.Errorf("StateProvider.Read(%q) after SetState failed: %v", declaredState, err)
	} else if gotState != wantState {
		r.Errorf("StateProvider.Read(%q) after SetState = %+v, want %+v", declaredState, gotState, wantState)
	}
}

// checkSnapshotBeforeWrite checks that SetState took a Snapshot before its
// Write, and that restoring that snapshot gives back the pre-Write value.
func checkSnapshotBeforeWrite(
	ctx context.Context,
	r reporter,
	provider *recordingStateProvider,
	calls []string,
	snaps []contract.Snapshot,
	initial contract.State,
) {
	r.Helper()

	snapAt, writeAt := indexOf(calls, callSnapshot), indexOf(calls, callWrite)
	if writeAt < 0 {
		r.Errorf("SetState on declared key never called StateProvider.Write (calls %v)", calls)
		return
	}
	if snapAt < 0 || snapAt > writeAt {
		r.Errorf("SetState must call StateProvider.Snapshot before Write (calls %v)", calls)
		return
	}
	backup := snaps[0]
	if len(backup.Data) == 0 {
		r.Errorf("SetState backup snapshot is empty")
		return
	}
	if err := provider.Restore(ctx, backup); err != nil {
		r.Errorf("StateProvider.Restore of the SetState backup failed: %v", err)
		return
	}
	got, err := provider.Read(ctx, declaredState)
	if err != nil || got != initial {
		r.Errorf("state after Restore of the SetState backup = %+v (err %v), want the pre-Write value %+v", got, err, initial)
	}
	provider.reset()
}

// checkSetStateWriteFailure makes Write change the key and then fail. The Tx
// must return an error, leave the provider at its pre-Write value and add no
// receipt row.
func checkSetStateWriteFailure(
	ctx context.Context,
	r reporter,
	tx contract.Tx,
	inspector TxInspector,
	provider *recordingStateProvider,
	initial contract.State,
) {
	r.Helper()

	provider.setFailWrite(true)
	defer provider.setFailWrite(false)

	beforeCount := len(inspector.Changes())
	err := tx.SetState(ctx, provider, declaredState, contract.State{Value: "half-written"})
	if !errors.Is(err, errInjectedWrite) {
		r.Errorf("SetState after a failed Write returned %v, want an error wrapping the Write error", err)
	}
	got, readErr := provider.Read(ctx, declaredState)
	if readErr != nil || got != initial {
		r.Errorf("state after a failed SetState Write = %+v (err %v), want the restored pre-Write value %+v", got, readErr, initial)
	}
	if len(inspector.Changes()) != beforeCount {
		r.Errorf("SetState recorded a receipt row for a failed Write")
	}
	provider.reset()
}

func checkEmitOrder(r reporter, tx contract.DeclarativeTx, inspector TxInspector) {
	r.Helper()

	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for i := uint64(1); i <= emitOrderCount; i++ {
		tx.Emit(contract.Event{
			Seq:   i,
			Time:  base.Add(time.Duration(i) * time.Second),
			RunID: contract.RunID("run-order-check"),
			Unit:  contract.UnitShell,
			Step:  contract.StepApply,
			Kind:  contract.EventOutput,
			Text:  "ordered event",
		})
	}
	events := inspector.Events()
	if len(events) < emitOrderCount {
		r.Errorf("Emit recorded %d events, want at least %d", len(events), emitOrderCount)
		return
	}
	tail := events[len(events)-emitOrderCount:]
	for i := range emitOrderCount {
		wantSeq := uint64(i + 1)
		if tail[i].Seq != wantSeq {
			r.Errorf("event[%d].Seq = %d, want %d (Emit must preserve order)", i, tail[i].Seq, wantSeq)
			return
		}
	}
}

func checkSink(r reporter, s contract.Sink, timeout time.Duration) {
	r.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
		for i := uint64(1); i <= sinkBurstCount; i++ {
			s.Deliver(contract.Event{
				Seq:   i,
				Time:  base,
				RunID: contract.RunID("run-sink-check"),
				Unit:  contract.UnitShell,
				Step:  contract.StepApply,
				Kind:  contract.EventOutput,
				Text:  "burst event",
			})
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		r.Errorf("Sink.Deliver blocked for more than %v", timeout)
	}
}

func checkStateProvider(
	r reporter,
	p contract.StateProvider,
	key contract.StateKey,
	mutate func(ctx context.Context) error,
) {
	r.Helper()

	ctx := context.Background()
	if p.Name() == "" {
		r.Errorf("StateProvider.Name() must not be empty")
		return
	}

	before, err := p.Read(ctx, key)
	if err != nil {
		r.Errorf("StateProvider.Read(%q) before snapshot failed: %v", key, err)
		return
	}
	snap, err := p.Snapshot(ctx)
	if err != nil {
		r.Errorf("StateProvider.Snapshot failed: %v", err)
		return
	}
	if snap.Provider != p.Name() {
		r.Errorf("Snapshot.Provider = %q, want %q", snap.Provider, p.Name())
	}

	if err := mutate(ctx); err != nil {
		r.Errorf("mutate state failed: %v", err)
		return
	}
	if err := p.Restore(ctx, snap); err != nil {
		r.Errorf("StateProvider.Restore failed: %v", err)
		return
	}
	after, err := p.Read(ctx, key)
	if err != nil {
		r.Errorf("StateProvider.Read(%q) after restore failed: %v", key, err)
		return
	}
	if after != before {
		r.Errorf("state after Restore = %+v, want %+v", after, before)
	}

	// Write: read back the written value, and undo it through Restore of a
	// snapshot taken before the write.
	beforeWrite, err := p.Snapshot(ctx)
	if err != nil {
		r.Errorf("StateProvider.Snapshot before Write failed: %v", err)
		return
	}
	want := contract.State{Value: "contracttest-written-value"}
	if err := p.Write(ctx, key, want); err != nil {
		r.Errorf("StateProvider.Write(%q) failed: %v", key, err)
		return
	}
	if got, err := p.Read(ctx, key); err != nil || got != want {
		r.Errorf("state after Write = %+v (err %v), want %+v", got, err, want)
	}
	if err := p.Restore(ctx, beforeWrite); err != nil {
		r.Errorf("StateProvider.Restore after Write failed: %v", err)
		return
	}
	if got, err := p.Read(ctx, key); err != nil || got != before {
		r.Errorf("state after Restore of the pre-Write snapshot = %+v (err %v), want %+v", got, err, before)
	}

	wrong := contract.Snapshot{
		Provider: contract.StateProviderName("other-provider"),
		Data:     snap.Data,
	}
	if err := p.Restore(ctx, wrong); err == nil {
		r.Errorf("StateProvider.Restore accepted snapshot from wrong provider %q", wrong.Provider)
	}
}

func assertUndeclaredError(r reporter, op string, wantKind contract.TargetKind, wantTarget string, err error) {
	r.Helper()

	if err == nil {
		r.Errorf("%s on undeclared %s %q succeeded, want UndeclaredTargetError", op, wantKind, wantTarget)
		return
	}
	if !errors.Is(err, contract.ErrUndeclaredTarget) {
		r.Errorf("%s error %v does not wrap ErrUndeclaredTarget", op, err)
	}
	var targetErr *contract.UndeclaredTargetError
	if !errors.As(err, &targetErr) {
		r.Errorf("%s error %T is not *contract.UndeclaredTargetError", op, err)
		return
	}
	if targetErr.Kind != wantKind {
		r.Errorf("%s error Kind = %q, want %q", op, targetErr.Kind, wantKind)
	}
	if targetErr.Target != wantTarget {
		r.Errorf("%s error Target = %q, want %q", op, targetErr.Target, wantTarget)
	}
}

func assertRefusedUnchanged(
	r reporter,
	op string,
	inspector TxInspector,
	wantKind contract.TargetKind,
	wantTarget string,
	call func() error,
) {
	r.Helper()

	before := inspector.BackingState()
	err := call()
	assertUndeclaredError(r, op, wantKind, wantTarget, err)
	after := inspector.BackingState()
	if !bytes.Equal(before, after) {
		r.Errorf("%s mutated backing state despite returning refusal error", op)
	}
}

func assertRecordedChange(
	r reporter,
	op string,
	changes []contract.AppliedChange,
	wantTarget string,
	requireBackup bool,
) {
	r.Helper()

	for _, ch := range changes {
		if ch.Target == wantTarget {
			if requireBackup && ch.Backup == "" {
				r.Errorf("%s on declared target %q recorded no backup reference", op, wantTarget)
			}
			return
		}
	}
	r.Errorf("%s on declared target %q was not recorded for the receipt (changes=%v)", op, wantTarget, changes)
}
