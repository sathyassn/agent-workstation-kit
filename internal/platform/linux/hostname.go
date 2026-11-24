package linux

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
	defaultHostnamectlBin  = "/usr/bin/hostnamectl"
	hostnameSnapshotVer    = 1
	hostnamectlSetCmd      = "set-hostname"
	hostnamectlFlagStatic  = "--static"
	hostnamectlFlagPretty  = "--pretty"
	hostnamectlFlagTrans   = "--transient"
	hostnamectlLocaleEnv   = "LANG=C"
	hostnamectlUnsetTarget = ""
)

func hostnamectlEnv() []string { return []string{supervisorPath, hostnamectlLocaleEnv} }

// HostnameProvider manages Linux hostnames through hostnamectl(1).
// It implements contract.StateProvider for contract.ProviderHostname.
type HostnameProvider struct {
	Runner platform.CommandRunner
	// Hostnamectl is the absolute path to hostnamectl; empty uses
	// /usr/bin/hostnamectl.
	Hostnamectl string
}

var _ contract.StateProvider = (*HostnameProvider)(nil)

// NewHostnameProvider returns a HostnameProvider wired to the real system
// hostnamectl binary after verifying it is root-owned and not writable by
// others.
func NewHostnameProvider() (*HostnameProvider, error) {
	if err := platform.CheckTrustedBinary(defaultHostnamectlBin); err != nil {
		return nil, fmt.Errorf("linux hostname provider: %w", err)
	}
	return &HostnameProvider{
		Runner:      platform.OSCommandRunner{},
		Hostnamectl: defaultHostnamectlBin,
	}, nil
}

// Name returns contract.ProviderHostname.
func (p *HostnameProvider) Name() contract.StateProviderName {
	return contract.ProviderHostname
}

func (p *HostnameProvider) bin() string {
	if p.Hostnamectl != "" {
		return p.Hostnamectl
	}
	return defaultHostnamectlBin
}

func (p *HostnameProvider) runner() platform.CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return platform.OSCommandRunner{}
}

func hostnamectlFlag(key contract.StateKey) (string, error) {
	switch key {
	case platform.HostnameKeyStatic:
		return hostnamectlFlagStatic, nil
	case platform.HostnameKeyPretty:
		return hostnamectlFlagPretty, nil
	case platform.HostnameKeyTransient:
		return hostnamectlFlagTrans, nil
	default:
		return "", fmt.Errorf("%w: %q", platform.ErrStateKeyNotFound, key)
	}
}

func validateLinuxHostnameValue(key contract.StateKey, val string, allowEmpty bool) error {
	if val == hostnamectlUnsetTarget && allowEmpty {
		return nil
	}
	var err error
	switch key {
	case platform.HostnameKeyStatic, platform.HostnameKeyTransient:
		err = platform.ValidateHostnameStatic(val)
	case platform.HostnameKeyPretty:
		err = platform.ValidateHostnamePretty(val)
	default:
		return fmt.Errorf("%w: %q", platform.ErrStateKeyNotFound, key)
	}
	if err != nil {
		return fmt.Errorf("linux %s: %w", key, err)
	}
	return nil
}

// Read returns the current Linux hostname value for key.
func (p *HostnameProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	if err := ctx.Err(); err != nil {
		return contract.State{}, fmt.Errorf("read %s: %w", key, err)
	}
	flag, err := hostnamectlFlag(key)
	if err != nil {
		return contract.State{}, err
	}
	res, err := p.runner().Run(ctx, platform.ExecSpec{
		Argv: []string{p.bin(), flag},
		Env:  hostnamectlEnv(),
	})
	if err != nil {
		return contract.State{}, fmt.Errorf("read %s: %w", key, err)
	}
	if res.ExitCode != 0 {
		return contract.State{}, fmt.Errorf("%w: hostnamectl %s exited %d: %s",
			platform.ErrProviderCommandFailed, flag, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return contract.State{Value: strings.TrimRight(res.Stdout, "\r\n")}, nil
}

// Write sets one Linux hostname key through hostnamectl set-hostname.
func (p *HostnameProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	return p.writeValue(ctx, key, want.Value, key == platform.HostnameKeyPretty)
}

func (p *HostnameProvider) writeValue(ctx context.Context, key contract.StateKey, val string, allowEmpty bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	flag, err := hostnamectlFlag(key)
	if err != nil {
		return err
	}
	if err := validateLinuxHostnameValue(key, val, allowEmpty); err != nil {
		return err
	}
	res, err := p.runner().Run(ctx, platform.ExecSpec{
		Argv: []string{p.bin(), hostnamectlSetCmd, flag, val},
		Env:  hostnamectlEnv(),
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w: hostnamectl %s %s exited %d: %s",
			platform.ErrProviderCommandFailed, hostnamectlSetCmd, flag, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

type linuxHostnameSnapshot struct {
	Version   int    `json:"version"`
	Static    string `json:"static"`
	Pretty    string `json:"pretty"`
	Transient string `json:"transient"`
}

// Snapshot captures static, pretty and transient hostnames into one JSON
// snapshot.
func (p *HostnameProvider) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, fmt.Errorf("snapshot %s: %w", p.Name(), err)
	}
	staticVal, err := p.Read(ctx, platform.HostnameKeyStatic)
	if err != nil {
		return contract.Snapshot{}, err
	}
	prettyVal, err := p.Read(ctx, platform.HostnameKeyPretty)
	if err != nil {
		return contract.Snapshot{}, err
	}
	transVal, err := p.Read(ctx, platform.HostnameKeyTransient)
	if err != nil {
		return contract.Snapshot{}, err
	}
	snap := linuxHostnameSnapshot{
		Version:   hostnameSnapshotVer,
		Static:    staticVal.Value,
		Pretty:    prettyVal.Value,
		Transient: transVal.Value,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return contract.Snapshot{}, fmt.Errorf("marshal %s snapshot: %w", p.Name(), err)
	}
	return contract.Snapshot{Provider: p.Name(), Data: data}, nil
}

// Restore returns static, pretty and transient hostnames to the values in s.
func (p *HostnameProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("restore %s: %w", p.Name(), err)
	}
	if s.Provider != p.Name() {
		return fmt.Errorf("%w: snapshot for %q cannot restore %q",
			platform.ErrSnapshotProviderMismatch, s.Provider, p.Name())
	}
	var snap linuxHostnameSnapshot
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
		{platform.HostnameKeyStatic, snap.Static},
		{platform.HostnameKeyPretty, snap.Pretty},
		{platform.HostnameKeyTransient, snap.Transient},
	}
	for _, item := range items {
		if err := validateLinuxHostnameValue(item.key, item.val, true); err != nil {
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
