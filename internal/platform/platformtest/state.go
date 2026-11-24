package platformtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	hostnameFileMode    = 0o600
	absHostnamectlBin   = "/usr/bin/hostnamectl"
	absScutilBin        = "/usr/sbin/scutil"
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

// ErrUnexpectedInvocation reports a fake runner receiving a non-absolute
// binary path or an unexpected environment variable.
var ErrUnexpectedInvocation = errors.New("unexpected hostname tool invocation")

// HostnameState holds the fake values read and written by HostnameRunner.
type HostnameState struct {
	Static        string `json:"static"`
	Pretty        string `json:"pretty"`
	Transient     string `json:"transient"`
	LocalHostName string `json:"local"`
}

// HostnameRunner is a fake platform.CommandRunner that emulates both Linux
// hostnamectl(1) and macOS scutil(8) hostname commands. It verifies that
// tools are invoked by trusted absolute path (/usr/bin/hostnamectl or
// /usr/sbin/scutil) with a minimal environment (PATH and LANG only). When
// backingFile is set, reads and writes persist through that JSON file so
// cross-process SIGKILL tests observe mutations across processes.
type HostnameRunner struct {
	mu          sync.Mutex
	state       HostnameState
	backingFile string
	calls       [][]string
	envs        [][]string
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

// Envs returns every command environment received so far.
func (r *HostnameRunner) Envs() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.envs))
	for i, e := range r.envs {
		out[i] = append([]string(nil), e...)
	}
	return out
}

func checkMinimalToolEnv(env []string) error {
	if err := platform.CheckToolEnv(env); err != nil {
		return fmt.Errorf("%w: %w", ErrUnexpectedInvocation, err)
	}
	var hasPath, hasLang bool
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("%w: malformed env entry %q", ErrUnexpectedInvocation, kv)
		}
		switch k {
		case "PATH":
			hasPath = true
		case "LANG":
			hasLang = true
		default:
			return fmt.Errorf("%w: unexpected env key %q", ErrUnexpectedInvocation, k)
		}
	}
	if !hasPath || !hasLang {
		return fmt.Errorf("%w: env %v must set PATH and LANG", ErrUnexpectedInvocation, env)
	}
	return nil
}

// Run emulates hostnamectl and scutil hostname operations after checking
// Argv[0] is an exact trusted absolute path and Env is minimal.
func (r *HostnameRunner) Run(ctx context.Context, spec platform.ExecSpec) (platform.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return platform.ExecResult{}, fmt.Errorf("fake hostname runner: %w", err)
	}
	if len(spec.Argv) < 2 {
		return platform.ExecResult{ExitCode: 2, Stderr: "too few arguments\n"}, nil
	}
	if err := checkMinimalToolEnv(spec.Env); err != nil {
		return platform.ExecResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), spec.Argv...))
	r.envs = append(r.envs, append([]string(nil), spec.Env...))
	if err := r.loadLocked(); err != nil {
		return platform.ExecResult{}, err
	}
	switch spec.Argv[0] {
	case absHostnamectlBin:
		return r.runHostnamectlLocked(spec.Argv[1:])
	case absScutilBin:
		return r.runScutilLocked(spec.Argv[1:])
	default:
		return platform.ExecResult{}, fmt.Errorf("%w: binary %q must be %q or %q",
			ErrUnexpectedInvocation, spec.Argv[0], absHostnamectlBin, absScutilBin)
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
			val = r.state.LocalHostName
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
			r.state.LocalHostName = args[2]
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
