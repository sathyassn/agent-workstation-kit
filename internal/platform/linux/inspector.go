package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	defaultProcRoot = "/proc"
	// procStatStartTimeIdx is field 22 of /proc/<pid>/stat (starttime),
	// counted from the first field after the command name, which is field 3.
	procStatStartTimeIdx = 22 - 3
)

// ProcessInspector reads process identities from /proc. A process counts as
// alive only when the boot ID and its start time both match, so a reused PID
// or a process from an earlier boot is never mistaken for the recorded one.
type ProcessInspector struct {
	// ProcRoot is /proc, or a fixture folder in tests.
	ProcRoot string
}

// NewProcessInspector returns an inspector that reads /proc.
func NewProcessInspector() *ProcessInspector {
	return &ProcessInspector{ProcRoot: defaultProcRoot}
}

// BootID reads /proc/sys/kernel/random/boot_id.
func (p *ProcessInspector) BootID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("read boot id: %w", err)
	}
	path := filepath.Join(p.root(), "sys", "kernel", "random", "boot_id")
	// #nosec G304 -- the path is fixed under the inspector's proc root.
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read boot id %q: %w", path, err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("%w: empty boot id", platform.ErrInvalidProcessIdentity)
	}
	return id, nil
}

// Self returns this process's identity.
func (p *ProcessInspector) Self(ctx context.Context) (platform.ProcessIdentity, error) {
	return p.Identity(ctx, os.Getpid())
}

// Identity returns the identity of the running process pid.
func (p *ProcessInspector) Identity(ctx context.Context, pid int) (platform.ProcessIdentity, error) {
	boot, err := p.BootID(ctx)
	if err != nil {
		return platform.ProcessIdentity{}, err
	}
	start, err := p.startTime(pid)
	if err != nil {
		return platform.ProcessIdentity{}, err
	}
	return platform.ProcessIdentity{BootID: boot, PID: pid, StartTime: start}, nil
}

// Alive reports whether the process id names is still running. It returns
// an error, never false, when it cannot tell, so a caller cannot mistake an
// unreadable /proc for a dead executor.
func (p *ProcessInspector) Alive(ctx context.Context, id platform.ProcessIdentity) (bool, error) {
	if !id.Valid() {
		return false, fmt.Errorf("%w: %+v", platform.ErrInvalidProcessIdentity, id)
	}
	boot, err := p.BootID(ctx)
	if err != nil {
		return false, err
	}
	if boot != id.BootID {
		return false, nil
	}
	start, err := p.startTime(id.PID)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return start == id.StartTime, nil
}

// Signal sends sig to the process id names, after checking it is still that
// process. A process that has already exited is not an error.
func (p *ProcessInspector) Signal(ctx context.Context, id platform.ProcessIdentity, sig syscall.Signal) error {
	alive, err := p.Alive(ctx, id)
	if err != nil || !alive {
		return err
	}
	if err := syscall.Kill(id.PID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal pid %d: %w", id.PID, err)
	}
	return nil
}

func (p *ProcessInspector) startTime(pid int) (string, error) {
	path := filepath.Join(p.root(), strconv.Itoa(pid), "stat")
	// #nosec G304 -- the path is built from the proc root and a number.
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	return ParseProcStatStartTime(string(raw))
}

func (p *ProcessInspector) root() string {
	if p.ProcRoot == "" {
		return defaultProcRoot
	}
	return p.ProcRoot
}

// ParseProcStatStartTime returns field 22 (starttime) of a /proc/<pid>/stat
// line. The command name is skipped by its last closing parenthesis, so a
// name holding spaces or parentheses cannot shift the fields.
func ParseProcStatStartTime(stat string) (string, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return "", fmt.Errorf("%w: proc stat has no command name", platform.ErrInvalidProcessIdentity)
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) <= procStatStartTimeIdx {
		return "", fmt.Errorf("%w: proc stat has %d fields after the name", platform.ErrInvalidProcessIdentity, len(fields))
	}
	start := fields[procStatStartTimeIdx]
	if _, err := strconv.ParseUint(start, 10, 64); err != nil {
		return "", fmt.Errorf("%w: start time %q", platform.ErrInvalidProcessIdentity, start)
	}
	return start, nil
}
