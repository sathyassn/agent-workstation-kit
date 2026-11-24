package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

func TestApplyWritesReceiptAndEvents(t *testing.T) {
	w := newWorld(t)
	sink := contracttest.NewFakeSink(0)
	e, _ := w.engine(t, func(o *engine.Options) { o.Sink = sink })
	if err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan()); err != nil {
		t.Fatal(err)
	}
	assertFinished(t, w, "after apply")
	rec := w.record(t)
	r := w.receipt(t, rec.RunID)
	if len(r.Changes) != 4 || r.PlanDigest != testPlan().Digest {
		t.Fatalf("receipt = %+v, want 4 changes and the plan digest", r)
	}
	for _, ch := range r.Changes {
		if ch.Backup == "" {
			t.Fatalf("change %+v has no backup reference", ch)
		}
	}
	events := sink.Events()
	for i, ev := range events {
		if ev.Seq != uint64(i+1) || ev.RunID != rec.RunID || ev.Unit != contract.UnitShell {
			t.Fatalf("event %d = %+v, want seq %d of run %s", i, ev, i+1, rec.RunID)
		}
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
}

// TestApplyFailureRestores: a failing Apply or Verify restores every
// declared path and closes the run with a failed receipt.
func TestApplyFailureRestores(t *testing.T) {
	tests := []struct {
		name string
		unit func(root string) contract.Unit
		want error
	}{
		{"apply error", func(root string) contract.Unit {
			return &testUnit{root: root, afterRun: func(context.Context) error { return errNotRestored }}
		}, errNotRestored},
		{"verify not verified", func(root string) contract.Unit {
			return &testUnit{root: root, verifyOutcome: contract.OutcomeFailed}
		}, engine.ErrVerifyFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			e, _ := w.engine(t)
			err := e.Apply(context.Background(), tc.unit(w.root), testPlan())
			if !errors.Is(err, engine.ErrApplyFailed) || !errors.Is(err, tc.want) || engine.ExitCode(err) != contract.ExitFailed {
				t.Fatalf("err = %v, want ErrApplyFailed wrapping %v", err, tc.want)
			}
			w.assertState(t, originalState(), "after failure")
			w.assertNoTemps(t)
			rec := w.record(t)
			if rec.Status != engine.StatusIdle {
				t.Fatalf("record = %+v", rec)
			}
			if r := w.receipt(t, rec.RunID); r == nil || r.Outcome != contract.OutcomeFailed {
				t.Fatalf("receipt = %+v, want failed", r)
			}
		})
	}
}

// failingSink is an event log that cannot write.
type failingSink struct {
	mu  sync.Mutex
	err error
}

func (s *failingSink) Deliver(contract.Event) {}
func (s *failingSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// TestEventLogFailureFailsClosed: when the event log reports a failed
// write, the step stops, the engine restores and records the failure.
func TestEventLogFailureFailsClosed(t *testing.T) {
	w := newWorld(t)
	sink := &failingSink{}
	e, _ := w.engine(t, func(o *engine.Options) { o.Sink = sink })
	u := &testUnit{root: w.root, afterRun: func(context.Context) error {
		sink.mu.Lock()
		sink.err = errors.New("disk full")
		sink.mu.Unlock()
		return nil
	}}
	err := e.Apply(context.Background(), u, testPlan())
	if !errors.Is(err, engine.ErrEventLog) || !errors.Is(err, engine.ErrApplyFailed) {
		t.Fatalf("err = %v, want ErrEventLog and ErrApplyFailed", err)
	}
	w.assertState(t, originalState(), "after event log failure")
}

// refusalUnit tries one undeclared or unsupported operation.
type refusalUnit struct {
	testUnit
	op func(ctx context.Context, tx contract.Tx) error
}

func (u *refusalUnit) Describe(ctx context.Context) (contract.Description, error) {
	d, err := u.testUnit.Describe(ctx)
	d.OwnedStateKeys = []contract.StateKey{"package:zsh"}
	d.DeclaredEnv = []string{"WSKIT_OK"}
	return d, err
}

func (u *refusalUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return u.op(ctx, tx)
}

// TestTxRefusesUndeclaredTargets: every mutating call refuses a target not
// in the description, before it changes anything, with the contract's typed
// error; operations this engine does not provide yet refuse with
// ErrUnsupported after the same check.
func TestTxRefusesUndeclaredTargets(t *testing.T) {
	const undeclared = "/etc/wskit-test/undeclared.conf"
	owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
	tests := []struct {
		name string
		op   func(ctx context.Context, tx contract.Tx) error
		kind contract.TargetKind
		want error
	}{
		{"WriteFile", func(ctx context.Context, tx contract.Tx) error {
			return tx.WriteFile(ctx, undeclared, []byte("x"), 0o600, owner)
		}, contract.TargetPath, contract.ErrUndeclaredTarget},
		{"WriteFile relative", func(ctx context.Context, tx contract.Tx) error {
			return tx.WriteFile(ctx, "etc/wskit-test/a.conf", []byte("x"), 0o600, owner)
		}, contract.TargetPath, contract.ErrUndeclaredTarget},
		{"WriteFile dot-dot", func(ctx context.Context, tx contract.Tx) error {
			return tx.WriteFile(ctx, "/etc/wskit-test/../wskit-test/a.conf", []byte("x"), 0o600, owner)
		}, contract.TargetPath, contract.ErrUndeclaredTarget},
		{"EnsureBlock", func(ctx context.Context, tx contract.Tx) error {
			return tx.EnsureBlock(ctx, undeclared, contract.ManagedBlock{ID: "b", Begin: "# b", End: "# /b x b"})
		}, contract.TargetPath, contract.ErrUndeclaredTarget},
		{"RemoveBlock", func(ctx context.Context, tx contract.Tx) error { return tx.RemoveBlock(ctx, undeclared, "b") }, contract.TargetPath, contract.ErrUndeclaredTarget},
		{"Remove", func(ctx context.Context, tx contract.Tx) error { return tx.Remove(ctx, undeclared) }, contract.TargetPath, contract.ErrUndeclaredTarget},
		{"Run env", func(ctx context.Context, tx contract.Tx) error {
			_, err := tx.Run(ctx, contract.Command{Argv: []string{"/usr/bin/true"}, EnvKeys: []string{"WSKIT_SECRET"}})
			return err
		}, contract.TargetEnvKey, contract.ErrUndeclaredTarget},
		{"Package undeclared", func(ctx context.Context, tx contract.Tx) error {
			return tx.Package(ctx, contract.PackageState{Name: "curl", Present: true})
		}, contract.TargetStateKey, contract.ErrUndeclaredTarget},
		{"Package declared", func(ctx context.Context, tx contract.Tx) error {
			return tx.Package(ctx, contract.PackageState{Name: "zsh", Present: true})
		}, "", engine.ErrUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			e, _ := w.engine(t)
			err := e.Apply(context.Background(), &refusalUnit{testUnit: testUnit{root: w.root}, op: tc.op}, testPlan())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var ute *contract.UndeclaredTargetError
			if tc.kind != "" && (!errors.As(err, &ute) || ute.Kind != tc.kind) {
				t.Fatalf("err = %v, want an undeclared %s", err, tc.kind)
			}
			w.assertState(t, originalState(), "after refusal")
			if _, err := os.Stat(filepath.Join(w.root, undeclared)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("undeclared path was created")
			}
			if len(w.cont.Launched()) != 0 {
				t.Fatal("a command ran after a refusal")
			}
		})
	}
}

// TestRunPassesOnlyAllowedEnvironment: a tool gets PATH, the allowlist and
// its declared keys, nothing else.
func TestRunPassesOnlyAllowedEnvironment(t *testing.T) {
	w := newWorld(t)
	env := map[string]string{"HOME": "/home/x", "WSKIT_OK": "yes", "AWS_SECRET_ACCESS_KEY": "no"}
	e, _ := w.engine(t, func(o *engine.Options) {
		o.LookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	})
	u := &refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
		_, err := tx.Run(ctx, contract.Command{Argv: []string{"tool"}, EnvKeys: []string{"WSKIT_OK"}})
		return err
	}}
	_ = e.Apply(context.Background(), u, testPlan())
	launched := w.cont.Launched()
	if len(launched) != 1 {
		t.Fatalf("launched %d", len(launched))
	}
	got := map[string]bool{}
	for _, kv := range launched[0].Env {
		got[kv] = true
	}
	for _, want := range []string{"HOME=/home/x", "WSKIT_OK=yes"} {
		if !got[want] {
			t.Errorf("env %v lacks %s", launched[0].Env, want)
		}
	}
	for kv := range got {
		if kv == "AWS_SECRET_ACCESS_KEY=no" {
			t.Errorf("undeclared variable passed: %s", kv)
		}
	}
	if launched[0].Scope != contract.ScopeUser || launched[0].Name == "" {
		t.Errorf("launch = %+v", launched[0])
	}
}

// TestNewRefusesUnsafeOptions covers option validation and a journal
// folder others can write.
func TestNewRefusesUnsafeOptions(t *testing.T) {
	dir := t.TempDir()
	loose := filepath.Join(dir, "loose")
	if err := os.Mkdir(loose, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o777); err != nil { //nolint:gosec // deliberately unsafe folder under test
		t.Fatal(err)
	}
	w := worldAt(dir)
	base := engine.Options{JournalDir: filepath.Join(dir, "j"), HostLock: filepath.Join(dir, "h"), Scope: contract.ScopeUser, Containment: w.cont, Inspector: w.table.Inspector(w.table.Spawn())}
	tests := []struct {
		name string
		mod  func(*engine.Options)
		want error
	}{
		{"relative journal", func(o *engine.Options) { o.JournalDir = "j" }, engine.ErrInvalidOptions},
		{"no host lock", func(o *engine.Options) { o.HostLock = "" }, engine.ErrInvalidOptions},
		{"no containment", func(o *engine.Options) { o.Containment = nil }, engine.ErrInvalidOptions},
		{"bad scope", func(o *engine.Options) { o.Scope = "nobody" }, engine.ErrInvalidOptions},
		{"journal writable by others", func(o *engine.Options) { o.JournalDir = loose }, platform.ErrUnsafeDir},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mod(&o)
			if _, err := engine.New(o); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCorruptRecordFailsClosed: a record that does not decode blocks the
// journal instead of being guessed at.
func TestCorruptRecordFailsClosed(t *testing.T) {
	w := newWorld(t)
	e, _ := w.engine(t)
	if err := os.WriteFile(filepath.Join(w.journal, "record.json"), []byte(`{"version":1,"op":"apply","status":"sideways"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(context.Background(), &testUnit{root: w.root}, testPlan()); !errors.Is(err, engine.ErrCorruptRecord) {
		t.Fatalf("apply err = %v", err)
	}
	if _, err := e.Resume(context.Background(), resolverFor(&testUnit{root: w.root}), engine.ResumeOptions{}); !errors.Is(err, engine.ErrCorruptRecord) {
		t.Fatalf("resume err = %v", err)
	}
	w.assertState(t, originalState(), "after corrupt record")
}

// TestRefusesSymlinkTargets is round 3, item 7. The contract cannot yet say
// that a unit handles symbolic links, so a declared path that is a link is
// refused: at the backup when it is one from the start, and before WriteFile
// or Remove when it becomes one during the apply. The link and what it
// points to are left as they were.
func TestRefusesSymlinkTargets(t *testing.T) {
	owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
	tests := []struct {
		name  string
		early bool // the link exists before the apply
		op    func(ctx context.Context, tx contract.Tx) error
	}{
		{"link before the apply", true, func(ctx context.Context, tx contract.Tx) error {
			return tx.WriteFile(ctx, pathD, []byte("replaced\n"), 0o600, owner)
		}},
		{"WriteFile over a new link", false, func(ctx context.Context, tx contract.Tx) error {
			return tx.WriteFile(ctx, pathD, []byte("replaced\n"), 0o600, owner)
		}},
		{"Remove of a new link", false, func(ctx context.Context, tx contract.Tx) error {
			return tx.Remove(ctx, pathD)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			outside := filepath.Join(w.dir, "outside.conf")
			if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(w.root, pathD)
			makeLink := func() error { return os.Symlink(outside, link) }
			if tc.early {
				if err := makeLink(); err != nil {
					t.Fatal(err)
				}
			}
			e, _ := w.engine(t)
			u := &refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
				if !tc.early {
					if err := makeLink(); err != nil {
						return err
					}
				}
				return tc.op(ctx, tx)
			}}
			err := e.Apply(context.Background(), u, testPlan())
			if !errors.Is(err, engine.ErrUnsupportedTarget) {
				t.Fatalf("err = %v, want ErrUnsupportedTarget", err)
			}
			if got, err := os.Readlink(link); tc.early && (err != nil || got != outside) {
				t.Fatalf("link = %q, %v; want it untouched", got, err)
			}
			if data, _ := os.ReadFile(outside); string(data) != "outside\n" { //nolint:gosec // test temp dir
				t.Fatalf("link target changed: %q", data)
			}
		})
	}
}

// TestRestoreKeepsSetuidBit: a restore puts back the full mode, setuid
// included, and the owner is set before the mode so chown cannot clear it.
func TestRestoreKeepsSetuidBit(t *testing.T) {
	w := newWorld(t)
	a := filepath.Join(w.root, pathA)
	if err := os.Chmod(a, 0o700|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	e, _ := w.engine(t)
	u := &refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
		if err := tx.WriteFile(ctx, pathA, []byte("x"), 0o600, contract.Owner{UID: os.Getuid(), GID: os.Getgid()}); err != nil {
			return err
		}
		return errNotRestored
	}}
	if err := e.Apply(context.Background(), u, testPlan()); !errors.Is(err, engine.ErrApplyFailed) {
		t.Fatalf("err = %v", err)
	}
	info, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModePerm | os.ModeSetuid); got != 0o700|os.ModeSetuid {
		t.Fatalf("mode after restore = %v, want -rwx------ with setuid", got)
	}
}

// TestEnsureBlockRefusesSymlink: editing a block through a link would
// replace the link with a file, so it is refused and nothing changes.
func TestEnsureBlockRefusesSymlink(t *testing.T) {
	w := newWorld(t)
	b := filepath.Join(w.root, pathB)
	target := filepath.Join(w.dir, "real-b.conf")
	if err := os.Rename(b, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, b); err != nil {
		t.Fatal(err)
	}
	e, _ := w.engine(t)
	u := &refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
		return tx.EnsureBlock(ctx, pathB, contract.ManagedBlock{ID: "wskit-b", Begin: "# BEGIN wskit-b", End: "# END wskit-b", Body: "x"})
	}}
	if err := e.Apply(context.Background(), u, testPlan()); !errors.Is(err, engine.ErrUnsupportedTarget) {
		t.Fatalf("err = %v, want ErrUnsupportedTarget", err)
	}
	if got, err := os.Readlink(b); err != nil || got != target {
		t.Fatalf("link = %q, %v", got, err)
	}
}

// TestRunRefusesLoaderVariables is decision d at the engine: a unit that
// declares a loader or systemd variable is refused with exit 2, before any
// launch.
func TestRunRefusesLoaderVariables(t *testing.T) {
	w := newWorld(t)
	e, _ := w.engine(t, func(o *engine.Options) {
		o.LookupEnv = func(string) (string, bool) { return "/tmp/x.so", true }
	})
	u := &loaderUnit{refusalUnit: refusalUnit{testUnit: testUnit{root: w.root}, op: func(ctx context.Context, tx contract.Tx) error {
		_, err := tx.Run(ctx, contract.Command{Argv: []string{"tool"}, EnvKeys: []string{"LD_PRELOAD"}})
		return err
	}}}
	err := e.Apply(context.Background(), u, testPlan())
	if !errors.Is(err, platform.ErrForbiddenEnv) || engine.ExitCode(err) != contract.ExitRefused {
		t.Fatalf("err = %v (exit %d), want ErrForbiddenEnv and exit 2", err, engine.ExitCode(err))
	}
	if len(w.cont.Launched()) != 0 {
		t.Fatal("a command launched with a loader variable")
	}
}

type loaderUnit struct{ refusalUnit }

func (u *loaderUnit) Describe(ctx context.Context) (contract.Description, error) {
	d, err := u.refusalUnit.Describe(ctx)
	d.DeclaredEnv = []string{"LD_PRELOAD"}
	return d, err
}

// TestRefusesWritableAncestors is round 3, item 5a: a journal or host lock
// below a folder that others can write is refused, at New and again on a
// later lock acquire, since such a folder can be swapped. A root-owned
// sticky folder such as /tmp is accepted: its sticky bit keeps others from
// replacing entries they do not own.
func TestRefusesWritableAncestors(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(base, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil { //nolint:gosec // deliberately unsafe folder under test
		t.Fatal(err)
	}
	w := worldAt(base)
	opts := func(journal, lock string) engine.Options {
		return engine.Options{JournalDir: journal, HostLock: lock, Root: base, Scope: contract.ScopeUser, Containment: w.cont, Inspector: w.table.Inspector(w.table.Spawn())}
	}
	for name, o := range map[string]engine.Options{
		"journal below a writable folder":   opts(filepath.Join(shared, "journal"), filepath.Join(base, "host.lock")),
		"host lock below a writable folder": opts(filepath.Join(base, "journal"), filepath.Join(shared, "host.lock")),
	} {
		if _, err := engine.New(o); !errors.Is(err, platform.ErrUnsafeDir) {
			t.Errorf("%s: err = %v, want ErrUnsafeDir", name, err)
		}
	}

	// A folder that becomes writable after New is refused at the next lock.
	safe := filepath.Join(base, "safe")
	if err := os.Mkdir(safe, 0o700); err != nil {
		t.Fatal(err)
	}
	e, err := engine.New(opts(filepath.Join(safe, "journal"), filepath.Join(base, "host.lock")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(safe, 0o777); err != nil { //nolint:gosec // deliberately unsafe folder under test
		t.Fatal(err)
	}
	if _, err := e.Resume(context.Background(), resolverFor(&testUnit{root: base}), engine.ResumeOptions{}); !errors.Is(err, platform.ErrUnsafeDir) {
		t.Fatalf("lock below a folder made writable: err = %v, want ErrUnsafeDir", err)
	}

	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		tmp, err := os.MkdirTemp("/tmp", "wskit-anc")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmp) })
		if _, err := engine.New(opts(filepath.Join(tmp, "journal"), filepath.Join(tmp, "host.lock"))); err != nil {
			t.Fatalf("journal below the root-owned sticky /tmp: %v", err)
		}
	}
}
