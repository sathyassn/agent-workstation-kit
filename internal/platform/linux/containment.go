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
	flagUser       = "--user"
	scopePrefix    = "wskit-run-"
	scopeSuffix    = ".scope"
	defaultRunUser = "/run/user"
	supervisorPath = "PATH=/usr/sbin:/usr/bin:/sbin:/bin"
)

// Binaries are the absolute paths the adapter executes. Nothing is looked
// up through PATH.
type Binaries struct {
	SystemdRun string
	Systemctl  string
	Shell      string
}

// DefaultBinaries are the Ubuntu paths of the supervisor tools.
func DefaultBinaries() Binaries {
	return Binaries{SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl", Shell: "/bin/sh"}
}

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
	// Bin are the supervisor binaries; empty fields take DefaultBinaries.
	Bin Binaries
	// UID owns the user manager for a user scope.
	UID int
	// RunUserRoot holds each user's runtime folder; empty is /run/user.
	RunUserRoot string
	// UserManager reports whether a user service manager runs. Nil checks
	// for a bus socket in RunUserRoot/<UID>, owned by UID.
	UserManager func(ctx context.Context) bool
}

// NewContainment returns an adapter that runs the real systemd tools, after
// checking that each binary is owned by root and not writable by others.
func NewContainment() (*Containment, error) {
	bin := DefaultBinaries()
	for _, b := range []string{bin.SystemdRun, bin.Systemctl, bin.Shell} {
		if err := platform.CheckTrustedBinary(b); err != nil {
			return nil, fmt.Errorf("linux containment: %w", err)
		}
	}
	return &Containment{Runner: platform.OSCommandRunner{}, Inspector: NewProcessInspector(), Bin: bin, UID: os.Getuid()}, nil
}

func (c *Containment) bin() Binaries {
	b, d := c.Bin, DefaultBinaries()
	if b.SystemdRun == "" {
		b.SystemdRun = d.SystemdRun
	}
	if b.Systemctl == "" {
		b.Systemctl = d.Systemctl
	}
	if b.Shell == "" {
		b.Shell = d.Shell
	}
	return b
}

func (c *Containment) runtimeDir() string {
	root := c.RunUserRoot
	if root == "" {
		root = defaultRunUser
	}
	return filepath.Join(root, strconv.Itoa(c.UID))
}

// supervisorEnv is the whole environment systemd-run and systemctl get: a
// fixed PATH, and for a user manager its runtime folder and bus.
func (c *Containment) supervisorEnv(user bool) []string {
	env := []string{supervisorPath, "LANG=C"}
	if user {
		dir := c.runtimeDir()
		env = append(env, "XDG_RUNTIME_DIR="+dir, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(dir, "bus"))
	}
	return env
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
	if user && !c.hasUserManager(ctx) {
		return contract.CommandResult{}, platform.ErrNoUserManager
	}
	res, err := c.Runner.Run(ctx, platform.ExecSpec{
		Argv:   SystemdRunArgv(c.bin().SystemdRun, spec.Name, user, spec.Env, spec.Argv),
		Env:    c.supervisorEnv(user),
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
	argv := c.systemctlArgv(sel, "show", "--property=ActiveState", "--value", name)
	res, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: argv, Env: c.supervisorEnv(sel.User)})
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
	argv := c.systemctlArgv(sel, "kill", "--signal="+sigName, name)
	if _, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: argv, Env: c.supervisorEnv(sel.User)}); err != nil {
		return fmt.Errorf("systemctl kill %q: %w", name, err)
	}
	return nil
}

// hasUserManager checks for the user manager's bus in /run/user/<uid>: the
// folder and the socket must be owned by that uid and the folder must not be
// writable by others. The caller's XDG_RUNTIME_DIR is never trusted.
func (c *Containment) hasUserManager(ctx context.Context) bool {
	if c.UserManager != nil {
		return c.UserManager(ctx)
	}
	dir := c.runtimeDir()
	for _, p := range []string{dir, filepath.Join(dir, "bus")} {
		info, err := os.Lstat(p)
		if err != nil {
			return false
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != c.UID {
			return false
		}
		if p == dir && (!info.IsDir() || info.Mode().Perm()&0o022 != 0) {
			return false
		}
		if p != dir && info.Mode()&os.ModeSocket == 0 {
			return false
		}
	}
	return true
}

// SystemdRunArgv is the systemd-run command line that starts command in the
// named transient scope. The tool's variables go in as --setenv, so the
// supervisor's own environment stays minimal.
func SystemdRunArgv(bin, name string, user bool, env, command []string) []string {
	argv := []string{bin}
	if user {
		argv = append(argv, flagUser)
	}
	argv = append(argv, "--scope", "--quiet", "--collect", "--unit="+name)
	for _, kv := range env {
		argv = append(argv, "--setenv="+kv)
	}
	argv = append(argv, "--")
	return append(argv, command...)
}

func (c *Containment) systemctlArgv(sel platform.ScopeSelector, args ...string) []string {
	argv := []string{c.bin().Systemctl}
	if sel.User {
		argv = append(argv, flagUser)
	}
	return append(argv, args...)
}
