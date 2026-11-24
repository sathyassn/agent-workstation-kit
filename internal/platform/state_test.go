package platform_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

func TestProviderRegistry(t *testing.T) {
	t.Parallel()

	host := contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
		platform.HostnameKeyStatic: {Value: "lab-ws-01"},
	})
	pkgs := contracttest.NewFakeStateProvider(contract.ProviderPackages, map[contract.StateKey]contract.State{})

	reg, err := platform.NewProviderRegistry(pkgs, host)
	if err != nil {
		t.Fatalf("NewProviderRegistry: %v", err)
	}
	names := reg.Names()
	if len(names) != 2 || names[0] != contract.ProviderHostname || names[1] != contract.ProviderPackages {
		t.Fatalf("Names = %v, want sorted [hostname packages]", names)
	}
	got, ok := reg.Get(contract.ProviderHostname)
	if !ok || got != host {
		t.Fatalf("Get(hostname) = %v, %v", got, ok)
	}
	if _, err := reg.Require(contract.ProviderHostname); err != nil {
		t.Fatalf("Require(hostname): %v", err)
	}
	if _, err := reg.Require(contract.ProviderUFW); !errors.Is(err, platform.ErrProviderNotRegistered) {
		t.Fatalf("Require(ufw) = %v, want ErrProviderNotRegistered", err)
	}
	if m := reg.Map(); len(m) != 2 {
		t.Fatalf("Map len = %d, want 2", len(m))
	}
}

func TestProviderRegistryRejectsBadEntries(t *testing.T) {
	t.Parallel()

	p := contracttest.NewFakeStateProvider(contract.ProviderHostname, nil)
	empty := contracttest.NewFakeStateProvider("", nil)
	tests := []struct {
		name      string
		providers []contract.StateProvider
	}{
		{"nil provider", []contract.StateProvider{nil}},
		{"empty name", []contract.StateProvider{empty}},
		{"duplicate", []contract.StateProvider{p, p}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := platform.NewProviderRegistry(tc.providers...); !errors.Is(err, platform.ErrInvalidProvider) {
				t.Fatalf("err = %v, want ErrInvalidProvider", err)
			}
		})
	}
}

func TestHostnameValidation(t *testing.T) {
	t.Parallel()

	staticCases := []struct {
		val     string
		wantErr bool
	}{
		{"lab-ws-01", false},
		{"lab-ws-01.example.com", false},
		{"a", false},
		{"", true},
		{"-leading-dash", true},
		{"trailing-dash-", true},
		{"bad..dots", true},
		{"bad host", true},
		{"bad\nhost", true},
		{strings.Repeat("a", 64), true},
	}
	for _, tc := range staticCases {
		err := platform.ValidateHostnameStatic(tc.val)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateHostnameStatic(%q) = %v, wantErr=%v", tc.val, err, tc.wantErr)
		}
		if err != nil && !errors.Is(err, platform.ErrInvalidStateValue) {
			t.Errorf("ValidateHostnameStatic(%q) = %v, want ErrInvalidStateValue", tc.val, err)
		}
	}

	labelCases := []struct {
		val     string
		wantErr bool
	}{
		{"lab-mac-01", false},
		{"lab.dotted", true},
		{"-flag", true},
		{"", true},
	}
	for _, tc := range labelCases {
		err := platform.ValidateHostnameLabel(tc.val)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateHostnameLabel(%q) = %v, wantErr=%v", tc.val, err, tc.wantErr)
		}
	}

	prettyCases := []struct {
		val     string
		wantErr bool
	}{
		{"Lab Workstation 01", false},
		{"", false},
		{"-flag", true},
		{"bad\nline", true},
		{"bad\x00nul", true},
	}
	for _, tc := range prettyCases {
		err := platform.ValidateHostnamePretty(tc.val)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateHostnamePretty(%q) = %v, wantErr=%v", tc.val, err, tc.wantErr)
		}
	}
}

// Keep context import used if expanded; verify nil registry safety.
func TestNilRegistrySafe(t *testing.T) {
	t.Parallel()
	_ = context.Background()
	var reg *platform.ProviderRegistry
	if _, ok := reg.Get(contract.ProviderHostname); ok {
		t.Fatal("nil registry returned ok")
	}
	if reg.Names() != nil || reg.Map() != nil {
		t.Fatal("nil registry returned non-nil")
	}
}
