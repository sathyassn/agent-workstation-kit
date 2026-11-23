package linux_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/linux"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

const scope = "wskit-run-r1-1.scope"

func procFixture(t *testing.T, boot string, procs map[int]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys", "kernel", "random"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sys", "kernel", "random", "boot_id"), []byte(boot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for pid, start := range procs {
		dir := filepath.Join(root, itoa(pid))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		fields := strings.Repeat("0 ", 18) + start + " 0 0"
		stat := itoa(pid) + " (tool (x) y) S " + fields
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestParseProcStatStartTime(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"1 (a) S " + strings.Repeat("0 ", 18) + "777 0", "777", true},
		{"1 (a b) c)) S " + strings.Repeat("0 ", 18) + "778 0", "778", true},
		{"1 a S 0", "", false},
		{"1 (a) S 0 0", "", false},
		{"1 (a) S " + strings.Repeat("0 ", 18) + "x 0", "", false},
	}
	for _, tc := range tests {
		got, err := linux.ParseProcStatStartTime(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseProcStatStartTime(%q) = %q, %v", tc.in, got, err)
		}
	}
}

// TestInspectorAlive is the Linux half of AC-2's identity check.
func TestInspectorAlive(t *testing.T) {
	root := procFixture(t, "boot-1", map[int]string{100: "5000"})
	p := &linux.ProcessInspector{ProcRoot: root}
	tests := []struct {
		name string
		id   platform.ProcessIdentity
		want bool
	}{
		{"same process", platform.ProcessIdentity{BootID: "boot-1", PID: 100, StartTime: "5000"}, true},
		{"pid reused", platform.ProcessIdentity{BootID: "boot-1", PID: 100, StartTime: "4000"}, false},
		{"earlier boot", platform.ProcessIdentity{BootID: "boot-0", PID: 100, StartTime: "5000"}, false},
		{"gone", platform.ProcessIdentity{BootID: "boot-1", PID: 101, StartTime: "5000"}, false},
	}
	for _, tc := range tests {
		got, err := p.Alive(context.Background(), tc.id)
		if err != nil || got != tc.want {
			t.Errorf("%s: Alive = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := p.Alive(context.Background(), platform.ProcessIdentity{}); !errors.Is(err, platform.ErrInvalidProcessIdentity) {
		t.Errorf("invalid identity err = %v", err)
	}
	// An unreadable stat is an error, never "dead".
	if err := os.WriteFile(filepath.Join(root, "100", "stat"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Alive(context.Background(), platform.ProcessIdentity{BootID: "boot-1", PID: 100, StartTime: "5000"}); err == nil {
		t.Error("garbage stat read as a dead process")
	}
}

func TestNameFor(t *testing.T) {
	c := &linux.Containment{}
	if n, err := c.NameFor("r1", 1); err != nil || n != scope {
		t.Fatalf("NameFor = %q, %v", n, err)
	}
	if _, err := c.NameFor("../x", 1); !errors.Is(err, platform.ErrInvalidContainmentName) {
		t.Fatalf("bad run ID err = %v", err)
	}
}

func TestRunStartsTransientScope(t *testing.T) {
	r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return platform.ExecResult{ExitCode: 7}, nil })
	c := &linux.Containment{Runner: r, Inspector: &linux.ProcessInspector{ProcRoot: procFixture(t, "b", map[int]string{4242: "9"})}}
	var ack platform.LaunchAck
	res, err := c.Run(context.Background(), platform.LaunchSpec{Name: scope, Scope: contract.ScopeRoot, Argv: []string{"apt-get", "install"}}, func(a platform.LaunchAck) error { ack = a; return nil })
	if err != nil || res.ExitCode != 7 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	want := []string{"/usr/bin/systemd-run", "--scope", "--quiet", "--unit=" + scope, "--", "apt-get", "install"}
	if got := r.Calls()[0]; !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if ack.Supervisor != (platform.ProcessIdentity{BootID: "b", PID: 4242, StartTime: "9"}) {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestRunRefusesUserScopeWithoutManager: no user manager means no
// containment, so nothing runs.
func TestRunRefusesUserScopeWithoutManager(t *testing.T) {
	r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return platform.ExecResult{}, nil })
	c := &linux.Containment{Runner: r, UserManager: func(context.Context) bool { return false }}
	_, err := c.Run(context.Background(), platform.LaunchSpec{Name: scope, Scope: contract.ScopeUser, Argv: []string{"true"}}, nil)
	if !errors.Is(err, platform.ErrNoUserManager) || len(r.Calls()) != 0 {
		t.Fatalf("err = %v, calls = %v", err, r.Calls())
	}
}

// scopeRunner answers systemctl as a scope that stays active until its
// SIGKILL, or forever when unkillable.
func scopeRunner(unkillable bool) *platformtest.Runner {
	var killed atomic.Bool
	return platformtest.NewRunner(func(argv []string) (platform.ExecResult, error) {
		switch {
		case slices.Contains(argv, "kill"):
			if slices.Contains(argv, "--signal=SIGKILL") && !unkillable {
				killed.Store(true)
			}
			return platform.ExecResult{}, nil
		case slices.Contains(argv, "show"):
			if killed.Load() {
				return platform.ExecResult{Stdout: "LoadState=loaded\nActiveState=failed\n"}, nil
			}
			return platform.ExecResult{Stdout: "LoadState=loaded\nActiveState=active\n"}, nil
		}
		return platform.ExecResult{ExitCode: 1}, nil
	})
}

func kills(r *platformtest.Runner) [][]string {
	var out [][]string
	for _, c := range r.Calls() {
		if slices.Contains(c, "kill") {
			out = append(out, c)
		}
	}
	return out
}

// TestEmptyScope covers AC-2 and AC-3 on Linux: an ordinary scope is
// terminated then killed within its bound; a package transaction is only
// waited for and never signalled.
func TestEmptyScope(t *testing.T) {
	opts := platform.EmptyOptions{Scope: contract.ScopeRoot, Grace: 2 * time.Millisecond, KillWait: 4 * time.Millisecond, PollInterval: time.Millisecond}
	pkg := opts
	pkg.PackageTransaction = true
	tests := []struct {
		name       string
		unkillable bool
		opts       platform.EmptyOptions
		kills      int
		wantErr    error
	}{
		{"terminated then killed", false, opts, 2, nil},
		{"cannot be emptied", true, opts, 2, platform.ErrContainmentNotEmpty},
		{"package transaction never signalled", false, pkg, 0, platform.ErrContainmentNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := scopeRunner(tc.unkillable)
			c := &linux.Containment{Runner: r}
			err := c.Empty(context.Background(), scope, tc.opts)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got := kills(r); len(got) != tc.kills {
				t.Fatalf("kill calls = %q, want %d", got, tc.kills)
			}
			if slices.Contains(r.Calls()[0], "--user") {
				t.Fatal("root scope checked in the user manager")
			}
		})
	}
}

// TestActiveFailsClosed: a state or tool failure it does not recognise is an
// error, so the caller treats the scope as not empty.
func TestActiveFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		res  platform.ExecResult
		err  error
	}{
		{"unknown state", platform.ExecResult{Stdout: "LoadState=loaded\nActiveState=maintenance\n"}, nil},
		{"unknown load state", platform.ExecResult{Stdout: "LoadState=masked\nActiveState=inactive\n"}, nil},
		{"systemctl failed", platform.ExecResult{ExitCode: 1}, nil},
		{"systemctl missing", platform.ExecResult{}, errors.New("exec: not found")},
	}
	for _, tc := range tests {
		r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return tc.res, tc.err })
		c := &linux.Containment{Runner: r}
		if err := c.Empty(context.Background(), scope, platform.EmptyOptions{}); err == nil {
			t.Errorf("%s: Empty succeeded", tc.name)
		}
		if len(kills(r)) != 0 {
			t.Errorf("%s: signalled a scope in unknown state", tc.name)
		}
	}
	if err := (&linux.Containment{}).Empty(context.Background(), "ssh.service", platform.EmptyOptions{}); !errors.Is(err, platform.ErrInvalidContainmentName) {
		t.Errorf("foreign unit name err = %v", err)
	}
}

// TestSupervisorIgnoresHostilePath is finding 2: a systemctl planted first
// on PATH is never run; the adapter runs its fixed absolute path.
func TestSupervisorIgnoresHostilePath(t *testing.T) {
	hostile := t.TempDir()
	marker := filepath.Join(hostile, "ran")
	script := "#!/bin/sh\ntouch " + marker + "\necho ActiveState=inactive\necho LoadState=not-found\n"
	for _, name := range []string{"systemctl", "systemd-run", "sh"} {
		if err := os.WriteFile(filepath.Join(hostile, name), []byte(script), 0o700); err != nil { //nolint:gosec // test executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", hostile)
	c := &linux.Containment{Runner: platform.OSCommandRunner{}}
	_ = c.Empty(context.Background(), scope, platform.EmptyOptions{})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a supervisor binary was found through PATH")
	}
}

// TestToolVariablesNeverInSupervisorArgv is decision d: /proc/<pid>/cmdline
// is readable by every local user, so no declared value may appear in
// systemd-run's argv, for a root or a user scope. systemd-run --scope execs
// the tool itself, so the declared variables travel in systemd-run's own
// environment: the fixed base plus the tool's variables and nothing else.
func TestToolVariablesNeverInSupervisorArgv(t *testing.T) {
	uid := os.Getuid()
	root, err := os.MkdirTemp("", "ru") // short: a socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, strconv.Itoa(uid))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("WSKIT_CALLER_ONLY", "leak")
	const secret = "s3cret-value"
	for _, sc := range []contract.Scope{contract.ScopeRoot, contract.ScopeUser} {
		t.Run(string(sc), func(t *testing.T) {
			r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return platform.ExecResult{}, nil })
			c := &linux.Containment{Runner: r, Inspector: &linux.ProcessInspector{ProcRoot: procFixture(t, "b", map[int]string{4242: "9"})}, UID: uid, RunUserRoot: root}
			spec := platform.LaunchSpec{Name: scope, Scope: sc, Argv: []string{"/usr/bin/true"}, Env: []string{"WSKIT_TOKEN=" + secret, "PATH=/usr/bin"}}
			if _, err := c.Run(context.Background(), spec, nil); err != nil {
				t.Fatal(err)
			}
			argv, env := r.Calls()[0], r.Envs()[0]
			for _, a := range argv {
				if strings.Contains(a, secret) || strings.HasPrefix(a, "--setenv") {
					t.Fatalf("argv %q carries a tool variable", argv)
				}
			}
			if !slices.Contains(env, "WSKIT_TOKEN="+secret) {
				t.Fatalf("supervisor env %q lacks the declared variable", env)
			}
			for _, kv := range env {
				k, _, _ := strings.Cut(kv, "=")
				switch k {
				case "PATH", "LANG", "WSKIT_TOKEN":
				case "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS":
					if sc == contract.ScopeRoot || !strings.Contains(kv, dir) {
						t.Fatalf("supervisor env holds %s", kv)
					}
				default:
					t.Fatalf("supervisor env holds %s, outside the base and the declared keys", kv)
				}
			}
		})
	}
}

// TestRefusesLoaderAndSystemdVariables is decision d: a variable that would
// steer the dynamic loader or systemd-run itself is refused before
// anything runs.
func TestRefusesLoaderAndSystemdVariables(t *testing.T) {
	for _, kv := range []string{"LD_PRELOAD=/tmp/x.so", "LD_LIBRARY_PATH=/tmp", "DYLD_INSERT_LIBRARIES=/tmp/x", "SYSTEMD_LOG_LEVEL=debug"} {
		r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return platform.ExecResult{}, nil })
		c := &linux.Containment{Runner: r, UserManager: func(context.Context) bool { return true }}
		_, err := c.Run(context.Background(), platform.LaunchSpec{Name: scope, Scope: contract.ScopeRoot, Argv: []string{"/usr/bin/true"}, Env: []string{kv}}, nil)
		if !errors.Is(err, platform.ErrForbiddenEnv) || len(r.Calls()) != 0 {
			t.Errorf("%s: err = %v, calls = %d; want ErrForbiddenEnv and nothing run", kv, err, len(r.Calls()))
		}
	}
}

// TestUserManagerFoundOnlyInRunUser: the user bus is taken from
// RunUserRoot/<uid>, owned by that uid; a folder owned by someone else, or
// writable by others, is not a user manager.
func TestUserManagerFoundOnlyInRunUser(t *testing.T) {
	root, err := os.MkdirTemp("", "ru") // short: a socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	uid := os.Getuid()
	dir := filepath.Join(root, strconv.Itoa(uid))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	r := platformtest.NewRunner(func([]string) (platform.ExecResult, error) { return platform.ExecResult{}, nil })
	run := func(c *linux.Containment) error {
		_, err := c.Run(context.Background(), platform.LaunchSpec{Name: scope, Scope: contract.ScopeUser, Argv: []string{"/usr/bin/true"}}, nil)
		return err
	}
	if err := run(&linux.Containment{Runner: r, Inspector: &linux.ProcessInspector{ProcRoot: procFixture(t, "b", map[int]string{4242: "9"})}, UID: uid, RunUserRoot: root}); err != nil {
		t.Fatalf("own runtime folder refused: %v", err)
	}
	env := r.Envs()[0]
	if !slices.Contains(env, "XDG_RUNTIME_DIR="+dir) || !slices.Contains(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(dir, "bus")) {
		t.Fatalf("supervisor env = %q", env)
	}
	if err := run(&linux.Containment{Runner: r, UID: uid + 1, RunUserRoot: root}); !errors.Is(err, platform.ErrNoUserManager) {
		t.Fatalf("another uid's folder: err = %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // deliberately unsafe folder under test
		t.Fatal(err)
	}
	if err := run(&linux.Containment{Runner: r, UID: uid, RunUserRoot: root}); !errors.Is(err, platform.ErrNoUserManager) {
		t.Fatalf("folder writable by others: err = %v", err)
	}
}

// TestRefusedGateRunsNothing: when the supervisor's identity cannot be made
// durable, the gate stays shut and systemd-run never runs.
func TestRefusedGateRunsNothing(t *testing.T) {
	table := platformtest.NewProcessTable("b")
	sd := platformtest.NewSystemd(table)
	ran := false
	sd.SetTool(func(context.Context, *platformtest.Scope) int { ran = true; return 0 })
	c := &linux.Containment{Runner: sd, Inspector: table}
	_, err := c.Run(context.Background(), platform.LaunchSpec{Name: scope, Scope: contract.ScopeRoot, Argv: []string{"x"}},
		func(platform.LaunchAck) error { return errors.New("disk full") })
	if !errors.Is(err, platform.ErrGateClosed) || ran || sd.Loaded(scope) {
		t.Fatalf("err = %v, ran = %v, loaded = %v", err, ran, sd.Loaded(scope))
	}
}
