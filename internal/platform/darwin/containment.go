package darwin

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	// launchctlBin is run by absolute path; nothing is looked up in PATH.
	launchctlBin  = "/bin/launchctl"
	shellBin      = "/bin/sh"
	labelPrefix   = "dev.agent-workstation-kit.run."
	domainSystem  = "system"
	domainGUIPref = "gui/"
	// launchctlNotFound is launchctl's exit status for a service that is
	// not loaded.
	launchctlNotFound   = 113
	defaultPollInterval = 25 * time.Millisecond
	defaultAckTimeout   = 10 * time.Second
	plistMode           = 0o600
	stateNotRunning     = "not running"
)

// ErrInvalidEnv reports an environment entry without a name. It is a
// sentinel for errors.Is and is never reassigned.
var ErrInvalidEnv = errors.New("environment entry has no name")

// launchctlEnv is the whole environment launchctl gets. The tool's own
// variables are in the job definition, never in launchctl's environment.
func launchctlEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"} }

// Patterns compiled once and never changed.
var (
	validLabel   = regexp.MustCompile(`^dev\.agent-workstation-kit\.run\.[a-z0-9][a-z0-9_-]{0,63}\.[0-9]{1,6}$`)
	printPID     = regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)\s*$`)
	printState   = regexp.MustCompile(`(?m)^\s*state = ([a-z ]+?)\s*$`)
	printLastRun = regexp.MustCompile(`(?m)^\s*last exit code = ([0-9]+)\s*$`)
)

// processControl reads a supervisor's identity and inspects and signals it
// by identity; ProcessInspector provides it.
type processControl interface {
	Identity(ctx context.Context, pid int) (platform.ProcessIdentity, error)
	platform.ProcessControl
}

// Containment runs each command as its own launchd job, in the system domain
// for root and the user's gui domain otherwise. launchd owns the job, so it
// outlives the engine and is emptied by label on the next start.
type Containment struct {
	Runner    platform.CommandRunner
	Inspector processControl
	// PlistDir holds the job definitions and output files. It must be a
	// private folder (platform.CheckPrivateDir); Run refuses anything else.
	PlistDir string
	// UID selects the gui/<uid> domain for a user scope.
	UID int
	// AckTimeout bounds the wait for launchd to start the job. Zero means
	// ten seconds.
	AckTimeout time.Duration
	// PollInterval is how often the job state is read. Zero means 25ms.
	PollInterval time.Duration
	// CallTimeout bounds each launchctl call. Zero means ten seconds.
	CallTimeout time.Duration
	// syncFn fsyncs a file or folder; tests replace it to observe order.
	syncFn func(*os.File) error
}

// NameFor returns the launchd label for the seq-th command of a run.
func (c *Containment) NameFor(runID contract.RunID, seq int) (string, error) {
	if err := platform.ValidateRunID(runID); err != nil {
		return "", fmt.Errorf("launchd label: %w", err)
	}
	name := labelPrefix + string(runID) + "." + strconv.Itoa(seq)
	if !validLabel.MatchString(name) {
		return "", fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, name)
	}
	return name, nil
}

// Run loads spec as a launchd job and waits for it to end. The launchctl
// bootstrap is held at a gate until onSupervisor has made its identity
// durable, so a restart can always tell whether a bootstrap may still be
// registering the job. Run never unloads the job: Empty proves it quiet and
// unloads it.
func (c *Containment) Run(ctx context.Context, spec platform.LaunchSpec, onSupervisor func(platform.LaunchAck) error) (contract.CommandResult, error) {
	if len(spec.Argv) == 0 {
		return contract.CommandResult{}, platform.ErrCommandEmpty
	}
	if !validLabel.MatchString(spec.Name) {
		return contract.CommandResult{}, fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, spec.Name)
	}
	if err := platform.CheckToolEnv(spec.Env); err != nil {
		return contract.CommandResult{}, fmt.Errorf("launchd job %q: %w", spec.Name, err)
	}
	dir, err := c.openJobDir()
	if err != nil {
		return contract.CommandResult{}, err
	}
	defer func() { _ = dir.Close() }()
	start := time.Now()
	files := c.jobFiles(spec.Name)
	errPath := files.out
	if spec.Stderr != nil {
		errPath = files.errOut
	}
	plist, err := RenderPlist(spec.Name, spec.Argv, spec.Env, spec.Dir, files.out, errPath)
	if err != nil {
		return contract.CommandResult{}, err
	}
	// The output files are created here, 0600, so launchd never creates them
	// with a mode of its own choosing.
	if err := c.writeDurable(dir, filepath.Base(files.out), nil); err != nil {
		return contract.CommandResult{}, err
	}
	if spec.Stderr != nil {
		if err := c.writeDurable(dir, filepath.Base(files.errOut), nil); err != nil {
			return contract.CommandResult{}, err
		}
	}
	if err := c.writeDurable(dir, filepath.Base(files.plist), plist); err != nil {
		return contract.CommandResult{}, err
	}
	sel := platform.ScopeSelector{User: spec.Scope != contract.ScopeRoot}
	target := c.target(spec.Name, sel)
	res, err := c.Runner.Run(ctx, platform.ExecSpec{
		Argv:      []string{launchctlBin, "bootstrap", c.domain(sel), files.plist},
		Env:       launchctlEnv(),
		GateShell: shellBin,
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
	if err != nil {
		return contract.CommandResult{}, fmt.Errorf("launchctl bootstrap %s: %w", target, err)
	}
	if res.ExitCode != 0 {
		return contract.CommandResult{}, fmt.Errorf("%w: launchctl bootstrap %s exited %d", platform.ErrLaunchNotAcknowledged, target, res.ExitCode)
	}
	st, err := c.waitStarted(ctx, target)
	if err != nil {
		return contract.CommandResult{}, err
	}
	for st.state == platform.StateActive {
		if err := c.sleep(ctx); err != nil {
			return contract.CommandResult{}, fmt.Errorf("wait for job %s: %w", target, err)
		}
		if st, err = c.print(ctx, target); err != nil {
			return contract.CommandResult{}, err
		}
	}
	if !st.exited {
		return contract.CommandResult{}, fmt.Errorf("%w: job %s ended without an exit code", platform.ErrSupervisorState, target)
	}
	copyOutput(dir, filepath.Base(files.out), spec.Output)
	if spec.Stderr != nil {
		copyOutput(dir, filepath.Base(files.errOut), spec.Stderr)
	}
	return contract.CommandResult{ExitCode: st.exitCode, Duration: time.Since(start)}, nil
}

// Empty empties the job within its bound (platform.EmptyBounded), then
// unloads it and verifies launchd no longer knows it. A package-manager
// transaction is waited for and never signalled or unloaded while it runs.
func (c *Containment) Empty(ctx context.Context, name string, opts platform.EmptyOptions) error {
	if !validLabel.MatchString(name) {
		return fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, name)
	}
	sel := platform.ScopeSelector{User: opts.Scope != contract.ScopeRoot}
	if err := platform.EmptyBounded(ctx, c, c.Inspector, name, sel, opts); err != nil {
		return fmt.Errorf("empty launchd job: %w", err)
	}
	return c.unload(ctx, name, sel)
}

// State reads the job: not loaded, or loaded and ended, is gone; loaded and
// not yet started is pending; running is active.
func (c *Containment) State(ctx context.Context, name string, sel platform.ScopeSelector) (platform.ContainmentState, error) {
	st, err := c.print(ctx, c.target(name, sel))
	if err != nil {
		return 0, err
	}
	return st.state, nil
}

// Signal sends sig to the job's process through launchd. SIGKILL also
// unloads the job, which removes a pending job before it can start.
func (c *Containment) Signal(ctx context.Context, name string, sel platform.ScopeSelector, sig syscall.Signal) error {
	target := c.target(name, sel)
	if _, err := c.launchctl(ctx, "kill", strconv.Itoa(int(sig)), target); err != nil {
		return err
	}
	if sig == syscall.SIGKILL {
		return c.bootout(ctx, target)
	}
	return nil
}

// bootout asks launchd to unload the job and requires an acknowledgement:
// exit 0, or "not found" when it is already gone. Anything else is an
// error, never ignored.
func (c *Containment) bootout(ctx context.Context, target string) error {
	res, err := c.launchctl(ctx, "bootout", target)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && res.ExitCode != launchctlNotFound {
		return fmt.Errorf("%w: launchctl bootout %s exited %d", platform.ErrSupervisorState, target, res.ExitCode)
	}
	return nil
}

// unloadRounds bounds how often unload may find the job loaded again.
const unloadRounds = 8

// unload runs once the supervisor is proven dead. It proves the label
// absent: two consecutive prints must show it not loaded. A job found
// loaded and ended in between, as from a bootstrap that was still in
// flight, is booted out with an acknowledgement and the count restarts. A
// job found running is not quiet. Only then are its files removed.
func (c *Containment) unload(ctx context.Context, name string, sel platform.ScopeSelector) error {
	target := c.target(name, sel)
	absent := 0
	for round := 0; absent < 2; {
		st, err := c.print(ctx, target)
		if err != nil {
			return err
		}
		if !st.loaded {
			absent++
			continue
		}
		absent = 0
		if round++; round > unloadRounds {
			return fmt.Errorf("%w: job %s keeps loading", platform.ErrSupervisorState, target)
		}
		if st.state != platform.StateGone {
			return fmt.Errorf("%w: job %s is not quiet", platform.ErrContainmentNotEmpty, target)
		}
		if err := c.bootout(ctx, target); err != nil {
			return err
		}
	}
	return c.removeJobFiles(name)
}

func (c *Containment) launchctl(ctx context.Context, args ...string) (platform.ExecResult, error) {
	d := c.CallTimeout
	if d <= 0 {
		d = defaultAckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	res, err := c.Runner.Run(cctx, platform.ExecSpec{Argv: append([]string{launchctlBin}, args...), Env: launchctlEnv()})
	if err != nil {
		return res, fmt.Errorf("launchctl %s: %w", args[0], err)
	}
	return res, nil
}

type jobState struct {
	state    platform.ContainmentState
	loaded   bool
	exited   bool
	exitCode int
	pid      int
}

// print reads the job's state with launchctl print. A job that is not
// loaded is gone.
func (c *Containment) print(ctx context.Context, target string) (jobState, error) {
	res, err := c.launchctl(ctx, "print", target)
	if err != nil {
		return jobState{}, err
	}
	if res.ExitCode == launchctlNotFound {
		return jobState{state: platform.StateGone}, nil
	}
	if res.ExitCode != 0 {
		return jobState{}, fmt.Errorf("%w: launchctl print %s exited %d", platform.ErrSupervisorState, target, res.ExitCode)
	}
	return parsePrint(res.Stdout)
}

// parsePrint reads a loaded job's state from launchctl print output. A
// running job, or one with a PID, is active; one that has exited, with a
// numeric last exit code, is gone; one that has never run is pending. Any
// other state is an error.
func parsePrint(out string) (jobState, error) {
	m := printState.FindStringSubmatch(out)
	if m == nil {
		return jobState{}, fmt.Errorf("%w: no state in launchctl print output", platform.ErrSupervisorState)
	}
	st := jobState{loaded: true}
	if p := printPID.FindStringSubmatch(out); p != nil {
		st.pid, _ = strconv.Atoi(p[1])
	}
	if e := printLastRun.FindStringSubmatch(out); e != nil {
		code, err := strconv.Atoi(e[1])
		if err != nil {
			return jobState{}, fmt.Errorf("%w: exit code %q", platform.ErrSupervisorState, e[1])
		}
		st.exited, st.exitCode = true, code
	}
	switch {
	case m[1] == "running" || st.pid > 0:
		st.state = platform.StateActive
	case m[1] == stateNotRunning && st.exited:
		st.state = platform.StateGone
	case m[1] == stateNotRunning || m[1] == "spawn scheduled" || m[1] == "waiting":
		st.state = platform.StatePending
	default:
		return jobState{}, fmt.Errorf("%w: job state %q", platform.ErrSupervisorState, m[1])
	}
	return st, nil
}

// waitStarted waits until launchd reports the job running or ended.
func (c *Containment) waitStarted(ctx context.Context, target string) (jobState, error) {
	timeout := c.AckTimeout
	if timeout <= 0 {
		timeout = defaultAckTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		st, err := c.print(ctx, target)
		if err != nil {
			return jobState{}, err
		}
		if st.state == platform.StateActive || st.exited {
			return st, nil
		}
		if time.Now().After(deadline) {
			return jobState{}, fmt.Errorf("%w: job %s did not start within %v", platform.ErrLaunchNotAcknowledged, target, timeout)
		}
		if err := c.sleep(ctx); err != nil {
			return jobState{}, fmt.Errorf("wait for job %s to start: %w", target, err)
		}
	}
}

func (c *Containment) sleep(ctx context.Context) error {
	poll := c.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	t := time.NewTimer(poll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("launchd poll: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

type jobFiles struct{ plist, out, errOut string }

func (c *Containment) jobFiles(name string) jobFiles {
	return jobFiles{
		plist:  filepath.Join(c.PlistDir, name+".plist"),
		out:    filepath.Join(c.PlistDir, name+".log"),
		errOut: filepath.Join(c.PlistDir, name+".err.log"),
	}
}

func (c *Containment) domain(sel platform.ScopeSelector) string {
	if !sel.User {
		return domainSystem
	}
	return domainGUIPref + strconv.Itoa(c.UID)
}

func (c *Containment) target(name string, sel platform.ScopeSelector) string {
	return c.domain(sel) + "/" + name
}

// openJobDir opens the job folder through the no-follow walk, after
// checking it is private: owned by this user and mode 0700. PlistDir must
// be a canonical path; a link anywhere on it is refused.
func (c *Containment) openJobDir() (*os.File, error) {
	if err := platform.CheckPrivateDir(c.PlistDir); err != nil {
		return nil, fmt.Errorf("launchd job folder: %w", err)
	}
	dir, err := platform.OpenDirNoFollow(c.PlistDir)
	if err != nil {
		return nil, fmt.Errorf("launchd job folder: %w", err)
	}
	info, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("launchd job folder: %w", err)
	}
	if info.Mode().Perm() != 0o700 {
		_ = dir.Close()
		return nil, fmt.Errorf("%w: launchd job folder %q has mode %v, want 0700", platform.ErrUnsafeDir, c.PlistDir, info.Mode().Perm())
	}
	return dir, nil
}

// writeDurable creates name in dir with mode 0600, refusing to follow or
// replace anything already there, then fsyncs the file and the folder, so
// launchd never loads a definition that power loss could take back.
func (c *Containment) writeDurable(dir *os.File, name string, data []byte) error {
	sync := c.syncFn
	if sync == nil {
		sync = (*os.File).Sync
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, plistMode)
	if err != nil {
		return fmt.Errorf("create %q: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %q: %w", name, err)
	}
	if err := sync(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync %q: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", name, err)
	}
	if err := sync(dir); err != nil {
		return fmt.Errorf("fsync job folder: %w", err)
	}
	return nil
}

// removeJobFiles deletes the job's files through the no-follow walk. If the
// walk fails, as when a folder above was swapped for a link, nothing is
// deleted and the error is returned.
func (c *Containment) removeJobFiles(name string) error {
	dir, err := platform.OpenDirNoFollow(c.PlistDir)
	if err != nil {
		return fmt.Errorf("launchd job folder: %w", err)
	}
	defer func() { _ = dir.Close() }()
	f := c.jobFiles(name)
	for _, p := range []string{f.plist, f.out, f.errOut} {
		if err := unix.Unlinkat(int(dir.Fd()), filepath.Base(p), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove %q: %w", p, err)
		}
	}
	return nil
}

func copyOutput(dir *os.File, name string, w io.Writer) {
	if w == nil {
		return
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	_, _ = io.Copy(w, f)
}

// RenderPlist renders a launchd job definition that runs argv once with
// exactly env.
func RenderPlist(label string, argv, env []string, dir, outPath, errPath string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	kv := func(k, v string) error {
		b.WriteString("<key>")
		if err := xml.EscapeText(&b, []byte(k)); err != nil {
			return fmt.Errorf("escape plist key: %w", err)
		}
		b.WriteString("</key><string>")
		if err := xml.EscapeText(&b, []byte(v)); err != nil {
			return fmt.Errorf("escape plist value: %w", err)
		}
		b.WriteString("</string>\n")
		return nil
	}
	if err := kv("Label", label); err != nil {
		return nil, err
	}
	b.WriteString("<key>ProgramArguments</key>\n<array>\n")
	for _, a := range argv {
		b.WriteString("<string>")
		if err := xml.EscapeText(&b, []byte(a)); err != nil {
			return nil, fmt.Errorf("escape plist argument: %w", err)
		}
		b.WriteString("</string>\n")
	}
	b.WriteString("</array>\n<key>EnvironmentVariables</key>\n<dict>\n")
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidEnv, e)
		}
		if err := kv(k, v); err != nil {
			return nil, err
		}
	}
	b.WriteString("</dict>\n")
	for _, p := range [][2]string{{"WorkingDirectory", dir}, {"StandardOutPath", outPath}, {"StandardErrorPath", errPath}} {
		if p[1] == "" {
			continue
		}
		if err := kv(p[0], p[1]); err != nil {
			return nil, err
		}
	}
	b.WriteString("<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><false/>\n</dict>\n</plist>\n")
	return b.Bytes(), nil
}
