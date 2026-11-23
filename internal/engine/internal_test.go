package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

func TestEnsureBlock(t *testing.T) {
	b := contract.ManagedBlock{ID: "wskit-x", Begin: "# BEGIN wskit-x", End: "# END wskit-x", Body: "a=1"}
	tests := []struct {
		name, in, want string
		err            error
	}{
		{"empty file", "", "# BEGIN wskit-x\na=1\n# END wskit-x\n", nil},
		{"append keeps foreign bytes", "x\r\ny", "x\r\ny\n# BEGIN wskit-x\na=1\n# END wskit-x\n", nil},
		{"replace keeps both sides", "top\n# BEGIN wskit-x\nold\n# END wskit-x\nbottom\n", "top\n# BEGIN wskit-x\na=1\n# END wskit-x\nbottom\n", nil},
		{"replace at end without newline", "top\n# BEGIN wskit-x\nold\n# END wskit-x", "top\n# BEGIN wskit-x\na=1\n# END wskit-x", nil},
		{"idempotent", "# BEGIN wskit-x\na=1\n# END wskit-x\n", "# BEGIN wskit-x\na=1\n# END wskit-x\n", nil},
		{"one marker", "# BEGIN wskit-x\n", "", ErrBlockMarkers},
		{"three markers", "# BEGIN wskit-x\n# END wskit-x\n# END wskit-x\n", "", ErrBlockMarkers},
		{"reversed markers", "# END wskit-x\n# BEGIN wskit-x\n", "", ErrBlockMarkers},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ensureBlock(tc.in, b)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("ensureBlock = %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestValidateBlockRejectsBadMarkers(t *testing.T) {
	for _, b := range []contract.ManagedBlock{
		{ID: "x", Begin: "# begin", End: "# end x"},
		{ID: "x", Begin: "# x", End: "# x"},
		{ID: "x", Begin: "# x\nmore", End: "# end x"},
		{ID: "x", Begin: "# begin x", End: "# end x", Body: "x"},
		{ID: "", Begin: "#", End: "# end"},
	} {
		if _, err := ensureBlock("", b); !errors.Is(err, ErrBlockMarkers) {
			t.Errorf("ensureBlock(%+v) err = %v, want ErrBlockMarkers", b, err)
		}
	}
}

func TestRemoveBlock(t *testing.T) {
	tests := []struct {
		name, in, want string
		err            error
	}{
		{"removes only the block", "a\n# BEGIN k\nbody\n# END k\nb\n", "a\nb\n", nil},
		{"missing block is no change", "a\nb", "a\nb", nil},
		{"one marker", "a\n# BEGIN k\n", "", ErrBlockMarkers},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := removeBlock(tc.in, "k")
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("removeBlock = %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

// TestLockOrderHostThenJournal: the host lock is never requested while the
// journal lock is held (SPC-001 "Locks").
func TestLockOrderHostThenJournal(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{opts: Options{JournalDir: dir, HostLock: filepath.Join(dir, "host.lock"), LockPoll: time.Millisecond}}
	l := &locks{e: e}
	err := l.withJournal(context.Background(), func() error { return l.takeHost(context.Background()) })
	if !errors.Is(err, ErrLockOrder) {
		t.Fatalf("host under journal: err = %v, want ErrLockOrder", err)
	}
	if err := l.takeHost(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer l.releaseHost()
	if err := l.withJournal(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("journal under host: %v", err)
	}
}

// TestLockWaitHonoursContext: a blocked lock wait ends with its context.
func TestLockWaitHonoursContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.lock")
	held, err := acquireLock(context.Background(), path, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := acquireLock(ctx, path, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}
