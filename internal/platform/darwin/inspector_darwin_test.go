package darwin

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

func TestNewContainmentChecksBinaries(t *testing.T) {
	if _, err := NewContainment(privateDir(t)); err != nil {
		t.Fatalf("NewContainment on this host: %v", err)
	}
}

// TestSignalFailsClosedOnChangedIdentity is round 3, item 4: when the PID
// now belongs to a process with another start time, Signal refuses with an
// error and signals nothing; the recorded process is signalled only while
// its identity still matches.
func TestSignalFailsClosedOnChangedIdentity(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	p := NewProcessInspector()
	id, err := p.Identity(context.Background(), cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	changed := id
	changed.StartTime = "1.0"
	if err := p.Signal(context.Background(), changed, syscall.SIGKILL); !errors.Is(err, platform.ErrIdentityChanged) {
		t.Fatalf("signal with a changed identity: err = %v, want ErrIdentityChanged", err)
	}
	if alive, _ := p.Alive(context.Background(), id); !alive {
		t.Fatal("a process with another identity was signalled")
	}
	if err := p.Signal(context.Background(), id, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if alive, _ := p.Alive(context.Background(), id); alive {
		t.Fatal("the matching process was not signalled")
	}
}
