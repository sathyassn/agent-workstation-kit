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
	"sync/atomic"
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

func TestParsePrint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want jobState
		ok   bool
	}{
		{"running", "\tstate = running\n\tpid = 321\n", jobState{running: true, pid: 321}, true},
		{"exited", "\tstate = not running\n\tlast exit code = 3\n", jobState{exited: true, exitCode: 3}, true},
		{"never exited", "\tstate = not running\n\tlast exit code = (never exited)\n", jobState{}, true},
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

// launchd fakes launchctl: a job runs for a few prints after bootstrap and
// exits with code 3; kill ends it unless it is unkillable.
type launchd struct {
	prints     atomic.Int32
	runFor     int32
	killed     atomic.Bool
	unkillable bool
}

func (l *launchd) runner() *platformtest.Runner {
	return platformtest.NewRunner(func(argv []string) (platform.ExecResult, error) {
		switch argv[1] {
		case "bootstrap", "bootout":
			return platform.ExecResult{}, nil
		case "kill":
			if !l.unkillable {
				l.killed.Store(true)
			}
			return platform.ExecResult{}, nil
		case "print":
			n := l.prints.Add(1)
			if l.killed.Load() || (l.runFor > 0 && n > l.runFor) {
				return platform.ExecResult{Stdout: "\tstate = not running\n\tlast exit code = 3\n"}, nil
			}
			return platform.ExecResult{Stdout: "\tstate = running\n\tpid = 321\n"}, nil
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
	l := &launchd{runFor: 3}
	r := l.runner()
	dir := privateDir(t)
	c := &Containment{Runner: r, Inspector: fixedIdentity{}, PlistDir: dir, UID: 501, PollInterval: time.Millisecond}
	var ack platform.LaunchAck
	res, err := c.Run(context.Background(), platform.LaunchSpec{Name: label, Scope: contract.ScopeUser, Argv: []string{"/usr/bin/true"}}, func(a platform.LaunchAck) { ack = a })
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("Run = %+v, %v; want exit code 3 from the ended job", res, err)
	}
	if ack.Tool.PID != 321 {
		t.Fatalf("ack = %+v", ack)
	}
	calls := r.Calls()
	if !slices.Equal(calls[0][:3], []string{"launchctl", "bootstrap", "gui/501"}) {
		t.Fatalf("first call = %q", calls[0])
	}
	if last := calls[len(calls)-1]; last[1] != "bootout" {
		t.Fatalf("job not unloaded: %q", last)
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

// TestEmptyJob covers AC-2 and AC-3 on macOS: an ordinary job is signalled
// within its bound; a package transaction is never signalled or unloaded
// while it runs.
func TestEmptyJob(t *testing.T) {
	opts := platform.EmptyOptions{Scope: contract.ScopeRoot, Grace: 2 * time.Millisecond, KillWait: 4 * time.Millisecond, PollInterval: time.Millisecond}
	pkg := opts
	pkg.PackageTransaction = true
	tests := []struct {
		name       string
		unkillable bool
		opts       platform.EmptyOptions
		signalled  bool
		wantErr    error
	}{
		{"signalled until empty", false, opts, true, nil},
		{"cannot be emptied", true, opts, true, platform.ErrContainmentNotEmpty},
		{"package transaction never signalled", false, pkg, false, platform.ErrContainmentNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := (&launchd{unkillable: tc.unkillable}).runner()
			c := &Containment{Runner: r, PlistDir: privateDir(t)}
			err := c.Empty(context.Background(), label, tc.opts)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			var signalled, bootout bool
			for _, call := range r.Calls() {
				signalled = signalled || call[1] == "kill"
				bootout = bootout || call[1] == "bootout"
				if strings.HasPrefix(call[len(call)-1], "gui/") {
					t.Fatalf("root job addressed in a gui domain: %q", call)
				}
			}
			if signalled != tc.signalled {
				t.Fatalf("signalled = %v, want %v (calls %q)", signalled, tc.signalled, r.Calls())
			}
			if tc.wantErr != nil && bootout {
				t.Fatal("a running job was unloaded")
			}
		})
	}
}

func TestPrintFailsClosed(t *testing.T) {
	for name, res := range map[string]platform.ExecResult{
		"not loaded":  {ExitCode: launchctlNotFound},
		"other error": {ExitCode: 5},
		"unparseable": {Stdout: "???"},
		"running pid": {Stdout: "state = running\npid = 9\n"},
		"not running": {Stdout: "state = not running\n"},
	} {
		r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return res, nil })
		c := &Containment{Runner: r}
		active, err := c.Active(context.Background(), label, platform.ScopeSelector{})
		switch name {
		case "not loaded", "not running":
			if err != nil || active {
				t.Errorf("%s: Active = %v, %v; want empty", name, active, err)
			}
		case "running pid":
			if err != nil || !active {
				t.Errorf("%s: Active = %v, %v; want active", name, active, err)
			}
		default:
			if err == nil {
				t.Errorf("%s: Active = %v with no error", name, active)
			}
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
