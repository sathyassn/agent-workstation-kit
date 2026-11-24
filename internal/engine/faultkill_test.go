package engine_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// The child-process protocol of the kill harness.
const (
	envChildDir   = "WSKIT_TEST_CHILD_DIR"
	envChildMode  = "WSKIT_TEST_CHILD_MODE"
	envKillAt     = "WSKIT_TEST_KILL_AT"
	envKillPoint  = "WSKIT_TEST_KILL_POINT"
	modeApply     = "apply"
	modeResume    = "resume"
	killedFile    = "killed"
	completedFile = "completed"
	faultHostFile = "fault-hostname.json"
	maxBoundaries = 500
)

func faultHostnameProvider(t *testing.T, w *world) contract.StateProvider {
	t.Helper()
	runner, err := platformtest.NewFileHostnameRunner(filepath.Join(w.dir, faultHostFile), platformtest.HostnameState{
		Static:    testOrigHostname,
		Pretty:    "Lab Fault Orig",
		Transient: testOrigHostname,
		Local:     testOrigHostname,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.wireHostnameRunner(runner)
	return newHostnameProvider(runner)
}

// TestHelperFaultChild is not a test: it is the child process of the kill
// harness. It runs one apply or resume and SIGKILLs itself at the Nth
// boundary (WSKIT_TEST_KILL_AT) or at the first time it reaches a named one
// (WSKIT_TEST_KILL_POINT). SIGKILL runs no deferred code and the kernel drops
// its locks, so the parent sees exactly what a crash leaves.
func TestHelperFaultChild(t *testing.T) {
	dir := os.Getenv(envChildDir)
	if dir == "" {
		t.Skip("child process of the kill harness")
	}
	n, _ := strconv.Atoi(os.Getenv(envKillAt))
	point := engine.Point(os.Getenv(envKillPoint))
	count := 0
	fault := func(p engine.Point) {
		count++
		if count == n || (point != "" && p == point) {
			_ = os.WriteFile(filepath.Join(dir, killedFile), []byte(p), 0o600) //nolint:gosec // the parent test's temp dir
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	w := worldAt(dir)
	hostProv := faultHostnameProvider(t, w)
	providers := map[contract.StateProviderName]contract.StateProvider{
		contract.ProviderHostname: hostProv,
	}
	e, _ := w.engine(t, func(o *engine.Options) {
		o.Fault = fault
		o.Providers = providers
	})
	u := &testUnit{root: w.root, hostProv: hostProv}
	var err error
	if os.Getenv(envChildMode) == modeResume {
		_, err = e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	} else {
		err = e.Apply(context.Background(), u, testPlan())
	}
	if err != nil {
		t.Fatalf("child %s: %v", os.Getenv(envChildMode), err)
	}
	if err := os.WriteFile(filepath.Join(dir, completedFile), nil, 0o600); err != nil { //nolint:gosec // the parent test's temp dir
		t.Fatal(err)
	}
}

// runChild runs the child in mode and reports the point it was killed at,
// or completed when it ran to the end.
func runChild(t *testing.T, dir, mode string, env ...string) (engine.Point, bool) {
	t.Helper()
	_ = os.Remove(filepath.Join(dir, killedFile))
	// #nosec G204,G702 -- re-runs this test binary.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperFaultChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), append([]string{envChildDir + "=" + dir, envChildMode + "=" + mode}, env...)...)
	out, err := cmd.CombinedOutput()
	if _, statErr := os.Stat(filepath.Join(dir, completedFile)); statErr == nil {
		return "", true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child %s: %v\n%s", mode, err, out)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL { //nolint:misspell // Go API name
		t.Fatalf("child %s ended without SIGKILL: %v\n%s", mode, err, out)
	}
	p, err := os.ReadFile(filepath.Join(dir, killedFile)) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	return engine.Point(p), false
}

// nextStart is what the next start does: resume, and when there was nothing
// to resume (the kill came before the intent record), apply. It returns the
// resume action.
func nextStart(t *testing.T, w *world) engine.ResumeAction {
	t.Helper()
	hostProv := faultHostnameProvider(t, w)
	providers := map[contract.StateProviderName]contract.StateProvider{
		contract.ProviderHostname: hostProv,
	}
	u := &testUnit{root: w.root, hostProv: hostProv}
	e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
	res, err := e.Resume(context.Background(), resolverFor(u), engine.ResumeOptions{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Action == engine.ResumeNothing && !finished(t, w) {
		if err := e.Apply(context.Background(), u, testPlan()); err != nil {
			t.Fatalf("apply after a kill before intent: %v", err)
		}
	}
	return res.Action
}

// finished reports whether the last run closed with a verified receipt, as
// when the kill came after the record went idle.
func finished(t *testing.T, w *world) bool {
	t.Helper()
	rec := w.record(t)
	if rec == nil || rec.Status != engine.StatusIdle {
		return false
	}
	r := w.receipt(t, rec.RunID)
	return r != nil && r.Outcome == contract.OutcomeVerified
}

// assertFinished checks the end state every kill must reach: the unit
// applied, the record idle, a verified receipt for the last run, and no
// temporary files. When a fault-hostname fixture was seeded in w.dir, it also
// verifies the applied hostname state.
func assertFinished(t *testing.T, w *world, what string) {
	t.Helper()
	w.assertState(t, appliedState(), what)
	w.assertNoTemps(t)
	if _, err := os.Stat(filepath.Join(w.dir, faultHostFile)); err == nil {
		hostProv := faultHostnameProvider(t, w)
		gotHost, err := hostProv.Read(context.Background(), platform.HostnameKeyStatic)
		if err != nil || gotHost.Value != testAppliedHostname {
			t.Fatalf("%s: hostname = %+v, %v; want %q", what, gotHost, err, testAppliedHostname)
		}
	}
	rec := w.record(t)
	if rec == nil || rec.Status != engine.StatusIdle {
		t.Fatalf("%s: record = %+v, want idle", what, rec)
	}
	if r := w.receipt(t, rec.RunID); r == nil || r.Outcome != contract.OutcomeVerified {
		t.Fatalf("%s: receipt for %s = %+v, want verified", what, rec.RunID, r)
	}
}

// TestKillAtEveryApplyBoundaryRecovers is AC-1: for N = 1, 2, ..., a child
// apply is SIGKILLed at its Nth boundary (including before, inside and after
// SetState), until an apply runs to the end with no kill. After each kill,
// the next start must end with both files and hostname state applied. The
// unit's Apply refuses to start from anything but the original files and
// hostname, so every reapply proves the restore ran first.
func TestKillAtEveryApplyBoundaryRecovers(t *testing.T) {
	seen := map[engine.Point]bool{}
	actions := map[engine.ResumeAction]int{}
	for n := 1; ; n++ {
		if n > maxBoundaries {
			t.Fatal("apply never completed")
		}
		w := newWorld(t)
		_ = faultHostnameProvider(t, w)
		point, completed := runChild(t, w.dir, modeApply, envKillAt+"="+strconv.Itoa(n))
		if completed {
			if n == 1 {
				t.Fatal("child completed without reaching a boundary")
			}
			assertFinished(t, w, "uninterrupted apply")
			t.Logf("killed at %d boundaries; actions %v", n-1, actions)
			break
		}
		beforeVerify := !seen[engine.PointVerified] && point != engine.PointVerified
		seen[point] = true
		what := "kill " + strconv.Itoa(n) + " at " + string(point)
		done := finished(t, w)
		action := nextStart(t, w)
		actions[action]++
		assertFinished(t, w, what)
		switch {
		case action == engine.ResumeNothing && seen[engine.PointIntent] && !done:
			t.Fatalf("%s: a kill after the intent record found nothing to resume", what)
		case beforeVerify && action != engine.ResumeNothing && action != engine.ResumeReapplied:
			t.Fatalf("%s: action %s, want a restore then reapply", what, action)
		}
	}
	for _, p := range []engine.Point{
		engine.PointAtomicTemp, engine.PointAtomicRenamed, engine.PointBackupEntry, engine.PointIntent,
		engine.PointMutation, engine.PointStateBackup, engine.PointStateWrite,
		engine.PointLaunch, engine.PointCommandDone, engine.PointCommandEmptied, engine.PointApplied,
		engine.PointVerified, engine.PointReceipt, engine.PointIdle,
	} {
		if !seen[p] {
			t.Errorf("no kill at %s", p)
		}
	}
	if actions[engine.ResumeReapplied] == 0 || actions[engine.ResumeNothing] == 0 {
		t.Errorf("actions %v: want both kills before the intent and restored-then-reapplied runs", actions)
	}
}

// TestKillDuringRecoveryRecovers kills the apply after SetState mutates the
// hostname, then kills the recovering start at each of its boundaries in turn
// (including mid state-restore at PointStateRestore). The start after that
// must still end with the unit applied: recovery is itself safe to interrupt.
func TestKillDuringRecoveryRecovers(t *testing.T) {
	seen := map[engine.Point]bool{}
	for m := 1; ; m++ {
		if m > maxBoundaries {
			t.Fatal("resume never completed")
		}
		w := newWorld(t)
		_ = faultHostnameProvider(t, w)
		if _, completed := runChild(t, w.dir, modeApply, envKillPoint+"="+string(engine.PointStateWrite)); completed {
			t.Fatal("apply was not killed after SetState write")
		}
		point, completed := runChild(t, w.dir, modeResume, envKillAt+"="+strconv.Itoa(m))
		if completed {
			assertFinished(t, w, "uninterrupted resume")
			t.Logf("resume killed at %d boundaries", m-1)
			break
		}
		seen[point] = true
		nextStart(t, w)
		assertFinished(t, w, "resume kill "+strconv.Itoa(m)+" at "+string(point))
	}
	if !seen[engine.PointStateRestore] {
		t.Errorf("no kill at %s during recovery", engine.PointStateRestore)
	}
}
