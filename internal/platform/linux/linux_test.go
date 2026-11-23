package linux_test

import (
	"context"
	"errors"
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
	res, err := c.Run(context.Background(), platform.LaunchSpec{Name: scope, Scope: contract.ScopeRoot, Argv: []string{"apt-get", "install"}}, func(a platform.LaunchAck) { ack = a })
	if err != nil || res.ExitCode != 7 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	want := []string{"systemd-run", "--scope", "--quiet", "--collect", "--unit=" + scope, "--", "apt-get", "install"}
	if got := r.Calls()[0]; !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if ack.Tool != (platform.ProcessIdentity{BootID: "b", PID: 4242, StartTime: "9"}) {
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
				return platform.ExecResult{Stdout: "inactive\n"}, nil
			}
			return platform.ExecResult{Stdout: "active\n"}, nil
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
		{"unknown state", platform.ExecResult{Stdout: "maintenance\n"}, nil},
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
