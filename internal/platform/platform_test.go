package platform_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// fakeControl is a containment that empties after a given number of State
// checks, or once it receives a given signal.
type fakeControl struct {
	mu       sync.Mutex
	active   bool
	emptyOn  syscall.Signal // the signal that ends the processes; 0 never
	selfEnds int            // State checks after which they end by themselves; 0 never
	checks   int
	signals  []syscall.Signal
	stateErr error
	block    bool // State blocks until its context ends
	lateOn   int  // the State check at which a late registration appears
}

func (f *fakeControl) State(ctx context.Context, _ string, _ platform.ScopeSelector) (platform.ContainmentState, error) {
	if f.block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateErr != nil {
		return 0, f.stateErr
	}
	f.checks++
	if f.lateOn > 0 && f.checks == f.lateOn {
		f.active = true
	}
	if f.selfEnds > 0 && f.checks >= f.selfEnds {
		f.active = false
	}
	if f.active {
		return platform.StateActive, nil
	}
	return platform.StateGone, nil
}

func (f *fakeControl) Signal(_ context.Context, _ string, _ platform.ScopeSelector, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, sig)
	if sig == f.emptyOn {
		f.active = false
	}
	return nil
}

// fakeProcs is a supervisor process that dies on a given signal.
type fakeProcs struct {
	mu      sync.Mutex
	alive   bool
	dieOn   syscall.Signal
	signals []syscall.Signal
}

func (p *fakeProcs) Alive(context.Context, platform.ProcessIdentity) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive, nil
}

func (p *fakeProcs) Signal(_ context.Context, _ platform.ProcessIdentity, sig syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals = append(p.signals, sig)
	if sig == p.dieOn {
		p.alive = false
	}
	return nil
}

var errBus = errors.New("bus down")

func TestEmptyBounded(t *testing.T) {
	opts := platform.EmptyOptions{Grace: 5 * time.Millisecond, KillWait: 10 * time.Millisecond, PollInterval: time.Millisecond}
	pkg := opts
	pkg.PackageTransaction = true
	tests := []struct {
		name    string
		ctl     *fakeControl
		opts    platform.EmptyOptions
		signals []syscall.Signal
		wantErr error
	}{
		{"already empty", &fakeControl{}, opts, nil, nil},
		{"ends within grace", &fakeControl{active: true, selfEnds: 2}, opts, nil, nil},
		{"needs SIGTERM", &fakeControl{active: true, emptyOn: syscall.SIGTERM}, opts, []syscall.Signal{syscall.SIGTERM}, nil},
		{"needs SIGKILL", &fakeControl{active: true, emptyOn: syscall.SIGKILL}, opts, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}, nil},
		{"never empties", &fakeControl{active: true}, opts, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}, platform.ErrContainmentNotEmpty},
		{"state unreadable", &fakeControl{active: true, stateErr: errBus}, opts, nil, errBus},
		{"package transaction ends while waited for", &fakeControl{active: true, selfEnds: 3, emptyOn: syscall.SIGTERM}, pkg, nil, nil},
		{"package transaction outlasts the bound", &fakeControl{active: true, emptyOn: syscall.SIGTERM}, pkg, nil, platform.ErrContainmentNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.EmptyBounded(context.Background(), tc.ctl, &fakeProcs{}, "c", platform.ScopeSelector{}, tc.opts)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !slices.Equal(tc.ctl.signals, tc.signals) {
				t.Fatalf("signals = %v, want %v", tc.ctl.signals, tc.signals)
			}
		})
	}
}

// TestEmptyBoundedWaitsForLiveSupervisor is finding 1: a containment the
// OS reports gone is not empty while its recorded supervisor lives, since
// it can still register the containment and start the tool. The supervisor
// is signalled with the containment; a package transaction's is not.
func TestEmptyBoundedWaitsForLiveSupervisor(t *testing.T) {
	sup := platform.ProcessIdentity{BootID: "b", PID: 7, StartTime: "1"}
	opts := platform.EmptyOptions{Grace: 2 * time.Millisecond, KillWait: 4 * time.Millisecond, PollInterval: time.Millisecond, Supervisor: sup}
	procs := &fakeProcs{alive: true, dieOn: syscall.SIGTERM}
	if err := platform.EmptyBounded(context.Background(), &fakeControl{}, procs, "c", platform.ScopeSelector{}, opts); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(procs.signals, []syscall.Signal{syscall.SIGTERM}) || procs.alive {
		t.Fatalf("supervisor signals = %v, alive = %v; want SIGTERM and dead", procs.signals, procs.alive)
	}

	opts.PackageTransaction = true
	procs = &fakeProcs{alive: true, dieOn: syscall.SIGTERM}
	err := platform.EmptyBounded(context.Background(), &fakeControl{}, procs, "c", platform.ScopeSelector{}, opts)
	if !errors.Is(err, platform.ErrContainmentNotEmpty) || len(procs.signals) != 0 {
		t.Fatalf("package transaction: err = %v, signals = %v; want not empty and no signal", err, procs.signals)
	}
}

// TestEmptyBoundedDeadlinePerCall is finding 7: an inspection that hangs
// ends at its own deadline, even under a context that never ends.
func TestEmptyBoundedDeadlinePerCall(t *testing.T) {
	start := time.Now()
	opts := platform.EmptyOptions{CallTimeout: 20 * time.Millisecond}
	err := platform.EmptyBounded(context.Background(), &fakeControl{block: true}, &fakeProcs{}, "c", platform.ScopeSelector{}, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("hung inspection was not bounded")
	}
}

// TestGateHoldsUntilAcknowledged: a gated process runs its argv only after
// the gate returns nil, keeping the PID it was started with; a refused gate
// runs nothing.
func TestGateHoldsUntilAcknowledged(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	argv := []string{"/bin/sh", "-c", "echo $$ > " + marker}
	var gatePID int
	_, err := platform.OSCommandRunner{}.Run(context.Background(), platform.ExecSpec{
		Argv: argv, GateShell: "/bin/sh",
		Gate: func(pid int) error {
			gatePID = pid
			if _, err := os.Stat(marker); err == nil {
				t.Error("argv ran before the gate opened")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker) //nolint:gosec // test temp dir
	if err != nil || strings.TrimSpace(string(got)) != strconv.Itoa(gatePID) {
		t.Fatalf("argv ran as pid %q, gate saw %d (%v)", got, gatePID, err)
	}

	_ = os.Remove(marker)
	_, err = platform.OSCommandRunner{}.Run(context.Background(), platform.ExecSpec{
		Argv: argv, GateShell: "/bin/sh",
		Gate: func(int) error { return errBus },
	})
	if !errors.Is(err, platform.ErrGateClosed) {
		t.Fatalf("refused gate: err = %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("argv ran through a refused gate")
	}
}

func TestOSCommandRunner(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		env  []string
		code int
		out  string
	}{
		{"exit code is a result", []string{"/bin/sh", "-c", "exit 3"}, nil, 3, ""},
		{"exact environment", []string{"/usr/bin/env"}, []string{"ONLY=1"}, 0, "ONLY=1\n"},
		{"empty environment", []string{"/usr/bin/env"}, nil, 0, ""},
		{"signal exit", []string{"/bin/sh", "-c", "kill -9 $$"}, nil, 128 + int(syscall.SIGKILL), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := platform.OSCommandRunner{}.Run(context.Background(), platform.ExecSpec{Argv: tc.argv, Env: tc.env})
			if err != nil || res.ExitCode != tc.code || res.Stdout != tc.out {
				t.Fatalf("Run = %+v, %v; want code %d output %q", res, err, tc.code, tc.out)
			}
		})
	}
	if _, err := (platform.OSCommandRunner{}).Run(context.Background(), platform.ExecSpec{}); !errors.Is(err, platform.ErrCommandEmpty) {
		t.Fatalf("empty argv err = %v", err)
	}
}

func TestOSCommandRunnerTerminatesOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := platform.OSCommandRunner{}.Run(ctx, platform.ExecSpec{Argv: []string{"/bin/sleep", "10"}, KillAfter: 100 * time.Millisecond})
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not stop the process")
	}
	if err == nil && res.ExitCode == 0 {
		t.Fatalf("Run = %+v, %v; want a terminated process", res, err)
	}
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := platform.NewLineWriter(func(l string) { lines = append(lines, l) })
	_, _ = w.Write([]byte("a\r\nb"))
	_, _ = w.Write([]byte("c\nd"))
	w.Flush()
	_, _ = w.Write([]byte(strings.Repeat("x", 70<<10)))
	w.Flush()
	want := []string{"a", "bc", "d"}
	if len(lines) != 5 || lines[0] != want[0] || lines[1] != want[1] || lines[2] != want[2] {
		t.Fatalf("lines = %q (%d), want %q then a long line in two pieces", lines[:min(3, len(lines))], len(lines), want)
	}
}

func TestCheckPrivateDir(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	loose := filepath.Join(dir, "loose")
	file := filepath.Join(dir, "file")
	link := filepath.Join(dir, "link")
	for _, d := range []string{private, loose} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(loose, 0o777); err != nil { //nolint:gosec // deliberately unsafe folder under test
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		ok   bool
	}{{private, true}, {loose, false}, {file, false}, {link, false}, {filepath.Join(dir, "missing"), false}}
	for _, tc := range tests {
		err := platform.CheckPrivateDir(tc.path)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, platform.ErrUnsafeDir)) {
			t.Errorf("CheckPrivateDir(%s) = %v, want ok=%v", filepath.Base(tc.path), err, tc.ok)
		}
	}
}

func TestValidateRunID(t *testing.T) {
	for id, ok := range map[string]bool{"r0a1": true, "run_1-2": true, "": false, "Run": false, "a/b": false, "a.b": false, strings.Repeat("a", 65): false} {
		if err := platform.ValidateRunID(contractRunID(id)); (err == nil) != ok {
			t.Errorf("ValidateRunID(%q) = %v, want ok=%v", id, err, ok)
		}
	}
}

func contractRunID(s string) contract.RunID { return contract.RunID(s) }

func TestCheckTrustedBinary(t *testing.T) {
	own := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(own, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	tests := []struct {
		path string
		ok   bool
	}{{"/bin/sh", true}, {"sh", false}, {own, false}, {"/bin/no-such-tool", false}}
	for _, tc := range tests {
		if err := platform.CheckTrustedBinary(tc.path); (err == nil) != tc.ok || (err != nil && !errors.Is(err, platform.ErrUntrustedBinary)) {
			t.Errorf("CheckTrustedBinary(%q) = %v, want ok=%v", tc.path, err, tc.ok)
		}
	}
}

// TestEmptyBoundedNeedsTwoQuietSamples is round 3, items 1 and 3: one quiet
// sample is not enough, because a registration can still be in flight. Here
// the containment reads gone once, then a late registration makes it
// active; Empty must see it and terminate it.
func TestEmptyBoundedNeedsTwoQuietSamples(t *testing.T) {
	ctl := &fakeControl{lateOn: 2, emptyOn: syscall.SIGTERM}
	opts := platform.EmptyOptions{KillWait: 10 * time.Millisecond, PollInterval: time.Millisecond}
	if err := platform.EmptyBounded(context.Background(), ctl, &fakeProcs{}, "c", platform.ScopeSelector{}, opts); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ctl.signals, []syscall.Signal{syscall.SIGTERM}) || ctl.active {
		t.Fatalf("signals = %v, active = %v; want the late registration terminated", ctl.signals, ctl.active)
	}
}
