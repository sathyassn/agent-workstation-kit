package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ExecSpec is one process a CommandRunner starts directly, with no shell.
type ExecSpec struct {
	Argv []string
	// Env is the complete environment. A nil Env is an empty environment,
	// never the caller's.
	Env    []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
	// OnStart is called with the PID once the process has started.
	OnStart func(pid int)
	// KillAfter is how long the runner waits after SIGTERM, sent when ctx
	// ends, before it kills the process. Zero means one second.
	KillAfter time.Duration
	// Gate, when set, holds the process before it runs Argv: it starts as
	// GateShell waiting on a pipe, Gate is called with its PID, and only
	// when Gate returns nil does it exec Argv, keeping the same PID and
	// start time. If Gate fails, or this process dies first, the pipe
	// closes and the held process exits without running Argv.
	Gate      func(pid int) error
	GateShell string
}

// gateScript reads one line from descriptor 3 and execs its arguments only
// if the line is "go". End of file, as when the engine dies, exits.
const gateScript = `IFS= read -r go <&3 || exit 125; [ "$go" = go ] || exit 125; exec 3<&-; exec "$@"`

// ErrGateClosed reports a gated launch whose Gate refused it; nothing ran.
// It is a sentinel for errors.Is and is never reassigned.
var ErrGateClosed = errors.New("launch gate closed; nothing ran")

// ExecResult is how a process ended. A non-zero exit code is a result, not
// an error.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// CommandRunner starts processes. Adapters take it as a field so their
// supervision logic is tested with a fake on any OS.
type CommandRunner interface {
	Run(ctx context.Context, spec ExecSpec) (ExecResult, error)
}

type commandRunnerContextKey struct{}

// WithCommandRunner returns a derived context carrying r so a StateProvider
// invoked inside an engine transaction runs its OS commands inside the
// engine's recorded containment rather than launching unsupervised children.
func WithCommandRunner(ctx context.Context, r CommandRunner) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, commandRunnerContextKey{}, r)
}

// CommandRunnerFromContext returns the CommandRunner attached by
// WithCommandRunner, or nil when none is set.
func CommandRunnerFromContext(ctx context.Context) CommandRunner {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(commandRunnerContextKey{}).(CommandRunner)
	return r
}

// OSCommandRunner runs processes with os/exec.
type OSCommandRunner struct{}

const (
	defaultKillAfter = time.Second
	// signalExitBase is the shell convention for a process ended by a
	// signal: 128 plus the signal number.
	signalExitBase = 128
)

// Run starts spec.Argv without a shell, with exactly spec.Env, and waits for
// it. When ctx ends it sends SIGTERM, then SIGKILL after spec.KillAfter. The
// returned error is set only when the process could not be started or
// waited for.
func (OSCommandRunner) Run(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, ErrCommandEmpty
	}
	start := time.Now()
	argv := spec.Argv
	var gateR, gateW *os.File
	if spec.Gate != nil {
		if !filepath.IsAbs(spec.GateShell) {
			return ExecResult{}, fmt.Errorf("%w: gate shell %q", ErrUntrustedBinary, spec.GateShell)
		}
		var err error
		if gateR, gateW, err = os.Pipe(); err != nil {
			return ExecResult{}, fmt.Errorf("gate pipe: %w", err)
		}
		defer func() { _ = gateW.Close() }()
		argv = append([]string{spec.GateShell, "-c", gateScript, "wskit-gate"}, spec.Argv...)
	}
	// #nosec G204 -- argv comes from a declared Command or a fixed adapter
	// argv, and runs without a shell; the gate script is a constant.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if gateR != nil {
		cmd.ExtraFiles = []*os.File{gateR}
	}
	cmd.Dir = spec.Dir
	cmd.Env = append([]string{}, spec.Env...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = spec.KillAfter
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = defaultKillAfter
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = teeWriter(&outBuf, spec.Stdout)
	cmd.Stderr = teeWriter(&errBuf, spec.Stderr)

	if err := cmd.Start(); err != nil {
		if gateR != nil {
			_ = gateR.Close()
		}
		return ExecResult{Duration: time.Since(start)}, fmt.Errorf("start %s: %w", spec.Argv[0], err)
	}
	var gateErr error
	if gateR != nil {
		_ = gateR.Close()
		if gateErr = spec.Gate(cmd.Process.Pid); gateErr == nil {
			_, gateErr = gateW.Write([]byte("go\n"))
		}
		_ = gateW.Close()
	}
	if spec.OnStart != nil {
		spec.OnStart(cmd.Process.Pid)
	}
	waitErr := cmd.Wait()
	res := ExecResult{Stdout: outBuf.String(), Stderr: errBuf.String(), Duration: time.Since(start)}
	if gateErr != nil {
		return res, fmt.Errorf("%w: %w", ErrGateClosed, gateErr)
	}
	if waitErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		res.ExitCode = exitCode(exitErr.ProcessState)
		return res, nil
	}
	return res, fmt.Errorf("wait %s: %w", spec.Argv[0], waitErr)
}

func exitCode(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() { //nolint:misspell // Go API name
		return signalExitBase + int(ws.Signal())
	}
	return ps.ExitCode()
}

func teeWriter(buf *bytes.Buffer, w io.Writer) io.Writer {
	if w == nil {
		return buf
	}
	return io.MultiWriter(buf, w)
}

// maxPartialLine bounds the bytes a LineWriter holds while it waits for a
// newline, so a tool that never ends a line cannot grow memory.
const maxPartialLine = 64 << 10

// LineWriter forwards each complete line to a callback. A line longer than
// 64 KiB is forwarded in pieces.
type LineWriter struct {
	mu      sync.Mutex
	onLine  func(string)
	partial []byte
}

// NewLineWriter returns a LineWriter that calls onLine for each line.
func NewLineWriter(onLine func(string)) *LineWriter {
	return &LineWriter{onLine: onLine}
}

// Write implements io.Writer.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			w.emitLocked()
			continue
		}
		w.partial = append(w.partial, b)
		if len(w.partial) >= maxPartialLine {
			w.emitLocked()
		}
	}
	return len(p), nil
}

// Flush forwards a trailing line that did not end with a newline.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.emitLocked()
	}
}

func (w *LineWriter) emitLocked() {
	line := string(bytes.TrimSuffix(w.partial, []byte{'\r'}))
	w.partial = w.partial[:0]
	if w.onLine != nil {
		w.onLine(line)
	}
}
