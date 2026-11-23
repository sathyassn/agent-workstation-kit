package platform

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

const defaultPollInterval = 25 * time.Millisecond

// SupervisorControl is what EmptyBounded needs from an OS adapter: whether a
// named containment still holds processes, and a way to signal all of them.
type SupervisorControl interface {
	// Active reports whether the containment holds processes. It returns an
	// error, never false, when it cannot tell.
	Active(ctx context.Context, name string, scope ScopeSelector) (bool, error)
	// Signal sends sig to every process in the containment.
	Signal(ctx context.Context, name string, scope ScopeSelector, sig syscall.Signal) error
}

// ScopeSelector tells an adapter which service manager owns a containment.
type ScopeSelector struct {
	User bool
}

// EmptyBounded empties a containment within its bound (SPC-001 "Process
// supervision", step 4). It waits for opts.Grace, sends SIGTERM, waits up to
// half of opts.KillWait, sends SIGKILL and waits the rest. A package-manager
// transaction is only waited for, for Grace plus KillWait, and is never
// signalled. If processes remain, it returns ErrContainmentNotEmpty.
func EmptyBounded(ctx context.Context, ctl SupervisorControl, name string, sel ScopeSelector, opts EmptyOptions) error {
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	if opts.PackageTransaction {
		empty, err := waitEmpty(ctx, ctl, name, sel, opts.Grace+opts.KillWait, poll)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("%w: package-manager transaction still running in %q", ErrContainmentNotEmpty, name)
		}
		return nil
	}

	empty, err := waitEmpty(ctx, ctl, name, sel, opts.Grace, poll)
	if err != nil || empty {
		return err
	}
	termWait := opts.KillWait / 2
	for _, step := range []struct {
		sig  syscall.Signal
		wait time.Duration
	}{
		{syscall.SIGTERM, termWait},
		{syscall.SIGKILL, opts.KillWait - termWait},
	} {
		if err := ctl.Signal(ctx, name, sel, step.sig); err != nil {
			return fmt.Errorf("signal %q with %v: %w", name, step.sig, err)
		}
		empty, err := waitEmpty(ctx, ctl, name, sel, step.wait, poll)
		if err != nil || empty {
			return err
		}
	}
	return fmt.Errorf("%w: %q", ErrContainmentNotEmpty, name)
}

// waitEmpty polls until the containment is empty or bound has passed. It
// always checks at least once.
func waitEmpty(ctx context.Context, ctl SupervisorControl, name string, sel ScopeSelector, bound, poll time.Duration) (bool, error) {
	deadline := time.Now().Add(bound)
	for {
		active, err := ctl.Active(ctx, name, sel)
		if err != nil {
			return false, fmt.Errorf("check containment %q: %w", name, err)
		}
		if !active {
			return true, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return false, nil
		}
		timer := time.NewTimer(min(poll, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, fmt.Errorf("wait for containment %q: %w", name, ctx.Err())
		case <-timer.C:
		}
	}
}
