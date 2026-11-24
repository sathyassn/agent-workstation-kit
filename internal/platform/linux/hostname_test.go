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
