package darwin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

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

// Signal sends sig to the process id names. It re-reads the start time
// immediately before kill(2): a PID now held by a process with another start
// time is refused with ErrIdentityChanged, and a PID with no process is not
// an error.
//
// Residual race: macOS has no pidfd, so the process can exit, and its PID be
// reused, between that read and kill(2). The window is one system call, and
// macOS hands out PIDs in sequence rather than reusing a freed one at once.
func (p ProcessInspector) Signal(ctx context.Context, id platform.ProcessIdentity, sig syscall.Signal) error {
	if !id.Valid() {
		return fmt.Errorf("%w: %+v", platform.ErrInvalidProcessIdentity, id)
	}
	boot, err := p.BootID(ctx)
	if err != nil {
		return err
	}
	if boot != id.BootID {
		return nil
	}
	start, found, err := startTime(id.PID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if start != id.StartTime {
		return fmt.Errorf("%w: pid %d started at %s, recorded %s", platform.ErrIdentityChanged, id.PID, start, id.StartTime)
	}
	if err := syscall.Kill(id.PID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal pid %d: %w", id.PID, err)
	}
	return nil
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
// its job definitions in plistDir, after checking that launchctl and the
// shell are owned by root and not writable by others.
func NewContainment(plistDir string) (*Containment, error) {
	for _, b := range []string{launchctlBin, shellBin} {
		if err := platform.CheckTrustedBinary(b); err != nil {
			return nil, fmt.Errorf("darwin containment: %w", err)
		}
	}
	return &Containment{
		Runner:    platform.OSCommandRunner{},
		Inspector: NewProcessInspector(),
		PlistDir:  plistDir,
		UID:       os.Getuid(),
	}, nil
}
