package darwin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	defaultScutilBin    = "/usr/sbin/scutil"
	hostnameSnapshotVer = 1
	scutilGetFlag       = "--get"
	scutilSetFlag       = "--set"
	scutilKeyHost       = "HostName"
	scutilKeyComputer   = "ComputerName"
	scutilKeyLocalHost  = "LocalHostName"
	scutilNotSetMarker  = "not set"
)

// HostnameProvider manages macOS hostnames through scutil(8).
// It implements contract.StateProvider for contract.ProviderHostname.
type HostnameProvider struct {
	Runner platform.CommandRunner
	// Scutil is the absolute path to scutil; empty uses /usr/sbin/scutil.
	Scutil string
}

var _ contract.StateProvider = (*HostnameProvider)(nil)

// NewHostnameProvider returns a HostnameProvider wired to the real system
// scutil binary after verifying it is root-owned and not writable by others.
func NewHostnameProvider() (*HostnameProvider, error) {
	if err := platform.CheckTrustedBinary(defaultScutilBin); err != nil {
		return nil, fmt.Errorf("darwin hostname provider: %w", err)
	}
	return &HostnameProvider{
		Runner: platform.OSCommandRunner{},
		Scutil: defaultScutilBin,
	}, nil
}

// Name returns contract.ProviderHostname.
func (p *HostnameProvider) Name() contract.StateProviderName {
	return contract.ProviderHostname
}

func (p *HostnameProvider) bin() string {
	if p.Scutil != "" {
		return p.Scutil
	}
	return defaultScutilBin
}

func (p *HostnameProvider) runner() platform.CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return platform.OSCommandRunner{}
}

func scutilKey(key contract.StateKey) (string, error) {
	switch key {
	case platform.HostnameKeyStatic:
		return scutilKeyHost, nil
	case platform.HostnameKeyPretty:
		return scutilKeyComputer, nil
	case platform.HostnameKeyLocal:
		return scutilKeyLocalHost, nil
	default:
		return "", fmt.Errorf("%w: %q", platform.ErrStateKeyNotFound, key)
	}
}

func validateDarwinHostnameValue(key contract.StateKey, val string, allowEmpty bool) error {
	if val == "" && allowEmpty {
		return nil
	}
	var err error
	switch key {
	case platform.HostnameKeyStatic:
		err = platform.ValidateHostnameStatic(val)
	case platform.HostnameKeyLocal:
		err = platform.ValidateHostnameLabel(val)
	case platform.HostnameKeyPretty:
		err = platform.ValidateHostnamePretty(val)
	default:
		return fmt.Errorf("%w: %q", platform.ErrStateKeyNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("darwin %s: %w", key, err)
	}
	return nil
}

// Read returns the current macOS hostname value for key. When scutil reports
// that a key is "not set" (common for HostName on fresh Macs), Read returns
// an empty State without error.
func (p *HostnameProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	if err := ctx.Err(); err != nil {
		return contract.State{}, fmt.Errorf("read %s: %w", key, err)
	}
	sk, err := scutilKey(key)
	if err != nil {
		return contract.State{}, err
	}
	res, err := p.runner().Run(ctx, platform.ExecSpec{
		Argv: []string{p.bin(), scutilGetFlag, sk},
		Env:  launchctlEnv(),
	})
	if err != nil {
		return contract.State{}, fmt.Errorf("read %s: %w", key, err)
	}
	if res.ExitCode != 0 {
		combined := strings.ToLower(res.Stderr + " " + res.Stdout)
		if strings.Contains(combined, scutilNotSetMarker) {
			return contract.State{Value: ""}, nil
		}
		return contract.State{}, fmt.Errorf("%w: scutil %s %s exited %d: %s",
			platform.ErrProviderCommandFailed, scutilGetFlag, sk, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return contract.State{Value: strings.TrimRight(res.Stdout, "\r\n")}, nil
}

// Write sets one macOS hostname key through scutil --set.
func (p *HostnameProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	return p.writeValue(ctx, key, want.Value, key == platform.HostnameKeyPretty)
}

func (p *HostnameProvider) writeValue(ctx context.Context, key contract.StateKey, val string, allowEmpty bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	sk, err := scutilKey(key)
	if err != nil {
		return err
	}
	if err := validateDarwinHostnameValue(key, val, allowEmpty); err != nil {
		return err
	}
	res, err := p.runner().Run(ctx, platform.ExecSpec{
		Argv: []string{p.bin(), scutilSetFlag, sk, val},
		Env:  launchctlEnv(),
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w: scutil %s %s exited %d: %s",
			platform.ErrProviderCommandFailed, scutilSetFlag, sk, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

type darwinHostnameSnapshot struct {
	Version  int    `json:"version"`
	Host     string `json:"host_name"`
	Computer string `json:"computer_name"`
	Local    string `json:"local_host_name"`
}

// Snapshot captures HostName, ComputerName and LocalHostName into one JSON
// snapshot.
func (p *HostnameProvider) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, fmt.Errorf("snapshot %s: %w", p.Name(), err)
	}
	hostVal, err := p.Read(ctx, platform.HostnameKeyStatic)
	if err != nil {
		return contract.Snapshot{}, err
	}
	compVal, err := p.Read(ctx, platform.HostnameKeyPretty)
	if err != nil {
		return contract.Snapshot{}, err
	}
	localVal, err := p.Read(ctx, platform.HostnameKeyLocal)
	if err != nil {
		return contract.Snapshot{}, err
	}
	snap := darwinHostnameSnapshot{
		Version:  hostnameSnapshotVer,
		Host:     hostVal.Value,
		Computer: compVal.Value,
		Local:    localVal.Value,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return contract.Snapshot{}, fmt.Errorf("marshal %s snapshot: %w", p.Name(), err)
	}
	return contract.Snapshot{Provider: p.Name(), Data: data}, nil
}

// Restore returns HostName, ComputerName and LocalHostName to the values in s.
func (p *HostnameProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("restore %s: %w", p.Name(), err)
	}
	if s.Provider != p.Name() {
		return fmt.Errorf("%w: snapshot for %q cannot restore %q",
			platform.ErrSnapshotProviderMismatch, s.Provider, p.Name())
	}
	var snap darwinHostnameSnapshot
	dec := json.NewDecoder(bytes.NewReader(s.Data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snap); err != nil {
		return fmt.Errorf("%w: %w", platform.ErrCorruptSnapshot, err)
	}
	if snap.Version != hostnameSnapshotVer {
		return fmt.Errorf("%w: unsupported snapshot version %d", platform.ErrCorruptSnapshot, snap.Version)
	}
	type pair struct {
		key contract.StateKey
		val string
	}
	items := []pair{
		{platform.HostnameKeyStatic, snap.Host},
		{platform.HostnameKeyPretty, snap.Computer},
		{platform.HostnameKeyLocal, snap.Local},
	}
	for _, item := range items {
		if err := validateDarwinHostnameValue(item.key, item.val, true); err != nil {
			return fmt.Errorf("%w: %w", platform.ErrCorruptSnapshot, err)
		}
	}
	for _, item := range items {
		if err := p.writeValue(ctx, item.key, item.val, true); err != nil {
			return err
		}
	}
	return nil
}
