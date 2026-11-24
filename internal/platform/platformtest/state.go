package platformtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	hostnameFileMode    = 0o600
	cmdHostnamectlSet   = "set-hostname"
	flagStatic          = "--static"
	flagPretty          = "--pretty"
	flagTransient       = "--transient"
	flagScutilGet       = "--get"
	flagScutilSet       = "--set"
	scutilHostName      = "HostName"
	scutilComputerName  = "ComputerName"
	scutilLocalHostName = "LocalHostName"
	errUnknownScutilKey = "unknown scutil key\n"
)

// HostnameState holds the fake values read and written by HostnameRunner.
type HostnameState struct {
	Static    string `json:"static"`
	Pretty    string `json:"pretty"`
	Transient string `json:"transient"`
	Local     string `json:"local"`
}

// HostnameRunner is a fake platform.CommandRunner that emulates both Linux
// hostnamectl(1) and macOS scutil(8) hostname commands. When BackingFile is
// set, every read and write persists through that JSON file, so cross-process
// SIGKILL tests observe real mutations across processes.
type HostnameRunner struct {
	mu          sync.Mutex
	state       HostnameState
	backingFile string
	calls       [][]string
}

var _ platform.CommandRunner = (*HostnameRunner)(nil)

// NewHostnameRunner returns an in-memory fake runner seeded with initial.
func NewHostnameRunner(initial HostnameState) *HostnameRunner {
	return &HostnameRunner{state: initial}
}

// NewFileHostnameRunner returns a fake runner persisted to path. If path does
// not exist, initial is written to it.
func NewFileHostnameRunner(path string, initial HostnameState) (*HostnameRunner, error) {
	r := &HostnameRunner{state: initial, backingFile: path}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := r.saveLocked(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat hostname backing file: %w", err)
	} else {
		if err := r.loadLocked(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// State returns a snapshot of the runner's current hostname values.
func (r *HostnameRunner) State() (HostnameState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLocked(); err != nil {
		return HostnameState{}, err
	}
	return r.state, nil
}

// Calls returns every command argv received so far.
func (r *HostnameRunner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// Run emulates hostnamectl and scutil hostname operations.
func (r *HostnameRunner) Run(ctx context.Context, spec platform.ExecSpec) (platform.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return platform.ExecResult{}, fmt.Errorf("fake hostname runner: %w", err)
	}
	if len(spec.Argv) < 2 {
		return platform.ExecResult{ExitCode: 2, Stderr: "too few arguments\n"}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), spec.Argv...))
	if err := r.loadLocked(); err != nil {
		return platform.ExecResult{}, err
	}
	bin := filepath.Base(spec.Argv[0])
	switch bin {
	case "hostnamectl":
		return r.runHostnamectlLocked(spec.Argv[1:])
	case "scutil":
		return r.runScutilLocked(spec.Argv[1:])
	default:
		return platform.ExecResult{ExitCode: 127, Stderr: fmt.Sprintf("unsupported binary %q\n", bin)}, nil
	}
}

func (r *HostnameRunner) runHostnamectlLocked(args []string) (platform.ExecResult, error) {
	if len(args) == 1 {
		switch args[0] {
		case flagStatic:
			return platform.ExecResult{Stdout: r.state.Static + "\n"}, nil
		case flagPretty:
			return platform.ExecResult{Stdout: r.state.Pretty + "\n"}, nil
		case flagTransient:
			return platform.ExecResult{Stdout: r.state.Transient + "\n"}, nil
		}
	}
	if len(args) == 3 && args[0] == cmdHostnamectlSet {
		switch args[1] {
		case flagStatic:
			r.state.Static = args[2]
		case flagPretty:
			r.state.Pretty = args[2]
		case flagTransient:
			r.state.Transient = args[2]
		default:
			return platform.ExecResult{ExitCode: 1, Stderr: "unknown hostnamectl flag\n"}, nil
		}
		if err := r.saveLocked(); err != nil {
			return platform.ExecResult{}, err
		}
		return platform.ExecResult{}, nil
	}
	return platform.ExecResult{ExitCode: 1, Stderr: "unsupported hostnamectl invocation\n"}, nil
}

func (r *HostnameRunner) runScutilLocked(args []string) (platform.ExecResult, error) {
	if len(args) == 2 && args[0] == flagScutilGet {
		var val string
		switch args[1] {
		case scutilHostName:
			val = r.state.Static
		case scutilComputerName:
			val = r.state.Pretty
		case scutilLocalHostName:
			val = r.state.Local
		default:
			return platform.ExecResult{ExitCode: 1, Stderr: errUnknownScutilKey}, nil
		}
		if val == "" {
			return platform.ExecResult{ExitCode: 1, Stderr: args[1] + ": not set\n"}, nil
		}
		return platform.ExecResult{Stdout: val + "\n"}, nil
	}
	if len(args) == 3 && args[0] == flagScutilSet {
		switch args[1] {
		case scutilHostName:
			r.state.Static = args[2]
		case scutilComputerName:
			r.state.Pretty = args[2]
		case scutilLocalHostName:
			r.state.Local = args[2]
		default:
			return platform.ExecResult{ExitCode: 1, Stderr: errUnknownScutilKey}, nil
		}
		if err := r.saveLocked(); err != nil {
			return platform.ExecResult{}, err
		}
		return platform.ExecResult{}, nil
	}
	return platform.ExecResult{ExitCode: 1, Stderr: "unsupported scutil invocation\n"}, nil
}

func (r *HostnameRunner) loadLocked() error {
	if r.backingFile == "" {
		return nil
	}
	data, err := os.ReadFile(r.backingFile) //nolint:gosec // test temp file
	if err != nil {
		return fmt.Errorf("read hostname backing file: %w", err)
	}
	var st HostnameState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("decode hostname backing file: %w", err)
	}
	r.state = st
	return nil
}

func (r *HostnameRunner) saveLocked() error {
	if r.backingFile == "" {
		return nil
	}
	data, err := json.Marshal(r.state)
	if err != nil {
		return fmt.Errorf("encode hostname backing file: %w", err)
	}
	tmp := r.backingFile + ".tmp"
	if err := os.WriteFile(tmp, data, hostnameFileMode); err != nil {
		return fmt.Errorf("write hostname backing temp: %w", err)
	}
	if err := os.Rename(tmp, r.backingFile); err != nil {
		return fmt.Errorf("rename hostname backing file: %w", err)
	}
	return nil
}
