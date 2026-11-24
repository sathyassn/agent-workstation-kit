package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/linux"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// withSystemd runs the engine on the real Linux adapter over the
// real-shaped systemd fake, instead of the kill harness's simple fake.
func withSystemd(w *world, sd *platformtest.Systemd) func(*engine.Options) {
	return func(o *engine.Options) {
		o.Containment = &linux.Containment{Runner: sd, Inspector: w.table, UserManager: func(context.Context) bool { return true }}
	}
}

// lateWrite returns work that overwrites path A, as a process that outlived
// its run would.
func lateWrite(w *world) func() {
	return func() { _ = os.WriteFile(filepath.Join(w.root, pathA), []byte("late\n"), 0o600) }
}

// TestKillBetweenLaunchAndScopeRegistration is finding 1. The engine dies
// after systemd-run passed its gate but before the scope registered: the
// scope reads "not-found", yet the supervisor lives and could still
// register and run the tool. The next start must treat that as not empty,
// kill the supervisor, and only then restore and reapply; the supervisor
// then never registers, so nothing writes after the recovery.
func TestKillBetweenLaunchAndScopeRegistration(t *testing.T) {
	w := newWorld(t)
	sd := platformtest.NewSystemd(w.table)
	u := &testUnit{root: w.root}
	sd.HoldRegistration(func(string) { panic(crashSentinel{}) }, lateWrite(w))
	dying, self := w.engine(t, withSystemd(w, sd))
	if !runCrashing(func() { _ = dying.Apply(context.Background(), u, testPlan()) }) {
		t.Fatal("apply did not crash between launch and registration")
	}
	w.table.Kill(self)
	rec := w.record(t)
	if rec.Command == nil || !rec.Command.Supervisor.Valid() || rec.Command.Finished {
		t.Fatalf("command record = %+v, want a durable supervisor and not finished", rec.Command)
	}
	if sd.Loaded(rec.Command.Name) {
		t.Fatal("scope registered before the crash; the test needs it held")
	}

	e, _ := w.engine(t, withSystemd(w, sd))
	res, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	if err != nil || res.Action != engine.ResumeReapplied {
		t.Fatalf("resume = %+v, %v; want reapplied", res, err)
	}
	if alive, _ := w.table.Alive(context.Background(), rec.Command.Supervisor); alive {
		t.Fatal("recovery left the supervisor alive")
	}
	if ran := sd.CompleteRegistrations(); ran != 0 {
		t.Fatalf("%d held launches registered after recovery", ran)
	}
	assertFinished(t, w, "after recovery")
}

// TestSuccessfulCommandLeavesNoDescendants is finding 4. A tool exits 0 but
// leaves a descendant that would write later. The engine proves the scope
// empty before the unit moves on, so the descendant is gone before verify
// and never writes.
func TestSuccessfulCommandLeavesNoDescendants(t *testing.T) {
	w := newWorld(t)
	sd := platformtest.NewSystemd(w.table)
	sd.SetTool(func(_ context.Context, s *platformtest.Scope) int {
		s.Spawn(lateWrite(w), false)
		return 0
	})
	e, _ := w.engine(t, withSystemd(w, sd))
	if err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan()); err != nil {
		t.Fatal(err)
	}
	if ran := sd.RunDelayed(); ran != 0 {
		t.Fatalf("%d descendants outlived a successful command", ran)
	}
	assertFinished(t, w, "after a command that left a descendant")
	rec := w.record(t)
	if sd.Loaded(rec.Command.Name) || !rec.Command.Finished {
		t.Fatalf("command %+v not finished or scope still loaded", rec.Command)
	}
}

// TestUnemptiableContainmentBlocksRollback is finding 5, through Apply: a
// descendant ignores every signal, so the containment cannot be proven
// empty. The apply exits 4 and leaves its record unfinished; it does not
// restore over a process that could undo the restore. The next start also
// exits 4 while the descendant lives.
func TestUnemptiableContainmentBlocksRollback(t *testing.T) {
	w := newWorld(t)
	sd := platformtest.NewSystemd(w.table)
	sd.SetTool(func(_ context.Context, s *platformtest.Scope) int {
		s.Spawn(nil, true)
		return 0
	})
	e, self := w.engine(t, withSystemd(w, sd))
	err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan())
	if !errors.Is(err, engine.ErrEmptinessUnknown) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("apply err = %v (exit %d), want ErrEmptinessUnknown and exit 4", err, engine.ExitCode(err))
	}
	rec := w.record(t)
	if rec.Status != engine.StatusApplying || rec.Command == nil || rec.Command.Finished {
		t.Fatalf("record = %+v, want applying with an unfinished command", rec)
	}
	if got := w.state(t)[pathA]; got == nil || *got != "new-a\n" {
		t.Fatalf("path A = %v, want the change left in place, not restored", got)
	}
	if r := w.receipt(t, rec.RunID); r != nil {
		t.Fatalf("receipt written: %+v", r)
	}

	w.table.Kill(self) // the process exits 4
	r, _ := w.engine(t, withSystemd(w, sd))
	_, err = r.Resume(context.Background(), resolverFor(&testUnit{root: w.root}), engine.ResumeOptions{})
	if !errors.Is(err, engine.ErrEmptinessUnknown) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("resume err = %v, want ErrEmptinessUnknown and exit 4", err)
	}
	if got := w.record(t); got.Status != engine.StatusApplying || got.Claim != nil {
		t.Fatalf("record after refused resume = %+v", got)
	}
}

// hangingContainment never answers Empty until its context ends.
type hangingContainment struct{ *platformtest.Containment }

func (h hangingContainment) Empty(ctx context.Context, _ string, _ platform.EmptyOptions) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestRecoveryEmptyHasDeadline is finding 7: a supervisor that never
// answers cannot hold recovery forever; the start exits 4 within its bound.
func TestRecoveryEmptyHasDeadline(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root}
	w.abandon(t, engine.PointLaunch, u)
	e, _ := w.engine(t, func(o *engine.Options) {
		o.Containment = hangingContainment{w.cont}
		o.CallTimeout = 10 * time.Millisecond
	})
	done := make(chan error, 1)
	go func() {
		_, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, engine.ErrEmptinessUnknown) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("err = %v, want ErrEmptinessUnknown and exit 4", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery waited on a hung supervisor without a deadline")
	}
}

// TestLatchedEmptinessFailureSurvivesUnitError is round 3, item 2: Run
// latches ErrEmptinessUnknown, then the unit returns an error of its own.
// The latched failure must still be seen: the apply exits 4 and restores
// nothing over the process that could not be stopped.
func TestLatchedEmptinessFailureSurvivesUnitError(t *testing.T) {
	w := newWorld(t)
	sd := platformtest.NewSystemd(w.table)
	sd.SetTool(func(_ context.Context, s *platformtest.Scope) int {
		s.Spawn(nil, true)
		return 0
	})
	e, _ := w.engine(t, withSystemd(w, sd))
	unitErr := errors.New("unit gave up")
	u := &refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
		owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
		if err := tx.WriteFile(ctx, pathA, []byte("new-a\n"), 0o644, owner); err != nil {
			return err
		}
		_, _ = tx.Run(ctx, contract.Command{Argv: []string{"/usr/bin/true"}})
		return unitErr // the unit's own error, ignoring Run's
	}}
	err := e.Apply(context.Background(), u, testPlan())
	if !errors.Is(err, engine.ErrEmptinessUnknown) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("err = %v (exit %d), want ErrEmptinessUnknown and exit 4", err, engine.ExitCode(err))
	}
	if got := w.state(t)[pathA]; got == nil || *got != "new-a\n" {
		t.Fatalf("path A = %v, want the change left in place, not restored", got)
	}
	if rec := w.record(t); rec.Status != engine.StatusApplying {
		t.Fatalf("record = %+v, want applying", rec)
	}
}

// TestLatchedEmptinessSurvivesEventLogFailure is round 3, item 2, the case
// Codex found: Run latches ErrEmptinessUnknown and the event log fails too.
// Whether the unit ignores Run's error or returns its own, both failures
// must reach the engine: exit 4, nothing restored, the intent unfinished.
func TestLatchedEmptinessSurvivesEventLogFailure(t *testing.T) {
	for _, ownErr := range []bool{false, true} {
		name := "unit ignores the command error"
		if ownErr {
			name = "unit returns its own error"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			sd := platformtest.NewSystemd(w.table)
			sink := &failingSink{}
			sd.SetTool(func(_ context.Context, s *platformtest.Scope) int {
				s.Spawn(nil, true) // survives every signal
				sink.mu.Lock()
				sink.err = errors.New("disk full")
				sink.mu.Unlock()
				return 0
			})
			e, _ := w.engine(t, withSystemd(w, sd), func(o *engine.Options) { o.Sink = sink })
			u := &refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
				owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
				if err := tx.WriteFile(ctx, pathA, []byte("new-a\n"), 0o644, owner); err != nil {
					return err
				}
				_, _ = tx.Run(ctx, contract.Command{Argv: []string{"/usr/bin/true"}})
				if ownErr {
					return errors.New("unit gave up")
				}
				return nil
			}}
			err := e.Apply(context.Background(), u, testPlan())
			if !errors.Is(err, engine.ErrEmptinessUnknown) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
				t.Fatalf("err = %v (exit %d), want ErrEmptinessUnknown and exit 4", err, engine.ExitCode(err))
			}
			if got := w.state(t)[pathA]; got == nil || *got != "new-a\n" {
				t.Fatalf("path A = %v, want the change left in place, not restored", got)
			}
			if rec := w.record(t); rec.Status != engine.StatusApplying || rec.Command == nil || rec.Command.Finished {
				t.Fatalf("record = %+v, want applying with an unfinished command", rec)
			}
		})
	}
}
