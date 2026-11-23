package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// TestCancelAndGraceNeverEndPackageTransaction is AC-3, with the ordinary
// command as the contrast. A cancel request arrives while the tool runs and
// the grace period ends while it still runs. A package-manager transaction's
// context is never cancelled and its containment is never emptied or
// signalled; it runs to its end. An ordinary command is cancelled at the
// end of the grace period and its containment emptied. Either way the run
// stops with exit 130 and leaves its record for the next start.
func TestCancelAndGraceNeverEndPackageTransaction(t *testing.T) {
	for _, pkg := range []bool{true, false} {
		name := "ordinary command"
		if pkg {
			name = "package transaction"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			const grace = 20 * time.Millisecond
			e, _ := w.engine(t, func(o *engine.Options) { o.Grace = grace })
			started := make(chan struct{})
			release := make(chan struct{})
			toolCtxEnded := make(chan bool, 1)
			w.cont.SetRun(func(ctx context.Context, spec platform.LaunchSpec) (contract.CommandResult, error) {
				close(started)
				select {
				case <-ctx.Done():
					toolCtxEnded <- true
					return contract.CommandResult{ExitCode: 143}, ctx.Err()
				case <-release:
					toolCtxEnded <- false
					return contract.CommandResult{}, nil
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- e.Apply(ctx, &testUnit{root: w.root, pkg: pkg}, testPlan()) }()
			<-started
			cancel()
			time.Sleep(10 * grace)
			close(release)
			err := <-done

			if ended := <-toolCtxEnded; ended == pkg {
				t.Fatalf("tool context ended = %v, want %v", ended, !pkg)
			}
			name := w.cont.Launched()[0].Name
			if pkg && (len(w.cont.EmptyCalls()) != 0 || w.cont.Killed(name)) {
				t.Fatalf("package transaction emptied or killed: %+v", w.cont.EmptyCalls())
			}
			if !pkg && len(w.cont.EmptyCalls()) == 0 {
				t.Fatal("ordinary command's containment not emptied after the grace period")
			}
			if !errors.Is(err, engine.ErrCancelled) || engine.ExitCode(err) != contract.ExitCancelled {
				t.Fatalf("err = %v (exit %d), want ErrCancelled and exit 130", err, engine.ExitCode(err))
			}
			if r := w.record(t); r.Status != engine.StatusApplying {
				t.Fatalf("record = %+v, want applying for the next start", r)
			}
		})
	}
}

// TestCancelWithinGraceFinishesStep: a step that ends within the grace
// period completes with its receipt.
func TestCancelWithinGraceFinishesStep(t *testing.T) {
	w := newWorld(t)
	e, _ := w.engine(t, func(o *engine.Options) { o.Grace = time.Minute })
	ctx, cancel := context.WithCancel(context.Background())
	u := &testUnit{root: w.root, afterRun: func(context.Context) error { cancel(); return nil }}
	if err := e.Apply(ctx, u, testPlan()); err != nil {
		t.Fatal(err)
	}
	assertFinished(t, w, "after cancel within grace")
}

// TestPackageTransactionIgnoresTimeout: a package-manager transaction's
// command timeout does not end it.
func TestPackageTransactionIgnoresTimeout(t *testing.T) {
	w := newWorld(t)
	e, _ := w.engine(t)
	w.cont.SetRun(func(ctx context.Context, _ platform.LaunchSpec) (contract.CommandResult, error) {
		select {
		case <-ctx.Done():
			return contract.CommandResult{}, ctx.Err()
		case <-time.After(30 * time.Millisecond):
			return contract.CommandResult{}, nil
		}
	})
	u := &timeoutUnit{testUnit: testUnit{root: w.root}}
	if err := e.Apply(context.Background(), u, testPlan()); err != nil {
		t.Fatal(err)
	}
}

type timeoutUnit struct{ testUnit }

func (u *timeoutUnit) Apply(ctx context.Context, tx contract.Tx, p contract.Plan) error {
	if _, err := tx.Run(ctx, contract.Command{Argv: []string{"apt-get"}, Timeout: time.Millisecond, PackageTransaction: true}); err != nil {
		return err
	}
	u.pkg = true
	return u.testUnit.Apply(ctx, tx, p)
}

// TestRestartWaitsForPackageTransaction is AC-3 on restart: a package
// transaction left running by a dead executor is waited for, never killed;
// if it outlasts the bound, the start exits 4.
func TestRestartWaitsForPackageTransaction(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root, pkg: true}
	w.abandon(t, engine.PointLaunch, u)
	rec := w.record(t)
	if rec.Command == nil || !rec.Command.PackageTransaction {
		t.Fatalf("record command = %+v, want a package transaction", rec.Command)
	}
	w.cont.SetBusy(rec.Command.Name, true)
	e, _ := w.engine(t)
	_, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	if !errors.Is(err, platform.ErrContainmentNotEmpty) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("err = %v, want exit 4", err)
	}
	calls := w.cont.EmptyCalls()
	if len(calls) != 1 || !calls[0].Opts.PackageTransaction {
		t.Fatalf("empty calls = %+v, want one package-transaction wait", calls)
	}
	if w.cont.Killed(rec.Command.Name) {
		t.Fatal("package transaction killed")
	}

	// Once the transaction ends, the next start recovers.
	w.cont.SetBusy(rec.Command.Name, false)
	res, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	if err != nil || res.Action != engine.ResumeReapplied {
		t.Fatalf("resume = %+v, %v", res, err)
	}
}
