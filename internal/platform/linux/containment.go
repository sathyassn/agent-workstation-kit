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
	activeInactive = "inactive"
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

// identityReader reads a started supervisor's identity for the launch
// acknowledgement.
type identityReader interface {
	Identity(ctx context.Context, pid int) (platform.ProcessIdentity, error)
}

// processControl is identityReader plus inspecting and signalling by
// identity; ProcessInspector provides both.
type processControl interface {
	identityReader
	platform.ProcessControl
}

// Containment runs each command in its own transient systemd scope: in the
// system manager for root, in the user's manager for a user scope. A scope
// is a cgroup, so it holds every process the tool forks, outlives the engine,
// and is emptied by name on the next start.
type Containment struct {
	Runner    platform.CommandRunner
	Inspector processControl
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
// it. The supervisor is held at a gate until onSupervisor has made its
// identity durable; systemd-run --scope then execs the tool in place, so
// the supervisor's identity is the tool's. A user scope with no user
// manager is refused, never run uncontained.
func (c *Containment) Run(ctx context.Context, spec platform.LaunchSpec, onSupervisor func(platform.LaunchAck) error) (contract.CommandResult, error) {
	if len(spec.Argv) == 0 {
		return contract.CommandResult{}, platform.ErrCommandEmpty
	}
	if !validScopeName.MatchString(spec.Name) {
		return contract.CommandResult{}, fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, spec.Name)
	}
	if err := platform.CheckToolEnv(spec.Env); err != nil {
		return contract.CommandResult{}, fmt.Errorf("scope %q: %w", spec.Name, err)
	}
	user := spec.Scope != contract.ScopeRoot
	if user && !c.hasUserManager(ctx) {
		return contract.CommandResult{}, platform.ErrNoUserManager
	}
	stdout, stderr := spec.Output, spec.Stderr
	if stderr == nil && stdout != nil {
		shared := platform.NewSyncWriter(stdout)
		stdout, stderr = shared, shared
	}
	res, err := c.Runner.Run(ctx, platform.ExecSpec{
		Argv:      SystemdRunArgv(c.bin().SystemdRun, spec.Name, user, spec.Argv),
		Env:       c.launchEnv(user, spec.Env),
		Dir:       spec.Dir,
		Stdout:    stdout,
		Stderr:    stderr,
		GateShell: c.bin().Shell,
		Gate: func(pid int) error {
			id, err := c.Inspector.Identity(ctx, pid)
			if err != nil {
				return fmt.Errorf("supervisor identity: %w", err)
			}
			if onSupervisor == nil {
				return nil
			}
			return onSupervisor(platform.LaunchAck{Name: spec.Name, Supervisor: id})
		},
	})
	out := contract.CommandResult{ExitCode: res.ExitCode, Duration: res.Duration}
	if err != nil {
		return out, fmt.Errorf("run in scope %q: %w", spec.Name, err)
	}
	return out, nil
}

// Empty empties the scope within its bound (platform.EmptyBounded), then
// clears a failed scope's record so systemd forgets it. A package-manager
// transaction is waited for and never signalled.
func (c *Containment) Empty(ctx context.Context, name string, opts platform.EmptyOptions) error {
	if !validScopeName.MatchString(name) {
		return fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, name)
	}
	sel := platform.ScopeSelector{User: opts.Scope != contract.ScopeRoot}
	if err := platform.EmptyBounded(ctx, c, c.Inspector, name, sel, opts); err != nil {
		return fmt.Errorf("empty scope: %w", err)
	}
	// Best effort: a scope with no process left holds nothing.
	_, _ = c.Runner.Run(ctx, platform.ExecSpec{Argv: c.systemctlArgv(sel, "reset-failed", name), Env: c.supervisorEnv(sel.User)})
	return nil
}

// State reads the scope's LoadState and ActiveState. A unit that is not
// loaded, or loaded with no process (inactive or failed), is gone. Any
// output it does not recognise is an error, so the caller treats the scope
// as not empty.
func (c *Containment) State(ctx context.Context, name string, sel platform.ScopeSelector) (platform.ContainmentState, error) {
	argv := c.systemctlArgv(sel, "show", "--property=LoadState,ActiveState", name)
	res, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: argv, Env: c.supervisorEnv(sel.User)})
	if err != nil {
		return 0, fmt.Errorf("systemctl show %q: %w", name, err)
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("%w: systemctl show %q exited %d", platform.ErrSupervisorState, name, res.ExitCode)
	}
	props := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	load, active := props["LoadState"], props["ActiveState"]
	switch {
	case load == "not-found" && active == activeInactive:
		return platform.StateGone, nil
	case load != "loaded":
		return 0, fmt.Errorf("%w: scope %q load state %q", platform.ErrSupervisorState, name, load)
	case active == activeInactive || active == "failed":
		return platform.StateGone, nil
	case active == "active" || active == "activating" || active == "deactivating" || active == "reloading" || active == "refreshing":
		return platform.StateActive, nil
	default:
		return 0, fmt.Errorf("%w: scope %q state %q", platform.ErrSupervisorState, name, active)
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

// launchEnv is systemd-run's whole environment for a launch. With --scope,
// systemd-run execs the tool itself, so the tool inherits this environment:
// the supervisor base plus the tool's variables, and nothing from this
// process. The base's user-manager variables always win, so a tool variable
// can never point systemd-run at another bus.
func (c *Containment) launchEnv(user bool, tool []string) []string {
	base := c.supervisorEnv(user)
	pinned := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		pinned[k] = user && (k == "XDG_RUNTIME_DIR" || k == "DBUS_SESSION_BUS_ADDRESS")
	}
	env := make([]string, 0, len(base)+len(tool))
	seen := map[string]bool{}
	for _, kv := range tool {
		k, _, _ := strings.Cut(kv, "=")
		if pinned[k] || seen[k] {
			continue
		}
		seen[k] = true
		env = append(env, kv)
	}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !seen[k] {
			env = append(env, kv)
		}
	}
	return env
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
// named transient scope. It never carries a tool variable: argv is readable
// by every local user through /proc/<pid>/cmdline.
func SystemdRunArgv(bin, name string, user bool, command []string) []string {
	argv := []string{bin}
	if user {
		argv = append(argv, flagUser)
	}
	// No --collect: a failed scope stays loaded, so its state is never
	// mistaken for a scope that never registered.
	argv = append(argv, "--scope", "--quiet", "--unit="+name, "--")
	return append(argv, command...)
}

func (c *Containment) systemctlArgv(sel platform.ScopeSelector, args ...string) []string {
	argv := []string{c.bin().Systemctl}
	if sel.User {
		argv = append(argv, flagUser)
	}
	return append(argv, args...)
}
