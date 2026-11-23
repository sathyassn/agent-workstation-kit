package darwin

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// ProcessInspector reads process identities with sysctl. A process counts as
// alive only when the boot session and its start time both match.
type ProcessInspector struct{}

// NewProcessInspector returns a sysctl-backed inspector.
func NewProcessInspector() *ProcessInspector { return &ProcessInspector{} }

// BootID returns kern.bootsessionuuid, which changes on every boot.
func (ProcessInspector) BootID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("read boot id: %w", err)
	}
	id, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", fmt.Errorf("sysctl kern.bootsessionuuid: %w", err)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("%w: empty boot session", platform.ErrInvalidProcessIdentity)
	}
	return id, nil
}

// Self returns this process's identity.
func (p ProcessInspector) Self(ctx context.Context) (platform.ProcessIdentity, error) {
	return p.Identity(ctx, os.Getpid())
}

// Identity returns the identity of the running process pid.
func (p ProcessInspector) Identity(ctx context.Context, pid int) (platform.ProcessIdentity, error) {
	boot, err := p.BootID(ctx)
	if err != nil {
		return platform.ProcessIdentity{}, err
	}
	start, found, err := startTime(pid)
	if err != nil {
		return platform.ProcessIdentity{}, err
	}
	if !found {
		return platform.ProcessIdentity{}, fmt.Errorf("%w: no process %d", platform.ErrInvalidProcessIdentity, pid)
	}
	return platform.ProcessIdentity{BootID: boot, PID: pid, StartTime: start}, nil
}

// Alive reports whether the process id names is still running. It returns
// an error, never false, when it cannot tell.
func (p ProcessInspector) Alive(ctx context.Context, id platform.ProcessIdentity) (bool, error) {
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
	start, found, err := startTime(id.PID)
	if err != nil || !found {
		return false, err
	}
	return start == id.StartTime, nil
}

// startTime returns the process start time as seconds.microseconds. An
// empty sysctl answer means no such process.
func startTime(pid int) (string, bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", false, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	if len(procs) == 0 {
		return "", false, nil
	}
	tv := procs[0].Proc.P_starttime
	return strconv.FormatInt(tv.Sec, 10) + "." + strconv.FormatInt(int64(tv.Usec), 10), true, nil
}

// NewContainment returns an adapter that runs the real launchctl, keeping
// its job definitions in plistDir.
func NewContainment(plistDir string) *Containment {
	return &Containment{
		Runner:    platform.OSCommandRunner{},
		Inspector: NewProcessInspector(),
		PlistDir:  plistDir,
		UID:       os.Getuid(),
	}
}
