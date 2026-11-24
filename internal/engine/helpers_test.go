package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// The test unit's declared paths, resolved under the test root.
const (
	pathA = "/etc/wskit-test/a.conf" // replaced with WriteFile
	pathB = "/etc/wskit-test/b.conf" // gets a managed block
	pathC = "/etc/wskit-test/c.conf" // removed
	pathD = "/etc/wskit-test/d.conf" // created
	bootA = "boot-a"
)

var errNotRestored = errors.New("apply started from a state that was not restored")

func ptr(s string) *string { return &s }

// originalState is the machine before any apply; appliedState after one.
func originalState() map[string]*string {
	return map[string]*string{
		pathA: ptr("old-a\n"),
		pathB: ptr("foreign 1\nforeign 2"),
		pathC: ptr("old-c\n"),
		pathD: nil,
	}
}

func appliedState() map[string]*string {
	return map[string]*string{
		pathA: ptr("new-a\n"),
		pathB: ptr("foreign 1\nforeign 2\n# BEGIN wskit-b\nmanaged\n# END wskit-b\n"),
		pathC: nil,
		pathD: ptr("new-d\n"),
	}
}

// world is one test machine: a test root, a journal, a host lock, a fake
// process table and a fake containment.
type world struct {
	dir, root, journal, hostLock string
	table                        *platformtest.ProcessTable
	cont                         *platformtest.Containment
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := worldAt(t.TempDir())
	if err := os.MkdirAll(filepath.Join(w.root, "etc", "wskit-test"), 0o750); err != nil {
		t.Fatal(err)
	}
	for p, c := range originalState() {
		if c != nil {
			if err := os.WriteFile(filepath.Join(w.root, p), []byte(*c), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return w
}

func worldAt(dir string) *world {
	table := platformtest.NewProcessTable(bootA)
	return &world{
		dir: dir, root: filepath.Join(dir, "root"), journal: filepath.Join(dir, "journal"),
		hostLock: filepath.Join(dir, "host.lock"), table: table, cont: platformtest.NewContainment(table),
	}
}

// wireHostnameRunner routes hostnamectl/scutil invocations launched inside
// recorded containment to runner so SetState runs end-to-end through
// Containment.Run in tests.
func (w *world) wireHostnameRunner(runner platform.CommandRunner) {
	w.cont.SetRun(func(ctx context.Context, spec platform.LaunchSpec) (contract.CommandResult, error) {
		if len(spec.Argv) > 0 && (spec.Argv[0] == "/usr/bin/hostnamectl" || spec.Argv[0] == "/usr/sbin/scutil") {
			res, err := runner.Run(ctx, platform.ExecSpec{
				Argv: spec.Argv,
				Env:  spec.Env,
				Dir:  spec.Dir,
			})
			if spec.Output != nil && res.Stdout != "" {
				_, _ = spec.Output.Write([]byte(res.Stdout))
			}
			stderrW := spec.Stderr
			if stderrW == nil {
				stderrW = spec.Output
			}
			if stderrW != nil && res.Stderr != "" {
				_, _ = stderrW.Write([]byte(res.Stderr))
			}
			return contract.CommandResult{ExitCode: res.ExitCode, Duration: res.Duration}, err
		}
		return contract.CommandResult{}, nil
	})
}

// engine returns an engine with a new live identity in the world's table.
func (w *world) engine(t testing.TB, mod ...func(*engine.Options)) (*engine.Engine, platform.ProcessIdentity) {
	t.Helper()
	self := w.table.Spawn()
	o := engine.Options{
		JournalDir: w.journal, HostLock: w.hostLock, Root: w.root, Scope: contract.ScopeUser,
		Containment: w.cont, Inspector: w.table.Inspector(self),
		Grace: 50 * time.Millisecond, KillWait: 50 * time.Millisecond, LockPoll: time.Millisecond,
		LookupEnv: func(string) (string, bool) { return "", false },
	}
	for _, m := range mod {
		m(&o)
	}
	e, err := engine.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return e, self
}

func (w *world) record(t testing.TB) *engine.Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.journal, "record.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var r engine.Record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func (w *world) receipt(t testing.TB, id contract.RunID) *contract.Receipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.journal, "receipts", string(id)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var r contract.Receipt
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// state reads the declared paths under the root.
func (w *world) state(t testing.TB) map[string]*string {
	t.Helper()
	out := map[string]*string{}
	for p := range originalState() {
		data, err := os.ReadFile(filepath.Join(w.root, p)) //nolint:gosec // test root in a temp dir
		switch {
		case errors.Is(err, os.ErrNotExist):
			out[p] = nil
		case err != nil:
			t.Fatal(err)
		default:
			out[p] = ptr(string(data))
		}
	}
	return out
}

func sameState(a, b map[string]*string) bool {
	for p := range originalState() {
		x, y := a[p], b[p]
		if (x == nil) != (y == nil) || (x != nil && *x != *y) {
			return false
		}
	}
	return true
}

func (w *world) assertState(t testing.TB, want map[string]*string, what string) {
	t.Helper()
	if got := w.state(t); !sameState(got, want) {
		t.Fatalf("%s: state = %s, want %s", what, show(got), show(want))
	}
}

// assertNoTemps fails if a temporary file is left next to a target.
func (w *world) assertNoTemps(t testing.TB) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(w.root, "etc", "wskit-test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".wskit-tmp-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

func show(s map[string]*string) string {
	var b strings.Builder
	for _, p := range []string{pathA, pathB, pathC, pathD} {
		b.WriteString(filepath.Base(p) + "=")
		if s[p] == nil {
			b.WriteString("<absent> ")
		} else {
			b.WriteString(strings.ReplaceAll(*s[p], "\n", `\n`) + " ")
		}
	}
	return b.String()
}

// testUnit writes, runs a command, edits a block, removes and creates. When
// hostProv is set it also declares and updates hostname.static through
// tx.SetState. Its Apply refuses to start from anything but the original
// state, so a reapply proves the restore ran first.
type testUnit struct {
	root string
	// hostProv, when non-nil, exercises SetState during Apply and verifies
	// hostname restoration before reapply.
	hostProv contract.StateProvider
	// pkg runs the command as a package-manager transaction.
	pkg bool
	// afterRun runs inside Apply after the command.
	afterRun func(ctx context.Context) error
	// verifyOutcome overrides Verify's outcome when set.
	verifyOutcome contract.Outcome
}

const (
	testOrigHostname    = "lab-fault-orig"
	testAppliedHostname = "lab-fault-applied"
)

func (u *testUnit) ID() contract.UnitID { return contract.UnitShell }

func (u *testUnit) Describe(context.Context) (contract.Description, error) {
	desc := contract.Description{
		Unit: contract.UnitShell, Scope: contract.ScopeUser,
		OwnedPaths: []string{pathA, pathB, pathC, pathD},
	}
	if u.hostProv != nil {
		desc.OwnedStateKeys = []contract.StateKey{platform.HostnameKeyStatic}
	}
	return desc, nil
}

func testPlan() contract.Plan {
	return contract.Plan{ID: "plan-1", Unit: contract.UnitShell, Digest: "digest-1"}
}

func (u *testUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return testPlan(), nil
}

func (u *testUnit) read() map[string]*string {
	out := map[string]*string{}
	for p := range originalState() {
		if data, err := os.ReadFile(filepath.Join(u.root, p)); err == nil { //nolint:gosec // test root in a temp dir
			out[p] = ptr(string(data))
		}
	}
	return out
}

func (u *testUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	if !sameState(u.read(), originalState()) {
		return errNotRestored
	}
	if u.hostProv != nil {
		got, err := u.hostProv.Read(ctx, platform.HostnameKeyStatic)
		if err != nil || got.Value != testOrigHostname {
			return errNotRestored
		}
	}
	owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
	if err := tx.WriteFile(ctx, pathA, []byte("new-a\n"), 0o644, owner); err != nil {
		return err
	}
	res, err := tx.Run(ctx, contract.Command{Argv: []string{"/usr/bin/true"}, PackageTransaction: u.pkg})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return errors.New("command failed")
	}
	if u.afterRun != nil {
		if err := u.afterRun(ctx); err != nil {
			return err
		}
	}
	if u.hostProv != nil {
		if err := tx.SetState(ctx, u.hostProv, platform.HostnameKeyStatic, contract.State{Value: testAppliedHostname}); err != nil {
			return err
		}
	}
	if err := tx.EnsureBlock(ctx, pathB, contract.ManagedBlock{ID: "wskit-b", Begin: "# BEGIN wskit-b", End: "# END wskit-b", Body: "managed"}); err != nil {
		return err
	}
	if err := tx.Remove(ctx, pathC); err != nil {
		return err
	}
	return tx.WriteFile(ctx, pathD, []byte("new-d\n"), 0o600, owner)
}

func (u *testUnit) Verify(ctx context.Context, _ contract.Plan) (contract.Result, error) {
	out := contract.OutcomeFailed
	if sameState(u.read(), appliedState()) {
		out = contract.OutcomeVerified
	}
	if out == contract.OutcomeVerified && u.hostProv != nil {
		got, err := u.hostProv.Read(ctx, platform.HostnameKeyStatic)
		if err != nil || got.Value != testAppliedHostname {
			out = contract.OutcomeFailed
		}
	}
	if u.verifyOutcome != "" {
		out = u.verifyOutcome
	}
	return contract.Result{Unit: contract.UnitShell, Outcome: out}, nil
}

func (u *testUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }

func (u *testUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitShell}, nil
}

func resolverFor(u contract.Unit) engine.Resolver {
	return func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
		return u, testPlan(), nil
	}
}

// crashSentinel is the panic value an in-process simulated crash uses.
type crashSentinel struct{}

// crashAt returns a fault hook that panics at point p, as a crash would stop
// the run there. Locks are released by the deferred unlocks, as the kernel
// releases them on death; the engine writes no state in deferred code.
func crashAt(p engine.Point) engine.FaultFunc {
	return func(got engine.Point) {
		if got == p {
			panic(crashSentinel{})
		}
	}
}

// runCrashing runs fn and reports whether it crashed through crashAt.
func runCrashing(fn func()) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crashSentinel); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	fn()
	return false
}

// abandon leaves an apply interrupted at point p by a process that is then
// dead, and returns the dead executor's identity.
func (w *world) abandon(t *testing.T, p engine.Point, u contract.Unit) platform.ProcessIdentity {
	t.Helper()
	e, self := w.engine(t, func(o *engine.Options) { o.Fault = crashAt(p) })
	if !runCrashing(func() { _ = e.Apply(context.Background(), u, testPlan()) }) {
		t.Fatalf("apply did not reach %s", p)
	}
	w.table.Kill(self)
	return self
}
