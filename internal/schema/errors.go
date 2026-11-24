package schema

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned by the schema package. They must never be reassigned.
var (
	// ErrValidation marks a profile or fleet schema validation failure.
	ErrValidation = errors.New("schema validation failed")
	// ErrSafetyFloor marks an attempt to set or weaken a safety-floor invariant.
	ErrSafetyFloor = errors.New("safety floor refusal")
	// ErrUnknownKey marks an unrecognised configuration key.
	ErrUnknownKey = errors.New("unknown configuration key")
	// ErrInvalidValue marks a configuration value that cannot be parsed for its field type.
	ErrInvalidValue = errors.New("invalid configuration value")
)

// RuleSafetyFloorUnknown is the refusal rule used when an undeclared key under
// the safety.* namespace is supplied through any configuration layer.
const RuleSafetyFloorUnknown SafetyRule = "safety-floor-namespace"

// Issue is a single schema or fleet validation finding tied to a dotted path.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Error formats the issue in the canonical `path: message` form.
func (i Issue) Error() string {
	return fmt.Sprintf("%s: %s", i.Path, i.Message)
}

// ValidationError collects one or more schema validation issues.
type ValidationError struct {
	Issues []Issue
}

// Error formats all validation issues as a newline-separated summary.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return ErrValidation.Error()
	}
	parts := make([]string, len(e.Issues))
	for i, iss := range e.Issues {
		parts[i] = iss.Error()
	}
	return strings.Join(parts, "; ")
}

// Unwrap returns ErrValidation so errors.Is matches any ValidationError.
func (*ValidationError) Unwrap() error { return ErrValidation }

// SafetyFloorError reports that a layer attempted to set or weaken a
// non-configurable safety-floor rule.
type SafetyFloorError struct {
	Key     Key
	Layer   Layer
	Rule    SafetyRule
	Message string
}

// Error formats the safety-floor refusal naming the rule, key and layer.
func (e *SafetyFloorError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("safety floor refusal (%s) on %s in layer %s: %s", e.Rule, e.Key, e.Layer, e.Message)
	}
	return fmt.Sprintf("safety floor refusal (%s): %s cannot be set in layer %s", e.Rule, e.Key, e.Layer)
}

// Unwrap returns ErrSafetyFloor so errors.Is matches any SafetyFloorError.
func (*SafetyFloorError) Unwrap() error { return ErrSafetyFloor }
