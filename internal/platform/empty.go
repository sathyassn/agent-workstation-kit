package platform

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

const (
	defaultPollInterval = 25 * time.Millisecond
	defaultCallTimeout  = 10 * time.Second
)

// SupervisorControl is what EmptyBounded needs from an OS adapter.
type SupervisorControl interface {
	// State reports the containment's state. It returns an error, never
	// StateGone, when it cannot tell.
	State(ctx context.Context, name string, sel ScopeSelector) (ContainmentState, error)
	// Signal sends sig to every process in the containment, and removes a
	// pending one that has not started.
	Signal(ctx context.Context, name string, sel ScopeSelector, sig syscall.Signal) error
}

// ScopeSelector tells an adapter which service manager owns a containment.
type ScopeSelector struct {
	User bool
}

// EmptyBounded empties a containment within its bound (SPC-001 "Process
// supervision", step 4). The containment counts as busy while the OS
// supervisor reports it active or pending, or while the recorded supervisor
// process lives, since a live supervisor can still register the
// containment or start the tool. It waits for opts.Grace, sends SIGTERM to
// both, waits up to half of opts.KillWait, sends SIGKILL and waits the
// rest. A package-manager transaction is only waited for, for Grace plus
// KillWait, and is never signalled. Every inspection and signal has its own
// deadline. If the containment stays busy, it returns ErrContainmentNotEmpty.
func EmptyBounded(ctx context.Context, ctl SupervisorControl, procs ProcessControl, name string, sel ScopeSelector, opts EmptyOptions) error {
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	b := bounded{ctl: ctl, procs: procs, name: name, sel: sel, opts: opts, poll: poll}
	if opts.PackageTransaction {
		empty, err := b.waitEmpty(ctx, opts.Grace+opts.KillWait)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("%w: package-manager transaction still running in %q", ErrContainmentNotEmpty, name)
		}
		return nil
	}

	empty, err := b.waitEmpty(ctx, opts.Grace)
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
		if err := b.signal(ctx, step.sig); err != nil {
			return err
		}
		empty, err := b.waitEmpty(ctx, step.wait)
		if err != nil || empty {
			return err
		}
	}
	return fmt.Errorf("%w: %q", ErrContainmentNotEmpty, name)
}

type bounded struct {
	ctl   SupervisorControl
	procs ProcessControl
	name  string
	sel   ScopeSelector
	opts  EmptyOptions
	poll  time.Duration
}

// call runs one inspection or signal under its own deadline.
func (b bounded) call(ctx context.Context, what string, f func(context.Context) error) error {
	d := b.opts.CallTimeout
	if d <= 0 {
		d = defaultCallTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	if err := f(cctx); err != nil {
		return fmt.Errorf("%s %q: %w", what, b.name, err)
	}
	return nil
}

func (b bounded) supervisorAlive(ctx context.Context) (bool, error) {
	if !b.opts.Supervisor.Valid() {
		return false, nil
	}
	var alive bool
	err := b.call(ctx, "inspect supervisor of", func(c context.Context) error {
		var err error
		if alive, err = b.procs.Alive(c, b.opts.Supervisor); err != nil {
			return fmt.Errorf("alive: %w", err)
		}
		return nil
	})
	return alive, err
}

func (b bounded) busy(ctx context.Context) (bool, error) {
	var st ContainmentState
	if err := b.call(ctx, "check containment", func(c context.Context) error {
		var err error
		if st, err = b.ctl.State(c, b.name, b.sel); err != nil {
			return fmt.Errorf("state: %w", err)
		}
		return nil
	}); err != nil {
		return false, err
	}
	sup, err := b.supervisorAlive(ctx)
	if err != nil {
		return false, err
	}
	return st != StateGone || sup, nil
}

func (b bounded) signal(ctx context.Context, sig syscall.Signal) error {
	if err := b.call(ctx, "signal containment", func(c context.Context) error {
		return b.ctl.Signal(c, b.name, b.sel, sig)
	}); err != nil {
		return err
	}
	alive, err := b.supervisorAlive(ctx)
	if err != nil || !alive {
		return err
	}
	return b.call(ctx, "signal supervisor of", func(c context.Context) error {
		return b.procs.Signal(c, b.opts.Supervisor, sig)
	})
}

// waitEmpty polls until the containment is empty or bound has passed. It
// always checks at least once.
func (b bounded) waitEmpty(ctx context.Context, bound time.Duration) (bool, error) {
	deadline := time.Now().Add(bound)
	for {
		busy, err := b.busy(ctx)
		if err != nil {
			return false, err
		}
		if !busy {
			return true, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return false, nil
		}
		timer := time.NewTimer(min(b.poll, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, fmt.Errorf("wait for containment %q: %w", b.name, ctx.Err())
		case <-timer.C:
		}
	}
}
