package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

func writeProc(t *testing.T, root string, pid int, start string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (tool) S " + strings.Repeat("0 ", 18) + start + " 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSignalGoesThroughPidfd is round 3, item 4 on Linux: the pidfd is
// opened before the identity check and the signal goes through it. A PID
// reused between the open and the check (another start time) is refused
// with ErrIdentityChanged and nothing is sent; an exited process is not an
// error; a kernel without pidfd fails closed.
func TestSignalGoesThroughPidfd(t *testing.T) {
	id := platform.ProcessIdentity{BootID: "boot-1", PID: 100, StartTime: "5000"}
	tests := []struct {
		name     string
		openErr  error
		reuse    bool // the PID is reused after the pidfd is opened
		wantSent bool
		wantErr  error
	}{
		{"same process", nil, false, true, nil},
		{"reused after open", nil, true, false, platform.ErrIdentityChanged},
		{"exited", syscall.ESRCH, false, false, nil},
		{"no pidfd", syscall.ENOSYS, false, false, syscall.ENOSYS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "sys", "kernel", "random"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "sys", "kernel", "random", "boot_id"), []byte("boot-1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			writeProc(t, root, 100, "5000")
			var opened, sent bool
			p := &ProcessInspector{ProcRoot: root, pidfd: &pidfdOps{
				open: func(int) (int, error) {
					opened = true
					if tc.reuse {
						writeProc(t, root, 100, "9999")
					}
					return 7, tc.openErr
				},
				send: func(fd int, _ syscall.Signal) error {
					if fd != 7 || !opened {
						t.Errorf("sent on fd %d before opening", fd)
					}
					sent = true
					return nil
				},
				close: func(int) error { return nil },
			}}
			err := p.Signal(context.Background(), id, syscall.SIGTERM)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if sent != tc.wantSent {
				t.Fatalf("sent = %v, want %v", sent, tc.wantSent)
			}
		})
	}
}
