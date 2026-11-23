package engine_test

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// TestResumeTellsActiveExecutorFromAbandonedRun is AC-2: the record's
// executor identity decides. Only a process with the same boot ID, PID and
// start time is the active executor; a reused PID or an earlier boot is an
// abandoned run. An executor that cannot be inspected counts as active.
func TestResumeTellsActiveExecutorFromAbandonedRun(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(w *world, exec platform.ProcessIdentity)
		wantErr error
	}{
		{"same identity alive", func(w *world, id platform.ProcessIdentity) { w.table.Revive(id) }, engine.ErrActiveExecutor},
		{"executor dead", func(*world, platform.ProcessIdentity) {}, nil},
		{"pid reused with another start time", func(w *world, id platform.ProcessIdentity) {
			id.StartTime += "-reused"
			w.table.Revive(id)
		}, nil},
		{"same pid on an earlier boot", func(w *world, id platform.ProcessIdentity) {
			id.BootID = "boot-earlier"
			w.table.Revive(id)
		}, nil},
		{"inspection fails", func(w *world, _ platform.ProcessIdentity) { w.table.FailInspection(platformtest.ErrInspect) }, engine.ErrActiveExecutor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			u := &testUnit{root: w.root}
			exec := w.abandon(t, engine.PointMutation, u)
			tc.setup(w, exec)
			e, _ := w.engine(t)
			res, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
					t.Fatalf("err = %v (exit %d), want %v and exit 4", err, engine.ExitCode(err), tc.wantErr)
				}
				if len(w.cont.EmptyCalls()) != 0 {
					t.Fatal("containment emptied while the executor may be running")
				}
				if r := w.record(t); r.Status != engine.StatusApplying || r.Claim != nil {
					t.Fatalf("record changed: %+v", r)
				}
				return
			}
			if err != nil || res.Action != engine.ResumeReapplied {
				t.Fatalf("resume = %+v, %v; want reapplied", res, err)
			}
			w.assertState(t, appliedState(), "after recovery")
		})
	}
}

// TestResumeEmptiesContainmentWithinBound is AC-2: a run killed while its
// tool ran leaves processes in the containment; the next start empties it
// by its recorded name, within the configured bound, before restoring.
func TestResumeEmptiesContainmentWithinBound(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root}
	w.abandon(t, engine.PointLaunch, u)
	rec := w.record(t)
	if rec.Command == nil {
		t.Fatal("containment name not in the record before launch")
	}
	w.cont.SetBusy(rec.Command.Name, true)
	e, _ := w.engine(t)
	res, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	if err != nil || res.Action != engine.ResumeReapplied {
		t.Fatalf("resume = %+v, %v", res, err)
	}
	calls := w.cont.EmptyCalls()
	if len(calls) == 0 || calls[0].Name != rec.Command.Name {
		t.Fatalf("empty calls = %+v, want %q first", calls, rec.Command.Name)
	}
	if o := calls[0].Opts; o.Grace <= 0 || o.KillWait <= 0 || o.PackageTransaction {
		t.Fatalf("empty options = %+v, want a bound and no package transaction", o)
	}
	if !w.cont.Killed(rec.Command.Name) || w.cont.Busy(rec.Command.Name) {
		t.Fatal("containment not emptied")
	}
	w.assertState(t, appliedState(), "after recovery")
}

// TestResumeExits4WithoutLockWhenContainmentStaysBusy is AC-2: when the
// containment cannot be emptied within its bound, the start exits 4, never
// takes the host lock, withdraws its claim, and leaves the record and the
// machine as they were.
func TestResumeExits4WithoutLockWhenContainmentStaysBusy(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root}
	w.abandon(t, engine.PointLaunch, u)
	name := w.record(t).Command.Name
	w.cont.SetBusy(name, true)
	w.cont.SetUnkillable(name)
	hostFree := false
	w.cont.OnEmpty(func(string, platform.EmptyOptions) { hostFree = lockFree(t, w.hostLock) })
	before := w.state(t)

	e, _ := w.engine(t)
	_, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	if !errors.Is(err, platform.ErrContainmentNotEmpty) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("err = %v (exit %d), want ErrContainmentNotEmpty and exit 4", err, engine.ExitCode(err))
	}
	if !hostFree {
		t.Fatal("host lock was held while emptying the containment")
	}
	if !lockFree(t, w.hostLock) {
		t.Fatal("host lock held after exit 4")
	}
	if r := w.record(t); r.Status != engine.StatusApplying || r.Claim != nil {
		t.Fatalf("record = %+v, want applying with no claim", r)
	}
	w.assertState(t, before, "after exit 4")
}

// lockFree reports whether nobody holds the flock at path.
func lockFree(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // lock file in a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

// TestResumeRestoreOnlyStops restores the interrupted run and does not
// reapply it.
func TestResumeRestoreOnlyStops(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root}
	w.abandon(t, engine.PointMutation, u)
	e, _ := w.engine(t)
	res, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{RestoreOnly: true})
	if err != nil || res.Action != engine.ResumeRestored {
		t.Fatalf("resume = %+v, %v", res, err)
	}
	w.assertState(t, originalState(), "after restore-only")
	rec := w.record(t)
	if rec.Status != engine.StatusIdle {
		t.Fatalf("record = %+v", rec)
	}
	if r := w.receipt(t, rec.RunID); r == nil || r.Outcome != contract.OutcomeFailed {
		t.Fatalf("receipt = %+v, want failed", r)
	}
}

// TestApplyRefusesUnfinishedRecord: a new apply never runs over an
// interrupted one; it exits 4 until resume handles it.
func TestApplyRefusesUnfinishedRecord(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root}
	w.abandon(t, engine.PointIntent, u)
	e, _ := w.engine(t)
	err := e.Apply(context.Background(), u, testPlan())
	if !errors.Is(err, engine.ErrRecoveryPending) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("err = %v, want ErrRecoveryPending and exit 4", err)
	}
}

// TestResumeRefusesChangedPlan restores, then refuses to reapply a plan
// whose digest differs from the interrupted run's.
func TestResumeRefusesChangedPlan(t *testing.T) {
	w := newWorld(t)
	u := &testUnit{root: w.root}
	w.abandon(t, engine.PointMutation, u)
	e, _ := w.engine(t)
	changed := func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
		p := testPlan()
		p.Digest = "digest-2"
		return u, p, nil
	}
	if _, err := e.Resume(context.Background(), changed, engine.ResumeOptions{}); !errors.Is(err, engine.ErrPlanChanged) {
		t.Fatalf("err = %v, want ErrPlanChanged", err)
	}
	w.assertState(t, originalState(), "after refused reapply")
	if r := w.record(t); r.Status != engine.StatusIdle {
		t.Fatalf("record = %+v, want idle", r)
	}
}
