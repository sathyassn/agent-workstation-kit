package darwin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/darwin"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

func TestDarwinHostnameProviderPassesContractTest(t *testing.T) {
	t.Parallel()

	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static: "lab-mac-01",
		Pretty: "Lab Mac 01",
		Local:  "lab-mac-01",
	})
	p := &darwin.HostnameProvider{Runner: runner}

	for _, key := range []contract.StateKey{
		platform.HostnameKeyStatic,
		platform.HostnameKeyPretty,
		platform.HostnameKeyLocal,
	} {
		t.Run(string(key), func(t *testing.T) {
			contracttest.TestStateProvider(t, p, key, func(ctx context.Context) error {
				return p.Write(ctx, key, contract.State{Value: "acme-mac-02"})
			})
		})
	}
}

func TestDarwinHostnameProviderUnsetHostNameRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Fresh macOS machines commonly have HostName unset while ComputerName
	// and LocalHostName are set.
	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static: "",
		Pretty: "Lab Mac",
		Local:  "lab-mac",
	})
	p := &darwin.HostnameProvider{Runner: runner}

	before, err := p.Read(ctx, platform.HostnameKeyStatic)
	if err != nil || before.Value != "" {
		t.Fatalf("Read unset HostName = %+v, %v; want empty with nil err", before, err)
	}
	snap, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := p.Write(ctx, platform.HostnameKeyStatic, contract.State{Value: "lab-mac-01"}); err != nil {
		t.Fatalf("Write HostName: %v", err)
	}
	if err := p.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	after, err := p.Read(ctx, platform.HostnameKeyStatic)
	if err != nil || after.Value != "" {
		t.Fatalf("Read HostName after Restore = %+v, %v; want unset again", after, err)
	}
}

func TestDarwinHostnameProviderRejectsInvalidKeysValuesAndSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static: "lab-mac-01",
		Pretty: "Lab Mac 01",
		Local:  "lab-mac-01",
	})
	p := &darwin.HostnameProvider{Runner: runner}

	if _, err := p.Read(ctx, platform.HostnameKeyTransient); !errors.Is(err, platform.ErrStateKeyNotFound) {
		t.Fatalf("Read unknown key = %v, want ErrStateKeyNotFound", err)
	}
	if err := p.Write(ctx, platform.HostnameKeyLocal, contract.State{Value: "dotted.local"}); !errors.Is(err, platform.ErrInvalidStateValue) {
		t.Fatalf("Write dotted local hostname = %v, want ErrInvalidStateValue", err)
	}
	if err := p.Restore(ctx, contract.Snapshot{Provider: contract.ProviderAppFirewall, Data: []byte(`{}`)}); !errors.Is(err, platform.ErrSnapshotProviderMismatch) {
		t.Fatalf("Restore wrong provider = %v, want ErrSnapshotProviderMismatch", err)
	}
	for _, badJSON := range []string{`{`, `{"version":2}`, `{"version":1,"extra":true}`, `{"version":1,"local_host_name":"bad.dot"}`} {
		if err := p.Restore(ctx, contract.Snapshot{Provider: contract.ProviderHostname, Data: []byte(badJSON)}); !errors.Is(err, platform.ErrCorruptSnapshot) {
			t.Errorf("Restore %s = %v, want ErrCorruptSnapshot", badJSON, err)
		}
	}
}
