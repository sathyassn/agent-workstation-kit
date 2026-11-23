package contract

import (
	"errors"
	"fmt"
)

// TargetKind classifies an undeclared target refused by a transaction.
type TargetKind string

// Kinds of target a unit must declare in its Description before mutating or
// passing them to a transaction.
const (
	TargetPath     TargetKind = "path"
	TargetStateKey TargetKind = "state-key"
	TargetEnvKey   TargetKind = "env-key"
)

// AllTargetKinds returns every TargetKind in a stable order.
func AllTargetKinds() []TargetKind {
	return []TargetKind{TargetPath, TargetStateKey, TargetEnvKey}
}

// Valid reports whether k is a known TargetKind.
func (k TargetKind) Valid() bool { return contains(AllTargetKinds(), k) }

// String returns k as a string.
func (k TargetKind) String() string { return string(k) }

// ErrUndeclaredTarget is the sentinel error for mutations or command
// environment variables outside a unit's Description.
// It is a sentinel for errors.Is and must never be reassigned.
var ErrUndeclaredTarget = errors.New("target not declared in unit description")

// UndeclaredTargetError reports that a unit attempted to mutate a path or
// state key, or pass an environment variable, not declared in its Description.
type UndeclaredTargetError struct {
	Kind   TargetKind
	Target string
}

// Error returns a human-readable description of the refusal.
func (e *UndeclaredTargetError) Error() string {
	return fmt.Sprintf("undeclared %s %q: %s", e.Kind, e.Target, ErrUndeclaredTarget)
}

// Unwrap returns ErrUndeclaredTarget so errors.Is matches this error.
func (e *UndeclaredTargetError) Unwrap() error {
	return ErrUndeclaredTarget
}
