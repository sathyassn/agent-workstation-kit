package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// fileLock is an exclusive flock on a lock file. The kernel releases it when
// the process dies, and Go opens files close-on-exec, so a tool the engine
// starts never inherits it.
type fileLock struct{ f *os.File }

// acquireLock blocks until it holds the lock at path or ctx ends.
func acquireLock(ctx context.Context, path string, poll time.Duration) (*fileLock, error) {
	// #nosec G304 -- the lock paths come from the engine options.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %q: %w", path, err)
	}
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &fileLock{f: f}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %q: %w", path, err)
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			_ = f.Close()
			return nil, fmt.Errorf("wait for lock %q: %w", path, ctx.Err())
		case <-t.C:
		}
	}
}

func (l *fileLock) release() {
	if l == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
}

// locks tracks what one operation holds, so the order host then journal is
// enforced: the host lock is never requested while the journal lock is held.
type locks struct {
	e       *Engine
	host    *fileLock
	journal *fileLock
}

func (l *locks) takeHost(ctx context.Context) error {
	if l.journal != nil {
		return ErrLockOrder
	}
	h, err := acquireLock(ctx, l.e.opts.HostLock, l.e.opts.LockPoll)
	if err != nil {
		return err
	}
	l.host = h
	return nil
}

func (l *locks) releaseHost() {
	l.host.release()
	l.host = nil
}

// withJournal runs fn while holding the journal lock. The lock is held only
// for fn, so another starter can read the record and see this executor's
// identity while it runs.
func (l *locks) withJournal(ctx context.Context, fn func() error) error {
	j, err := acquireLock(ctx, l.e.journalLockPath(), l.e.opts.LockPoll)
	if err != nil {
		return err
	}
	l.journal = j
	defer func() {
		l.journal = nil
		j.release()
	}()
	return fn()
}
