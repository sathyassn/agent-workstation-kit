package linux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	systemdRunBin = "systemd-run"
	systemctlBin  = "systemctl"
	flagUser      = "--user"
	scopePrefix   = "wskit-run-"
	scopeSuffix   = ".scope"
	envRuntimeDir = "XDG_RUNTIME_DIR"
)

// validScopeName matches the names NameFor makes; nothing else is passed to
// systemctl. It is compiled once and never changed.
var validScopeName = regexp.MustCompile(`^wskit-run-[a-z0-9][a-z0-9_-]{0,63}-[0-9]{1,6}\.scope$`)

// identityReader reads a started tool's identity for the launch
// acknowledgement.
type identityReader interface {
	Identity(ctx context.Context, pid int) (platform.ProcessIdentity, error)
}

// Containment runs each command in its own transient systemd scope: in the
// system manager for root, in the user's manager for a user scope. A scope
// is a cgroup, so it holds every process the tool forks, outlives the engine,
// and is emptied by name on the next start.
type Containment struct {
	Runner    platform.CommandRunner
	Inspector identityReader
	// UserManager reports whether a user service manager runs. Nil checks
	// for $XDG_RUNTIME_DIR/bus.
	UserManager func(ctx context.Context) bool
}

// NewContainment returns an adapter that runs the real systemd tools.
func NewContainment() *Containment {
	return &Containment{Runner: platform.OSCommandRunner{}, Inspector: NewProcessInspector()}
}

// NameFor returns the scope name for the seq-th command of a run.
func (c *Containment) NameFor(runID contract.RunID, seq int) (string, error) {
	if err := platform.ValidateRunID(runID); err != nil {
		return "", fmt.Errorf("linux scope name: %w", err)
	}
	name := scopePrefix + string(runID) + "-" + strconv.Itoa(seq) + scopeSuffix
	if !validScopeName.MatchString(name) {
		return "", fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, name)
	}
	return name, nil
}

// Run starts spec.Argv in a new transient scope named spec.Name and waits for
// it. A user scope with no user manager is refused, never run uncontained.
// onAck receives the tool's identity once it has started.
func (c *Containment) Run(ctx context.Context, spec platform.LaunchSpec, onAck func(platform.LaunchAck)) (contract.CommandResult, error) {
	if len(spec.Argv) == 0 {
		return contract.CommandResult{}, platform.ErrCommandEmpty
	}
	if !validScopeName.MatchString(spec.Name) {
		return contract.CommandResult{}, fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, spec.Name)
	}
	user := spec.Scope != contract.ScopeRoot
	env := append([]string{}, spec.Env...)
	if user {
		if !c.hasUserManager(ctx) {
			return contract.CommandResult{}, platform.ErrNoUserManager
		}
		// systemd-run --user finds the user bus through this variable.
		env = append(env, envRuntimeDir+"="+os.Getenv(envRuntimeDir))
	}
	res, err := c.Runner.Run(ctx, platform.ExecSpec{
		Argv:   SystemdRunArgv(spec.Name, user, spec.Argv),
		Env:    env,
		Dir:    spec.Dir,
		Stdout: spec.Output,
		Stderr: spec.Output,
		OnStart: func(pid int) {
			ack := platform.LaunchAck{Name: spec.Name}
			// systemd-run --scope execs the tool in place, so pid is the
			// tool. If it has already exited, the identity stays zero.
			if id, idErr := c.Inspector.Identity(ctx, pid); idErr == nil {
				ack.Tool = id
			}
			if onAck != nil {
				onAck(ack)
			}
		},
	})
	out := contract.CommandResult{ExitCode: res.ExitCode, Duration: res.Duration}
	if err != nil {
		return out, fmt.Errorf("run in scope %q: %w", spec.Name, err)
	}
	return out, nil
}

// Empty empties the scope within its bound (platform.EmptyBounded). A
// package-manager transaction is waited for and never signalled.
func (c *Containment) Empty(ctx context.Context, name string, opts platform.EmptyOptions) error {
	if !validScopeName.MatchString(name) {
		return fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, name)
	}
	sel := platform.ScopeSelector{User: opts.Scope != contract.ScopeRoot}
	if err := platform.EmptyBounded(ctx, c, name, sel, opts); err != nil {
		return fmt.Errorf("empty scope: %w", err)
	}
	return nil
}

// Active reports whether the scope holds processes, from its ActiveState.
// A scope that never existed or was collected reports "inactive". Any state
// or tool failure it does not recognise is an error, so the caller treats
// the scope as not empty.
func (c *Containment) Active(ctx context.Context, name string, sel platform.ScopeSelector) (bool, error) {
	argv := systemctlArgv(sel, "show", "--property=ActiveState", "--value", name)
	res, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: argv})
	if err != nil {
		return false, fmt.Errorf("systemctl show %q: %w", name, err)
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("%w: systemctl show %q exited %d", platform.ErrSupervisorState, name, res.ExitCode)
	}
	switch state := strings.TrimSpace(res.Stdout); state {
	case "inactive", "failed":
		return false, nil
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true, nil
	default:
		return false, fmt.Errorf("%w: scope %q state %q", platform.ErrSupervisorState, name, state)
	}
}

// Signal sends sig to every process in the scope. A non-zero exit, such as
// for a scope that just ended, is not an error: the next Active check
// decides.
func (c *Containment) Signal(ctx context.Context, name string, sel platform.ScopeSelector, sig syscall.Signal) error {
	sigName, ok := map[syscall.Signal]string{syscall.SIGTERM: "SIGTERM", syscall.SIGKILL: "SIGKILL"}[sig]
	if !ok {
		return fmt.Errorf("%w: signal %v", platform.ErrSupervisorState, sig)
	}
	argv := systemctlArgv(sel, "kill", "--signal="+sigName, name)
	if _, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: argv}); err != nil {
		return fmt.Errorf("systemctl kill %q: %w", name, err)
	}
	return nil
}

func (c *Containment) hasUserManager(ctx context.Context) bool {
	if c.UserManager != nil {
		return c.UserManager(ctx)
	}
	dir := os.Getenv(envRuntimeDir)
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "bus")) //nolint:gosec // only stats the user bus socket; nothing is read or written
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// SystemdRunArgv is the systemd-run command line that starts command in the
// named transient scope.
func SystemdRunArgv(name string, user bool, command []string) []string {
	argv := []string{systemdRunBin}
	if user {
		argv = append(argv, flagUser)
	}
	argv = append(argv, "--scope", "--quiet", "--collect", "--unit="+name, "--")
	return append(argv, command...)
}

func systemctlArgv(sel platform.ScopeSelector, args ...string) []string {
	argv := []string{systemctlBin}
	if sel.User {
		argv = append(argv, flagUser)
	}
	return append(argv, args...)
}
