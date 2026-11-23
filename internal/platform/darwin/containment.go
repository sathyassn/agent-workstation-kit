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

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	launchctlBin  = "launchctl"
	labelPrefix   = "dev.agent-workstation-kit.run."
	domainSystem  = "system"
	domainGUIPref = "gui/"
	// launchctlNotFound is launchctl's exit status for a service that is
	// not loaded.
	launchctlNotFound   = 113
	defaultPollInterval = 25 * time.Millisecond
	defaultAckTimeout   = 10 * time.Second
	plistMode           = 0o600
)

// ErrInvalidEnv reports an environment entry without a name. It is a
// sentinel for errors.Is and is never reassigned.
var ErrInvalidEnv = errors.New("environment entry has no name")

// Patterns compiled once and never changed.
var (
	validLabel   = regexp.MustCompile(`^dev\.agent-workstation-kit\.run\.[a-z0-9][a-z0-9_-]{0,63}\.[0-9]{1,6}$`)
	printPID     = regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)\s*$`)
	printState   = regexp.MustCompile(`(?m)^\s*state = ([a-z ]+?)\s*$`)
	printLastRun = regexp.MustCompile(`(?m)^\s*last exit code = ([0-9]+)\s*$`)
)

// identityReader reads a started tool's identity for the launch
// acknowledgement.
type identityReader interface {
	Identity(ctx context.Context, pid int) (platform.ProcessIdentity, error)
}

// Containment runs each command as its own launchd job, in the system domain
// for root and the user's gui domain otherwise. launchd owns the job, so it
// outlives the engine and is emptied by label on the next start.
type Containment struct {
	Runner    platform.CommandRunner
	Inspector identityReader
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

// Run loads spec as a launchd job, waits for launchd to start it, then for
// it to end, and returns its exit code. If ctx ends first, Run returns the
// context error and leaves the job for Empty.
func (c *Containment) Run(ctx context.Context, spec platform.LaunchSpec, onAck func(platform.LaunchAck)) (contract.CommandResult, error) {
	if len(spec.Argv) == 0 {
		return contract.CommandResult{}, platform.ErrCommandEmpty
	}
	if !validLabel.MatchString(spec.Name) {
		return contract.CommandResult{}, fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, spec.Name)
	}
	if err := platform.CheckPrivateDir(c.PlistDir); err != nil {
		return contract.CommandResult{}, fmt.Errorf("launchd job folder: %w", err)
	}
	start := time.Now()
	files := c.jobFiles(spec.Name)
	plist, err := RenderPlist(spec.Name, spec.Argv, spec.Env, spec.Dir, files.out, files.out)
	if err != nil {
		return contract.CommandResult{}, err
	}
	if err := writePrivate(files.plist, plist); err != nil {
		return contract.CommandResult{}, err
	}
	sel := platform.ScopeSelector{User: spec.Scope != contract.ScopeRoot}
	target := c.target(spec.Name, sel)
	res, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: []string{launchctlBin, "bootstrap", c.domain(sel), files.plist}})
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
	ack := platform.LaunchAck{Name: spec.Name}
	if st.pid > 0 {
		if id, idErr := c.Inspector.Identity(ctx, st.pid); idErr == nil {
			ack.Tool = id
		}
	}
	if onAck != nil {
		onAck(ack)
	}
	for st.running {
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
	copyOutput(files.out, spec.Output)
	c.unload(ctx, target, files)
	return contract.CommandResult{ExitCode: st.exitCode, Duration: time.Since(start)}, nil
}

// Empty empties the job within its bound (platform.EmptyBounded), then
// unloads its definition. A package-manager transaction is waited for and
// never signalled or unloaded while it runs.
func (c *Containment) Empty(ctx context.Context, name string, opts platform.EmptyOptions) error {
	if !validLabel.MatchString(name) {
		return fmt.Errorf("%w: %q", platform.ErrInvalidContainmentName, name)
	}
	sel := platform.ScopeSelector{User: opts.Scope != contract.ScopeRoot}
	if err := platform.EmptyBounded(ctx, c, name, sel, opts); err != nil {
		return fmt.Errorf("empty launchd job: %w", err)
	}
	c.unload(ctx, c.target(name, sel), c.jobFiles(name))
	return nil
}

// Active reports whether the job has a running process. A job that is not
// loaded is empty; output it cannot read is an error.
func (c *Containment) Active(ctx context.Context, name string, sel platform.ScopeSelector) (bool, error) {
	st, err := c.print(ctx, c.target(name, sel))
	if err != nil {
		return false, err
	}
	return st.running, nil
}

// Signal sends sig to the job's process through launchd.
func (c *Containment) Signal(ctx context.Context, name string, sel platform.ScopeSelector, sig syscall.Signal) error {
	argv := []string{launchctlBin, "kill", strconv.Itoa(int(sig)), c.target(name, sel)}
	if _, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: argv}); err != nil {
		return fmt.Errorf("launchctl kill %s: %w", name, err)
	}
	return nil
}

type jobState struct {
	running  bool
	exited   bool
	exitCode int
	pid      int
}

// print reads the job's state with launchctl print. A job that is not loaded
// reads as ended.
func (c *Containment) print(ctx context.Context, target string) (jobState, error) {
	res, err := c.Runner.Run(ctx, platform.ExecSpec{Argv: []string{launchctlBin, "print", target}})
	if err != nil {
		return jobState{}, fmt.Errorf("launchctl print %s: %w", target, err)
	}
	if res.ExitCode == launchctlNotFound {
		return jobState{}, nil
	}
	if res.ExitCode != 0 {
		return jobState{}, fmt.Errorf("%w: launchctl print %s exited %d", platform.ErrSupervisorState, target, res.ExitCode)
	}
	return parsePrint(res.Stdout)
}

// parsePrint reads the state, PID and last exit code from launchctl print
// output. Output without a state line is an error.
func parsePrint(out string) (jobState, error) {
	m := printState.FindStringSubmatch(out)
	if m == nil {
		return jobState{}, fmt.Errorf("%w: no state in launchctl print output", platform.ErrSupervisorState)
	}
	st := jobState{running: m[1] == "running"}
	if p := printPID.FindStringSubmatch(out); p != nil {
		st.pid, _ = strconv.Atoi(p[1])
		st.running = true
	}
	if e := printLastRun.FindStringSubmatch(out); e != nil {
		code, err := strconv.Atoi(e[1])
		if err != nil {
			return jobState{}, fmt.Errorf("%w: exit code %q", platform.ErrSupervisorState, e[1])
		}
		st.exited, st.exitCode = true, code
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
		if st.running || st.exited {
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

// unload removes an ended job's definition and files. It is best effort: a
// leftover definition of an ended job holds no process.
func (c *Containment) unload(ctx context.Context, target string, f jobFiles) {
	_, _ = c.Runner.Run(context.WithoutCancel(ctx), platform.ExecSpec{Argv: []string{launchctlBin, "bootout", target}})
	_ = os.Remove(f.plist)
	_ = os.Remove(f.out)
}

type jobFiles struct{ plist, out string }

func (c *Containment) jobFiles(name string) jobFiles {
	return jobFiles{
		plist: filepath.Join(c.PlistDir, name+".plist"),
		out:   filepath.Join(c.PlistDir, name+".log"),
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

// writePrivate creates path with mode 0600 and refuses to follow or replace
// anything already there.
func writePrivate(path string, data []byte) error {
	// #nosec G304 -- path is inside the checked private folder.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, plistMode)
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	return nil
}

func copyOutput(path string, w io.Writer) {
	if w == nil {
		return
	}
	// #nosec G304 -- path is inside the checked private folder.
	f, err := os.Open(path)
	if err != nil {
		return
	}
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
