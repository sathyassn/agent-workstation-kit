package contracttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

const (
	declaredPath     = "/etc/agent-workstation-kit/declared.conf"
	undeclaredPath   = "/etc/agent-workstation-kit/undeclared.conf"
	declaredState    = contract.StateKey("hostname.pretty")
	undeclaredState  = contract.StateKey("hostname.undeclared")
	declaredEnvKey   = "WSKIT_DECLARED_ENV"
	undeclaredEnvKey = "WSKIT_UNDECLARED_ENV"
	sampleTemplateID = contract.TemplateID("sample-tpl")
	sampleBlockID    = contract.BlockID("wskit-block")
	defaultSinkWait  = 250 * time.Millisecond
	sinkBurstCount   = 512
	emitOrderCount   = 8
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
		FormFactor: contract.FormFactor("fixed"),
		Topology:   contract.Topology("local-only"),
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
		Unit:           contract.UnitShell,
		Scope:          contract.ScopeUser,
		DependsOn:      []contract.UnitID{},
		Capabilities:   []contract.Capability{},
		OwnedPaths:     []string{declaredPath},
		OwnedStateKeys: []contract.StateKey{declaredState},
		Irreversible:   []string{},
		DeclaredEnv:    []string{declaredEnvKey},
	}
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

	assertUndeclaredError(r, "WriteFile", contract.TargetPath, undeclaredPath,
		tx.WriteFile(ctx, undeclaredPath, []byte("x"), defaultFilePerm, contract.Owner{}))
	assertUndeclaredError(r, "RenderTemplate", contract.TargetPath, undeclaredPath,
		tx.RenderTemplate(ctx, undeclaredPath, sampleTemplateID, contract.TemplateVars{}, defaultFilePerm, contract.Owner{}))
	assertUndeclaredError(r, "EnsureBlock", contract.TargetPath, undeclaredPath,
		tx.EnsureBlock(ctx, undeclaredPath, contract.ManagedBlock{ID: sampleBlockID, Begin: "# begin", End: "# end", Body: "x"}))
	assertUndeclaredError(r, "RemoveBlock", contract.TargetPath, undeclaredPath,
		tx.RemoveBlock(ctx, undeclaredPath, sampleBlockID))
	assertUndeclaredError(r, "Remove", contract.TargetPath, undeclaredPath,
		tx.Remove(ctx, undeclaredPath))

	if changes := inspector.Changes(); len(changes) != 0 {
		r.Errorf("refused undeclared mutations recorded %d changes, want 0", len(changes))
	}

	if err := tx.WriteFile(ctx, declaredPath, []byte("hello\n"), defaultFilePerm, contract.Owner{}); err != nil {
		r.Errorf("WriteFile on declared path failed: %v", err)
	}
	changes := inspector.Changes()
	foundDeclared := false
	for _, ch := range changes {
		if ch.Target == declaredPath {
			foundDeclared = true
			break
		}
	}
	if !foundDeclared {
		r.Errorf("WriteFile on declared path %q was not recorded for the receipt (changes=%v)", declaredPath, changes)
	}

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

	provider := NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
		declaredState: {Value: "lab-ws-01"},
	})

	assertUndeclaredError(r, "SetState", contract.TargetStateKey, string(undeclaredState),
		tx.SetState(ctx, provider, undeclaredState, contract.State{Value: "bad"}))

	_, runErr := tx.Run(ctx, contract.Command{
		Argv:    []string{"/usr/bin/true"},
		EnvKeys: []string{undeclaredEnvKey},
	})
	assertUndeclaredError(r, "Run", contract.TargetEnvKey, undeclaredEnvKey, runErr)

	beforeCount := len(inspector.Changes())
	if err := tx.SetState(ctx, provider, declaredState, contract.State{Value: "acme-ws-001"}); err != nil {
		r.Errorf("SetState on declared key failed: %v", err)
	}
	if len(inspector.Changes()) <= beforeCount {
		r.Errorf("SetState on declared key %q was not recorded for the receipt", declaredState)
	}
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
