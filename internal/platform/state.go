package platform

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// Sentinel errors returned by StateProvider implementations and the provider
// registry. They are sentinels for errors.Is and are never reassigned.
var (
	// ErrStateKeyNotFound reports a StateKey a provider does not own or know.
	ErrStateKeyNotFound = errors.New("state key not found")
	// ErrInvalidStateValue reports a state value that violates the provider's
	// syntax or safety rules.
	ErrInvalidStateValue = errors.New("invalid state value")
	// ErrSnapshotProviderMismatch reports Restore called with a snapshot
	// captured by a different StateProvider.
	ErrSnapshotProviderMismatch = errors.New("snapshot provider mismatch")
	// ErrCorruptSnapshot reports snapshot data that cannot be decoded
	// strictly into the provider's versioned format.
	ErrCorruptSnapshot = errors.New("snapshot data is corrupt")
	// ErrProviderNotRegistered reports a lookup for a StateProviderName that
	// is not registered.
	ErrProviderNotRegistered = errors.New("state provider not registered")
	// ErrProviderCommandFailed reports an OS state tool exiting non-zero.
	ErrProviderCommandFailed = errors.New("state provider command failed")
	// ErrInvalidProvider reports a nil provider or one with an empty or
	// duplicate Name.
	ErrInvalidProvider = errors.New("invalid state provider")
)

// State keys for the hostname provider (contract.ProviderHostname).
const (
	// HostnameKeyStatic is the persistent system hostname: hostnamectl
	// --static on Linux, scutil HostName on macOS.
	HostnameKeyStatic contract.StateKey = "hostname.static"
	// HostnameKeyPretty is the human-readable computer name: hostnamectl
	// --pretty on Linux, scutil ComputerName on macOS.
	HostnameKeyPretty contract.StateKey = "hostname.pretty"
	// HostnameKeyTransient is the kernel transient hostname on Linux
	// (hostnamectl --transient).
	HostnameKeyTransient contract.StateKey = "hostname.transient"
	// HostnameKeyLocal is the Bonjour local hostname on macOS
	// (scutil LocalHostName).
	HostnameKeyLocal contract.StateKey = "hostname.localname"
)

const (
	maxHostnameLen       = 253
	maxHostnameLabelLen  = 63
	maxPrettyHostnameLen = 253
)

// ProviderRegistry holds a validated set of StateProviders keyed by Name.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[contract.StateProviderName]contract.StateProvider
}

// NewProviderRegistry builds a registry from providers, refusing nil entries,
// empty names and duplicates.
func NewProviderRegistry(providers ...contract.StateProvider) (*ProviderRegistry, error) {
	m := make(map[contract.StateProviderName]contract.StateProvider, len(providers))
	for _, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("%w: nil provider", ErrInvalidProvider)
		}
		name := p.Name()
		if name == "" {
			return nil, fmt.Errorf("%w: empty provider name", ErrInvalidProvider)
		}
		if _, dup := m[name]; dup {
			return nil, fmt.Errorf("%w: duplicate provider %q", ErrInvalidProvider, name)
		}
		m[name] = p
	}
	return &ProviderRegistry{providers: m}, nil
}

// Get returns the registered provider for name.
func (r *ProviderRegistry) Get(name contract.StateProviderName) (contract.StateProvider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Require returns the registered provider for name or ErrProviderNotRegistered.
func (r *ProviderRegistry) Require(name contract.StateProviderName) (contract.StateProvider, error) {
	p, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProviderNotRegistered, name)
	}
	return p, nil
}

// Names returns all registered provider names in sorted order.
func (r *ProviderRegistry) Names() []contract.StateProviderName {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]contract.StateProviderName, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// Map returns a shallow copy of the registered providers map.
func (r *ProviderRegistry) Map() map[contract.StateProviderName]contract.StateProvider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[contract.StateProviderName]contract.StateProvider, len(r.providers))
	for k, v := range r.providers {
		out[k] = v
	}
	return out
}

// ValidateHostnameStatic checks that s is a valid RFC 1123 hostname (single
// label or dotted labels), non-empty, and cannot be parsed as a CLI flag.
func ValidateHostnameStatic(s string) error {
	if s == "" || len(s) > maxHostnameLen {
		return fmt.Errorf("%w: hostname %q must be 1..%d characters", ErrInvalidStateValue, s, maxHostnameLen)
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%w: hostname %q must not start with '-'", ErrInvalidStateValue, s)
	}
	for _, label := range strings.Split(s, ".") {
		if err := ValidateHostnameLabel(label); err != nil {
			return err
		}
	}
	return nil
}

// ValidateHostnameLabel checks that s is a single RFC 1123 DNS label
// (1..63 ASCII letters, digits or hyphens, not starting or ending with '-').
func ValidateHostnameLabel(s string) error {
	if s == "" || len(s) > maxHostnameLabelLen {
		return fmt.Errorf("%w: hostname label %q must be 1..%d characters", ErrInvalidStateValue, s, maxHostnameLabelLen)
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return fmt.Errorf("%w: hostname label %q must not start or end with '-'", ErrInvalidStateValue, s)
	}
	for i := range len(s) {
		c := s[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !isDigit && c != '-' {
			return fmt.Errorf("%w: hostname label %q contains invalid byte 0x%02x", ErrInvalidStateValue, s, c)
		}
	}
	return nil
}

// ValidateHostnamePretty checks that s is a safe human-readable computer name.
// An empty string is allowed so the pretty name can be cleared; control
// characters and leading '-' are refused.
func ValidateHostnamePretty(s string) error {
	if len(s) > maxPrettyHostnameLen {
		return fmt.Errorf("%w: pretty hostname exceeds %d bytes", ErrInvalidStateValue, maxPrettyHostnameLen)
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%w: pretty hostname %q must not start with '-'", ErrInvalidStateValue, s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: pretty hostname contains control character U+%04X", ErrInvalidStateValue, r)
		}
	}
	return nil
}
