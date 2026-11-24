package linux_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/linux"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

func TestLinuxHostnameProviderPassesContractTest(t *testing.T) {
	t.Parallel()

	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static:    "lab-ws-01",
		Pretty:    "Lab Workstation 01",
		Transient: "lab-ws-01",
	})
	p := &linux.HostnameProvider{Runner: runner}

	for _, key := range []contract.StateKey{
		platform.HostnameKeyStatic,
		platform.HostnameKeyPretty,
		platform.HostnameKeyTransient,
	} {
		t.Run(string(key), func(t *testing.T) {
			contracttest.TestStateProvider(t, p, key, func(ctx context.Context) error {
				return p.Write(ctx, key, contract.State{Value: "acme-ws-02"})
			})
		})
	}
}

func TestLinuxHostnameProviderRejectsInvalidKeysValuesAndSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static:    "lab-ws-01",
		Pretty:    "Lab 01",
		Transient: "lab-ws-01",
	})
	p := &linux.HostnameProvider{Runner: runner}

	if _, err := p.Read(ctx, platform.HostnameKeyLocal); !errors.Is(err, platform.ErrStateKeyNotFound) {
		t.Fatalf("Read unknown key = %v, want ErrStateKeyNotFound", err)
	}
	if err := p.Write(ctx, platform.HostnameKeyLocal, contract.State{Value: "x"}); !errors.Is(err, platform.ErrStateKeyNotFound) {
		t.Fatalf("Write unknown key = %v, want ErrStateKeyNotFound", err)
	}
	for _, bad := range []string{"", "-flag", "bad name", "bad\nname"} {
		if err := p.Write(ctx, platform.HostnameKeyStatic, contract.State{Value: bad}); !errors.Is(err, platform.ErrInvalidStateValue) {
			t.Errorf("Write static %q = %v, want ErrInvalidStateValue", bad, err)
		}
	}
	if err := p.Restore(ctx, contract.Snapshot{Provider: contract.ProviderUFW, Data: []byte(`{}`)}); !errors.Is(err, platform.ErrSnapshotProviderMismatch) {
		t.Fatalf("Restore wrong provider = %v, want ErrSnapshotProviderMismatch", err)
	}
	for _, badJSON := range []string{`not json`, `{"version":99}`, `{"version":1,"unknown":1}`, `{"version":1,"static":"-bad"}`} {
		if err := p.Restore(ctx, contract.Snapshot{Provider: contract.ProviderHostname, Data: []byte(badJSON)}); !errors.Is(err, platform.ErrCorruptSnapshot) {
			t.Errorf("Restore %s = %v, want ErrCorruptSnapshot", badJSON, err)
		}
	}
}

func TestLinuxHostnameProviderFileBackedRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backing := filepath.Join(t.TempDir(), "hostname.json")
	r1, err := platformtest.NewFileHostnameRunner(backing, platformtest.HostnameState{
		Static:    "lab-ws-01",
		Pretty:    "Lab WS",
		Transient: "lab-ws-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	p1 := &linux.HostnameProvider{Runner: r1}
	snap, err := p1.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := p1.Write(ctx, platform.HostnameKeyStatic, contract.State{Value: "lab-ws-99"}); err != nil {
		t.Fatal(err)
	}

	// A second runner reading the same backing file sees the mutation and restores it.
	r2, err := platformtest.NewFileHostnameRunner(backing, platformtest.HostnameState{})
	if err != nil {
		t.Fatal(err)
	}
	p2 := &linux.HostnameProvider{Runner: r2}
	got, err := p2.Read(ctx, platform.HostnameKeyStatic)
	if err != nil || got.Value != "lab-ws-99" {
		t.Fatalf("Read across runners = %+v, %v", got, err)
	}
	if err := p2.Restore(ctx, snap); err != nil {
		t.Fatal(err)
	}
	got, err = p2.Read(ctx, platform.HostnameKeyStatic)
	if err != nil || got.Value != "lab-ws-01" {
		t.Fatalf("Read after Restore = %+v, %v", got, err)
	}
}

func TestLinuxHostnameSnapshotRejectsInvalidState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static:    "-invalid-leading-dash",
		Pretty:    "Lab 01",
		Transient: "lab-ws-01",
	})
	p := &linux.HostnameProvider{Runner: runner}
	if _, err := p.Snapshot(ctx); !errors.Is(err, platform.ErrInvalidStateValue) {
		t.Fatalf("Snapshot on invalid static hostname = %v, want ErrInvalidStateValue", err)
	}
}

func TestLinuxHostnameToolInvocationSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static:    "lab-ws-01",
		Pretty:    "Lab 01",
		Transient: "lab-ws-01",
	})
	p := &linux.HostnameProvider{Runner: runner}
	trickyPretty := "Lab Host; echo $(id) && 'quoted'"
	if err := p.Write(ctx, platform.HostnameKeyPretty, contract.State{Value: trickyPretty}); err != nil {
		t.Fatalf("Write pretty with shell metacharacters: %v", err)
	}
	calls := runner.Calls()
	envs := runner.Envs()
	if len(calls) != 1 || len(envs) != 1 {
		t.Fatalf("calls=%v envs=%v, want 1", calls, envs)
	}
	wantArgv := []string{"/usr/bin/hostnamectl", "set-hostname", "--pretty", trickyPretty}
	if len(calls[0]) != len(wantArgv) {
		t.Fatalf("argv = %q, want %q", calls[0], wantArgv)
	}
	for i := range wantArgv {
		if calls[0][i] != wantArgv[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, calls[0][i], wantArgv[i])
		}
	}
	if len(envs[0]) != 2 || envs[0][0] != "PATH=/usr/sbin:/usr/bin:/sbin:/bin" || envs[0][1] != "LANG=C" {
		t.Fatalf("env = %q, want minimal PATH and LANG=C only", envs[0])
	}

	// Fake runner rejects relative binary path and unexpected env variables.
	if _, err := runner.Run(ctx, platform.ExecSpec{
		Argv: []string{"hostnamectl", "--static"},
		Env:  []string{"PATH=/usr/bin", "LANG=C"},
	}); !errors.Is(err, platformtest.ErrUnexpectedInvocation) {
		t.Fatalf("relative binary err = %v, want ErrUnexpectedInvocation", err)
	}
	if _, err := runner.Run(ctx, platform.ExecSpec{
		Argv: []string{"/usr/bin/hostnamectl", "--static"},
		Env:  []string{"PATH=/usr/bin", "LANG=C", "SECRET=leak"},
	}); !errors.Is(err, platformtest.ErrUnexpectedInvocation) {
		t.Fatalf("extra env err = %v, want ErrUnexpectedInvocation", err)
	}
}
