package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
}

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
	// #nosec G204 -- argv comes from a declared Command or a fixed adapter
	// argv, and runs without a shell.
	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
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
		return ExecResult{Duration: time.Since(start)}, fmt.Errorf("start %s: %w", spec.Argv[0], err)
	}
	if spec.OnStart != nil {
		spec.OnStart(cmd.Process.Pid)
	}
	waitErr := cmd.Wait()
	res := ExecResult{Stdout: outBuf.String(), Stderr: errBuf.String(), Duration: time.Since(start)}
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
