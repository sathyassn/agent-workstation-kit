package darwin

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

const label = "dev.agent-workstation-kit.run.r1.1"

type fixedIdentity struct{}

func (fixedIdentity) Identity(_ context.Context, pid int) (platform.ProcessIdentity, error) {
	return platform.ProcessIdentity{BootID: "b", PID: pid, StartTime: "1.2"}, nil
}
func (fixedIdentity) Alive(context.Context, platform.ProcessIdentity) (bool, error) {
	return false, nil
}
func (fixedIdentity) Signal(context.Context, platform.ProcessIdentity, syscall.Signal) error {
	return nil
}

func TestParsePrint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want jobState
		ok   bool
	}{
		{"running", "\tstate = running\n\tpid = 321\n", jobState{state: platform.StateActive, loaded: true, pid: 321}, true},
		{"exited", "\tstate = not running\n\tlast exit code = 3\n", jobState{state: platform.StateGone, loaded: true, exited: true, exitCode: 3}, true},
		{"never exited is pending", "\tstate = not running\n\tlast exit code = (never exited)\n", jobState{state: platform.StatePending, loaded: true}, true},
		{"spawn scheduled is pending", "\tstate = spawn scheduled\n", jobState{state: platform.StatePending, loaded: true}, true},
		{"unknown state", "\tstate = exiting\n", jobState{}, false},
		{"no state", "garbage", jobState{}, false},
	}
	for _, tc := range tests {
		got, err := parsePrint(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s: parsePrint = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

func TestRenderPlistEscapes(t *testing.T) {
	out, err := RenderPlist(label, []string{"/bin/echo", "<&>"}, []string{"A=1&2"}, "", "/x/out", "/x/out")
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(out, new(any)); err != nil {
		t.Fatalf("plist is not XML: %v", err)
	}
	if !bytes.Contains(out, []byte("&lt;&amp;&gt;")) || !bytes.Contains(out, []byte("1&amp;2")) {
		t.Fatalf("not escaped:\n%s", out)
	}
	if _, err := RenderPlist(label, []string{"x"}, []string{"NOEQUALS"}, "", "", ""); !errors.Is(err, ErrInvalidEnv) {
		t.Fatalf("env without = err = %v", err)
	}
}

// launchd fakes launchctl with a loaded job's life: bootstrap loads it as
// pending; it runs for runFor prints and exits with code 3; kill ends a
// running job unless unkillable; bootout unloads it (and ends it); print
// of an unloaded job exits 113.
type launchd struct {
	mu         sync.Mutex
	loaded     bool
	started    bool
	ended      bool
	prints     int
	runFor     int
	pendingFor int
	unkillable bool
	bootoutRC  int
	stuck      bool // bootout is acknowledged but the job keeps running
	registerAt int  // an in-flight bootstrap lands, ended, just after this print
	events     *[]string
}

func (l *launchd) runner() *platformtest.Runner {
	return platformtest.NewRunner(func(argv []string) (platform.ExecResult, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.events != nil {
			*l.events = append(*l.events, "launchctl "+argv[1])
		}
		switch argv[1] {
		case "bootstrap":
			l.loaded = true
			return platform.ExecResult{}, nil
		case "bootout":
			if l.bootoutRC != 0 {
				return platform.ExecResult{ExitCode: l.bootoutRC}, nil
			}
			if !l.loaded {
				return platform.ExecResult{ExitCode: launchctlNotFound}, nil
			}
			if l.stuck {
				return platform.ExecResult{}, nil
			}
			l.loaded, l.ended = false, true
			return platform.ExecResult{}, nil
		case "kill":
			if l.started && !l.ended && !l.unkillable {
				l.ended = true
			}
			return platform.ExecResult{}, nil
		case "print":
			if l.registerAt > 0 && !l.loaded {
				l.prints++
				if l.prints == l.registerAt {
					l.loaded, l.started, l.ended = true, true, true
					l.registerAt = 0
				}
				return platform.ExecResult{ExitCode: launchctlNotFound}, nil
			}
			if !l.loaded {
				return platform.ExecResult{ExitCode: launchctlNotFound}, nil
			}
			l.prints++
			if !l.started && l.prints > l.pendingFor {
				l.started = true
			}
			if l.started && !l.ended && l.runFor > 0 && l.prints > l.pendingFor+l.runFor {
				l.ended = true
			}
			switch {
			case !l.started:
				return platform.ExecResult{Stdout: "\tstate = not running\n\tlast exit code = (never exited)\n"}, nil
			case l.ended:
				return platform.ExecResult{Stdout: "\tstate = not running\n\tlast exit code = 3\n"}, nil
			default:
				return platform.ExecResult{Stdout: "\tstate = running\n\tpid = 321\n"}, nil
			}
		}
		return platform.ExecResult{ExitCode: 1}, nil
	})
}

func privateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "jobs")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRunReadsExitCodeAfterJobEnds(t *testing.T) {
	l := &launchd{runFor: 3, pendingFor: 1}
	r := l.runner()
	dir := privateDir(t)
	c := &Containment{Runner: r, Inspector: fixedIdentity{}, PlistDir: dir, UID: 501, PollInterval: time.Millisecond}
	var ack platform.LaunchAck
	res, err := c.Run(context.Background(), platform.LaunchSpec{Name: label, Scope: contract.ScopeUser, Argv: []string{"/usr/bin/true"}}, func(a platform.LaunchAck) error { ack = a; return nil })
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("Run = %+v, %v; want exit code 3 from the ended job", res, err)
	}
	if ack.Supervisor.PID != 4242 {
		t.Fatalf("ack = %+v, want the gated bootstrap as supervisor", ack)
	}
	if !slices.Equal(r.Calls()[0][:3], []string{"/bin/launchctl", "bootstrap", "gui/501"}) {
		t.Fatalf("first call = %q", r.Calls()[0])
	}
	// Run leaves the ended job for Empty, which unloads it and verifies.
	if err := c.Empty(context.Background(), label, platform.EmptyOptions{Scope: contract.ScopeUser}); err != nil {
		t.Fatal(err)
	}
	if l.loaded {
		t.Fatal("job still loaded after Empty")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("job files left: %v", entries)
	}
}

func TestRunRefusesSharedFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // deliberately unsafe folder under test
		t.Fatal(err)
	}
	r := (&launchd{}).runner()
	c := &Containment{Runner: r, PlistDir: dir}
	if _, err := c.Run(context.Background(), platform.LaunchSpec{Name: label, Argv: []string{"x"}}, nil); !errors.Is(err, platform.ErrUnsafeDir) {
		t.Fatalf("err = %v, want ErrUnsafeDir", err)
	}
	if len(r.Calls()) != 0 {
		t.Fatal("launchctl ran with an unsafe folder")
	}
}

// TestEmptyJob covers findings 1 and AC-2/AC-3 on macOS: each launchd state
// is handled. A running job is signalled; a pending one is booted out before
// it can start; an ended one is booted out with an acknowledgement and
// verified gone; a bootout launchd does not acknowledge leaves it blocked. A
// package transaction is never signalled or unloaded while it runs or may
// start.
func TestEmptyJob(t *testing.T) {
	opts := platform.EmptyOptions{Scope: contract.ScopeRoot, Grace: 2 * time.Millisecond, KillWait: 4 * time.Millisecond, PollInterval: time.Millisecond}
	pkg := opts
	pkg.PackageTransaction = true
	running := func() *launchd { return &launchd{loaded: true, started: true} }
	tests := []struct {
		name      string
		job       *launchd
		opts      platform.EmptyOptions
		signalled bool
		wantErr   error
	}{
		{"not loaded", &launchd{}, opts, false, nil},
		{"ended, booted out and verified", &launchd{loaded: true, started: true, ended: true}, opts, false, nil},
		{"bootout not acknowledged", &launchd{loaded: true, started: true, ended: true, bootoutRC: 5}, opts, false, platform.ErrSupervisorState},
		{"running, signalled until empty", running(), opts, true, nil},
		{"pending, removed before it starts", &launchd{loaded: true, pendingFor: 1000}, opts, true, nil},
		{"cannot be emptied", &launchd{loaded: true, started: true, unkillable: true, stuck: true}, opts, true, platform.ErrContainmentNotEmpty},
		{"package transaction running, never signalled", running(), pkg, false, platform.ErrContainmentNotEmpty},
		{"package transaction pending, never removed", &launchd{loaded: true, pendingFor: 1000}, pkg, false, platform.ErrContainmentNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.job.runner()
			c := &Containment{Runner: r, PlistDir: privateDir(t)}
			err := c.Empty(context.Background(), label, tc.opts)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			var signalled bool
			for _, call := range r.Calls() {
				signalled = signalled || call[1] == "kill"
				if tc.opts.PackageTransaction && call[1] == "bootout" {
					t.Fatalf("package transaction job booted out: %q", r.Calls())
				}
				if strings.HasPrefix(call[len(call)-1], "gui/") {
					t.Fatalf("root job addressed in a gui domain: %q", call)
				}
			}
			if signalled != tc.signalled {
				t.Fatalf("signalled = %v, want %v (calls %q)", signalled, tc.signalled, r.Calls())
			}
			if tc.wantErr == nil && tc.job.loaded {
				t.Fatal("job still loaded after a successful Empty")
			}
		})
	}
}

func TestPrintFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		res  platform.ExecResult
		want platform.ContainmentState
		ok   bool
	}{
		"not loaded":  {platform.ExecResult{ExitCode: launchctlNotFound}, platform.StateGone, true},
		"other error": {platform.ExecResult{ExitCode: 5}, 0, false},
		"unparseable": {platform.ExecResult{Stdout: "???"}, 0, false},
		"running pid": {platform.ExecResult{Stdout: "state = running\npid = 9\n"}, platform.StateActive, true},
		"pending":     {platform.ExecResult{Stdout: "state = not running\n"}, platform.StatePending, true},
	} {
		r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return tc.res, nil })
		c := &Containment{Runner: r}
		got, err := c.State(context.Background(), label, platform.ScopeSelector{})
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s: State = %v, %v; want %v ok=%v", name, got, err, tc.want, tc.ok)
		}
	}
}

// TestPlistIsDurableBeforeBootstrap is finding 6 on macOS: the job
// definition and its folder are fsynced before launchd is asked to load it.
func TestPlistIsDurableBeforeBootstrap(t *testing.T) {
	var events []string
	l := &launchd{runFor: 1}
	inner := l.runner()
	r := platformtest.NewRunner(func(argv []string) (platform.ExecResult, error) {
		events = append(events, "run "+argv[1])
		res, err := inner.Run(context.Background(), platform.ExecSpec{Argv: argv})
		return res, err
	})
	dir := privateDir(t)
	c := &Containment{Runner: r, Inspector: fixedIdentity{}, PlistDir: dir, PollInterval: time.Millisecond}
	c.syncFn = func(f *os.File) error {
		events = append(events, "sync "+filepath.Base(f.Name()))
		return f.Sync()
	}
	if _, err := c.Run(context.Background(), platform.LaunchSpec{Name: label, Scope: contract.ScopeRoot, Argv: []string{"/usr/bin/true"}}, nil); err != nil {
		t.Fatal(err)
	}
	boot := slices.Index(events, "run bootstrap")
	file := slices.Index(events, "sync "+label+".plist")
	folder := slices.Index(events, "sync "+filepath.Base(dir))
	if boot < 0 || file < 0 || folder < 0 || file > boot || folder > boot {
		t.Fatalf("events = %q, want the plist and its folder fsynced before bootstrap", events)
	}
}

// TestLaunchctlIgnoresHostilePathAndEnvironment is finding 2 on macOS: a
// launchctl planted first on PATH is never run, and launchctl gets a fixed
// minimal environment; the tool's variables travel only in the plist.
func TestLaunchctlIgnoresHostilePathAndEnvironment(t *testing.T) {
	hostile := t.TempDir()
	marker := filepath.Join(hostile, "ran")
	if err := os.WriteFile(filepath.Join(hostile, "launchctl"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 113\n"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", hostile)
	c := &Containment{Runner: platform.OSCommandRunner{}, PlistDir: privateDir(t)}
	_ = c.Empty(context.Background(), label, platform.EmptyOptions{})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("launchctl was found through PATH")
	}

	r := (&launchd{runFor: 1}).runner()
	c = &Containment{Runner: r, Inspector: fixedIdentity{}, PlistDir: privateDir(t), PollInterval: time.Millisecond}
	if _, err := c.Run(context.Background(), platform.LaunchSpec{Name: label, Scope: contract.ScopeRoot, Argv: []string{"/usr/bin/true"}, Env: []string{"WSKIT_TOKEN=s3cret"}}, nil); err != nil {
		t.Fatal(err)
	}
	for i, argv := range r.Calls() {
		if argv[0] != "/bin/launchctl" {
			t.Fatalf("call %d runs %q, want /bin/launchctl", i, argv[0])
		}
		for _, kv := range r.Envs()[i] {
			if strings.HasPrefix(kv, "WSKIT_TOKEN=") {
				t.Fatalf("launchctl environment holds %s", kv)
			}
		}
		if len(r.Envs()[i]) == 0 {
			t.Fatalf("call %d has an empty environment, want a fixed PATH", i)
		}
	}
}

// TestPlistIsPrivateAtBootstrap: when launchd is asked to load the job, its
// definition is mode 0600 and owned by this process's user (root, for a
// root scope), in a private folder; the tool's variables live only there.
func TestPlistIsPrivateAtBootstrap(t *testing.T) {
	dir := privateDir(t)
	plist := filepath.Join(dir, label+".plist")
	inner := (&launchd{runFor: 1}).runner()
	checked := false
	r := platformtest.NewRunner(func(argv []string) (platform.ExecResult, error) {
		if argv[1] == "bootstrap" {
			info, err := os.Lstat(plist)
			if err != nil {
				t.Errorf("plist missing at bootstrap: %v", err)
			} else {
				st, _ := info.Sys().(*syscall.Stat_t)
				if info.Mode().Perm() != 0o600 || st == nil || int(st.Uid) != os.Geteuid() {
					t.Errorf("plist mode %v uid %v at bootstrap, want 0600 owned by uid %d", info.Mode().Perm(), st, os.Geteuid())
				}
			}
			checked = true
		}
		return inner.Run(context.Background(), platform.ExecSpec{Argv: argv})
	})
	c := &Containment{Runner: r, Inspector: fixedIdentity{}, PlistDir: dir, PollInterval: time.Millisecond}
	if _, err := c.Run(context.Background(), platform.LaunchSpec{Name: label, Scope: contract.ScopeRoot, Argv: []string{"/usr/bin/true"}, Env: []string{"WSKIT_TOKEN=x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("bootstrap never ran")
	}
}

// recordingProcs is a supervisor that logs signals into a shared event list
// and dies only on SIGKILL.
type recordingProcs struct {
	fixedIdentity
	alive  bool
	events *[]string
}

func (p *recordingProcs) Alive(context.Context, platform.ProcessIdentity) (bool, error) {
	return p.alive, nil
}

func (p *recordingProcs) Signal(_ context.Context, _ platform.ProcessIdentity, sig syscall.Signal) error {
	*p.events = append(*p.events, "supervisor "+sig.String())
	if sig == syscall.SIGKILL {
		p.alive = false
	}
	return nil
}

// TestSupervisorKilledBeforeBootout is round 3, item 3: the supervisor is
// killed before launchd is asked to boot the job out, so a bootstrap still
// in progress cannot load the job again afterwards.
func TestSupervisorKilledBeforeBootout(t *testing.T) {
	var events []string
	l := &launchd{loaded: true, started: true, unkillable: true, events: &events}
	procs := &recordingProcs{alive: true, events: &events}
	c := &Containment{Runner: l.runner(), Inspector: procs, PlistDir: privateDir(t)}
	opts := platform.EmptyOptions{Scope: contract.ScopeRoot, KillWait: 10 * time.Millisecond, PollInterval: time.Millisecond, Supervisor: platform.ProcessIdentity{BootID: "b", PID: 9, StartTime: "1"}}
	if err := c.Empty(context.Background(), label, opts); err != nil {
		t.Fatal(err)
	}
	kill := slices.Index(events, "supervisor killed")
	bootout := slices.Index(events, "launchctl bootout")
	if kill < 0 || bootout < 0 || kill > bootout {
		t.Fatalf("events = %q, want the supervisor killed before the first bootout", events)
	}
}

// TestBootoutMustBeAcknowledged is round 3, item 3: a bootout launchd
// refuses is an error at once, never ignored.
func TestBootoutMustBeAcknowledged(t *testing.T) {
	l := &launchd{loaded: true, started: true, unkillable: true, bootoutRC: 5}
	c := &Containment{Runner: l.runner(), PlistDir: privateDir(t)}
	opts := platform.EmptyOptions{Scope: contract.ScopeRoot, KillWait: 10 * time.Millisecond, PollInterval: time.Millisecond}
	if err := c.Empty(context.Background(), label, opts); !errors.Is(err, platform.ErrSupervisorState) {
		t.Fatalf("err = %v, want ErrSupervisorState", err)
	}
}

// TestUnloadProvesAbsence is round 3, item 3: a job that reads not loaded
// once is not proven absent. Here an in-flight bootstrap lands on a later
// print; unload must boot it out and see it absent twice in a row.
func TestUnloadProvesAbsence(t *testing.T) {
	l := &launchd{registerAt: 3}
	c := &Containment{Runner: l.runner(), PlistDir: privateDir(t)}
	opts := platform.EmptyOptions{Scope: contract.ScopeRoot, KillWait: 10 * time.Millisecond, PollInterval: time.Millisecond}
	if err := c.Empty(context.Background(), label, opts); err != nil {
		t.Fatal(err)
	}
	if l.loaded {
		t.Fatal("a late-loaded job was left loaded")
	}
}
