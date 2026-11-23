package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// fileLock is an exclusive flock on a lock file. The kernel releases it when
// the process dies, and Go opens files close-on-exec, so a tool the engine
// starts never inherits it.
type fileLock struct{ f *os.File }

const lockCreateRetries = 10

// acquireLock blocks until it holds the lock at path or ctx ends. Every
// acquire opens the lock's folder without following links and checks, on
// the open descriptor, that the folder is private: owned by this user and
// not writable by group or others.
func acquireLock(ctx context.Context, path string, poll time.Duration) (*fileLock, error) {
	p, err := openParent(path)
	if err != nil {
		return nil, fmt.Errorf("lock folder: %w", err)
	}
	defer p.close()
	if err := checkPrivateFD(p.dir); err != nil {
		return nil, err
	}
	// Darwin can answer ENOENT to an O_CREAT open that races another
	// creator of the same name; a few retries settle it.
	var fd int
	for range lockCreateRetries {
		fd, err = unix.Openat(p.fd(), p.name, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if !errors.Is(err, unix.ENOENT) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open lock %q: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
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

// checkPrivateFD checks an open folder: owned by the effective user, not
// writable by group or others.
func checkPrivateFD(dir *os.File) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
		return fmt.Errorf("stat %q: %w", dir.Name(), err)
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&0o022 != 0 {
		return fmt.Errorf("%w: %q (uid %d, mode %o)", platform.ErrUnsafeDir, dir.Name(), st.Uid, st.Mode&0o777)
	}
	return nil
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
