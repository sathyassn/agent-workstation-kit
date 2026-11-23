package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
)

// overlapUnit counts how many applies are inside their step at once.
type overlapUnit struct {
	testUnit
	inside, maxInside *atomic.Int32
	reset             func()
}

func (u *overlapUnit) Apply(ctx context.Context, tx contract.Tx, p contract.Plan) error {
	n := u.inside.Add(1)
	defer u.inside.Add(-1)
	for {
		m := u.maxInside.Load()
		if n <= m || u.maxInside.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	return u.testUnit.Apply(ctx, tx, p)
}

// Verify passes, then puts the original state back, so the next apply in
// the queue starts from it, as a test fixture rather than a revert.
func (u *overlapUnit) Verify(ctx context.Context, p contract.Plan) (contract.Result, error) {
	res, err := u.testUnit.Verify(ctx, p)
	u.reset()
	return res, err
}

// TestConcurrentAppliesAreSerialized is AC-4: concurrent applies from
// separate engines (separate processes as far as locks and identities go)
// run one at a time under the host lock, and every one completes.
func TestConcurrentAppliesAreSerialized(t *testing.T) {
	w := newWorld(t)
	var inside, maxInside atomic.Int32
	reset := func() { restoreOriginal(t, w) }
	const n = 6
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		e, _ := w.engine(t)
		u := &overlapUnit{testUnit: testUnit{root: w.root}, inside: &inside, maxInside: &maxInside, reset: reset}
		wg.Go(func() { errs <- e.Apply(context.Background(), u, testPlan()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := maxInside.Load(); got != 1 {
		t.Fatalf("%d applies ran at once, want 1", got)
	}
}

func restoreOriginal(t *testing.T, w *world) {
	t.Helper()
	for p, c := range originalState() {
		path := filepath.Join(w.root, p)
		var err error
		if c == nil {
			err = os.Remove(path)
		} else {
			err = os.WriteFile(path, []byte(*c), 0o600)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	}
}

// TestResumeDuringActiveApplyExits4 is AC-4 with AC-2: while an apply runs
// and holds the host lock, a resume reads the record under the journal
// lock, sees the live executor and exits 4 without touching anything; the
// apply then completes.
func TestResumeDuringActiveApplyExits4(t *testing.T) {
	w := newWorld(t)
	inStep := make(chan struct{})
	release := make(chan struct{})
	u := &testUnit{root: w.root, afterRun: func(context.Context) error {
		close(inStep)
		<-release
		return nil
	}}
	a, _ := w.engine(t)
	done := make(chan error, 1)
	go func() { done <- a.Apply(context.Background(), u, testPlan()) }()
	<-inStep

	r, _ := w.engine(t)
	emptiesBefore := len(w.cont.EmptyCalls())
	_, err := r.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	emptiesByResume := len(w.cont.EmptyCalls()) - emptiesBefore
	if !errors.Is(err, engine.ErrActiveExecutor) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("resume err = %v, want ErrActiveExecutor and exit 4", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertFinished(t, w, "apply after a refused resume")
	if emptiesByResume != 0 {
		t.Fatal("resume emptied an active executor's containment")
	}
}

// TestTwoResumersOnlyOneProceeds is AC-4 and the spec's race test: two
// starters recover the same abandoned run. The first is paused between
// identity inspection and cleanup, between cleanup and reapply, or holding
// the execution locks; the second runs meanwhile. Only the first proceeds;
// the second exits 4 on the live claim and changes nothing.
func TestTwoResumersOnlyOneProceeds(t *testing.T) {
	for _, pause := range []engine.Point{engine.PointClaimed, engine.PointEmptied, engine.PointRecoverLocked} {
		t.Run(string(pause), func(t *testing.T) {
			w := newWorld(t)
			u := &testUnit{root: w.root}
			w.abandon(t, engine.PointLaunch, u)
			name := w.record(t).Command.Name
			w.cont.SetBusy(name, true)

			paused := make(chan struct{})
			resume := make(chan struct{})
			first, _ := w.engine(t, func(o *engine.Options) {
				o.Fault = func(p engine.Point) {
					if p == pause {
						close(paused)
						<-resume
					}
				}
			})
			done := make(chan error, 1)
			go func() {
				res, err := first.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
				if err == nil && res.Action != engine.ResumeReapplied {
					err = errors.New("first resumer did not reapply: " + string(res.Action))
				}
				done <- err
			}()
			<-paused
			emptiesBefore := len(w.cont.EmptyCalls())

			second, _ := w.engine(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := second.Resume(ctx, resolverFor(u), engine.ResumeOptions{})
			if !errors.Is(err, engine.ErrRecoveryClaimed) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
				t.Fatalf("second resume err = %v, want ErrRecoveryClaimed and exit 4", err)
			}
			if len(w.cont.EmptyCalls()) != emptiesBefore {
				t.Fatal("second resumer emptied the containment")
			}
			close(resume)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			assertFinished(t, w, "after the first resumer")
		})
	}
}

// TestApplyDuringRecoveryIsSerialized is AC-4: an apply that starts while
// a resume recovers either exits 4 on the unfinished record (recovery has
// not yet taken the host lock) or waits for the host lock and runs after
// the recovery finished (recovery holds it).
func TestApplyDuringRecoveryIsSerialized(t *testing.T) {
	tests := []struct {
		pause   engine.Point
		wantErr error
	}{
		{engine.PointClaimed, engine.ErrRecoveryPending},
		{engine.PointRecoverLocked, nil},
	}
	for _, tc := range tests {
		t.Run(string(tc.pause), func(t *testing.T) {
			w := newWorld(t)
			u := &testUnit{root: w.root}
			w.abandon(t, engine.PointMutation, u)
			paused := make(chan struct{})
			resume := make(chan struct{})
			r, _ := w.engine(t, func(o *engine.Options) {
				o.Fault = func(p engine.Point) {
					if p == tc.pause {
						close(paused)
						<-resume
					}
				}
			})
			recovered := make(chan error, 1)
			go func() {
				_, err := r.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
				recovered <- err
			}()
			<-paused

			a, _ := w.engine(t)
			applied := make(chan error, 1)
			go func() {
				// After a recovery the state is applied; put it back so the
				// queued apply has the original state to start from.
				err := a.Apply(context.Background(), &resetUnit{testUnit: testUnit{root: w.root}, w: w, t: t}, testPlan())
				applied <- err
			}()
			if tc.wantErr != nil {
				if err := <-applied; !errors.Is(err, tc.wantErr) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
					t.Fatalf("apply err = %v, want %v and exit 4", err, tc.wantErr)
				}
				close(resume)
				if err := <-recovered; err != nil {
					t.Fatal(err)
				}
				return
			}
			select {
			case err := <-applied:
				t.Fatalf("apply ran while recovery held the host lock: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(resume)
			if err := <-recovered; err != nil {
				t.Fatal(err)
			}
			if err := <-applied; err != nil {
				t.Fatalf("queued apply: %v", err)
			}
			assertFinished(t, w, "apply after recovery")
		})
	}
}

// resetUnit puts the original state back before applying, so an apply
// queued behind a recovery starts from it.
type resetUnit struct {
	testUnit
	w *world
	t *testing.T
}

func (u *resetUnit) Apply(ctx context.Context, tx contract.Tx, p contract.Plan) error {
	restoreOriginal(u.t, u.w)
	return u.testUnit.Apply(ctx, tx, p)
}
