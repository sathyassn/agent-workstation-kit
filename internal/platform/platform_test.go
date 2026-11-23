package platform_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// fakeControl is a containment that empties after a given number of Active
// checks once it receives a given signal.
type fakeControl struct {
	mu        sync.Mutex
	active    bool
	emptyOn   syscall.Signal // the signal that ends the processes; 0 never
	selfEnds  int            // Active checks after which the processes end by themselves; 0 never
	checks    int
	signals   []syscall.Signal
	activeErr error
}

func (f *fakeControl) Active(context.Context, string, platform.ScopeSelector) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activeErr != nil {
		return false, f.activeErr
	}
	f.checks++
	if f.selfEnds > 0 && f.checks >= f.selfEnds {
		f.active = false
	}
	return f.active, nil
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
		{"state unreadable", &fakeControl{active: true, activeErr: errBus}, opts, nil, errBus},
		{"package transaction ends while waited for", &fakeControl{active: true, selfEnds: 3, emptyOn: syscall.SIGTERM}, pkg, nil, nil},
		{"package transaction outlasts the bound", &fakeControl{active: true, emptyOn: syscall.SIGTERM}, pkg, nil, platform.ErrContainmentNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.EmptyBounded(context.Background(), tc.ctl, "c", platform.ScopeSelector{}, tc.opts)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(tc.ctl.signals) != len(tc.signals) {
				t.Fatalf("signals = %v, want %v", tc.ctl.signals, tc.signals)
			}
			for i := range tc.signals {
				if tc.ctl.signals[i] != tc.signals[i] {
					t.Fatalf("signals = %v, want %v", tc.ctl.signals, tc.signals)
				}
			}
		})
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
