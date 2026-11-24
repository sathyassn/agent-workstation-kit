package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// Class is the error class written in a CLI error (error.schema.json). Each
// class maps to exactly one SPC-001 exit code, so the code cannot disagree
// with the class.
type Class string

// Error classes, one per SPC-001 exit code except 2, which has three.
const (
	ClassFailed         Class = "failed"
	ClassUsage          Class = "usage"
	ClassRefused        Class = "refused"
	ClassNotImplemented Class = "not-implemented"
	ClassPersonMustAct  Class = "person-must-act"
	ClassRecovery       Class = "recovery"
	ClassNotStagedRoot  Class = "not-staged-root"
	ClassDrift          Class = "drift"
	ClassCancelled      Class = "cancelled"
)

// AllClasses returns every Class in a stable order.
func AllClasses() []Class {
	return []Class{
		ClassFailed, ClassUsage, ClassRefused, ClassNotImplemented, ClassPersonMustAct,
		ClassRecovery, ClassNotStagedRoot, ClassDrift, ClassCancelled,
	}
}

// ExitCode returns the SPC-001 exit code for c. An unknown class is a
// failure.
func (c Class) ExitCode() contract.ExitCode {
	switch c {
	case ClassUsage, ClassRefused, ClassNotImplemented:
		return contract.ExitRefused
	case ClassPersonMustAct:
		return contract.ExitPersonMustAct
	case ClassRecovery:
		return contract.ExitRecoveryBlocks
	case ClassNotStagedRoot:
		return contract.ExitNotStagedRoot
	case ClassDrift:
		return contract.ExitDriftFound
	case ClassCancelled:
		return contract.ExitCancelled
	case ClassFailed:
		return contract.ExitFailed
	default:
		return contract.ExitFailed
	}
}

// Sentinel errors. They are never reassigned.
var (
	// ErrNotImplemented marks a command that is in the tree but not built yet.
	ErrNotImplemented = errors.New("not implemented yet")
	// ErrUsage marks a command line the CLI cannot parse.
	ErrUsage = errors.New("usage error")
)

const (
	// cliErrorSchemaVersion is the schema_version of error.schema.json.
	cliErrorSchemaVersion = 1
	// unnamedRule fills the rule of a refusal built without one, because
	// error.schema.json requires a rule for every refusal.
	unnamedRule = "unnamed"
)

// Error is a CLI failure with its class and, for a refusal, the rule it
// enforces. Every error a command returns is an *Error, so the exit code is
// always chosen on purpose.
type Error struct {
	Class Class
	// Rule names the violated rule; error.schema.json requires it for
	// ClassRefused.
	Rule string
	Err  error
}

// Error returns the message of the wrapped error.
func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Class)
	}
	return e.Err.Error()
}

// Unwrap returns the wrapped error.
func (e *Error) Unwrap() error { return e.Err }

// NewError wraps err with class c.
func NewError(c Class, err error) *Error { return &Error{Class: c, Err: err} }

// NewRefusal wraps err as a refusal of rule.
func NewRefusal(rule string, err error) *Error {
	return &Error{Class: ClassRefused, Rule: rule, Err: err}
}

// ExitCodeFor maps err to its SPC-001 exit code: nil is 0, a cancelled
// context is 130, an *Error uses its class, and anything else is 1.
func ExitCodeFor(err error) contract.ExitCode {
	if err == nil {
		return contract.ExitDone
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Class.ExitCode()
	}
	if errors.Is(err, context.Canceled) {
		return contract.ExitCancelled
	}
	return contract.ExitFailed
}

// ToCLIError converts a non-nil err to the JSON error result
// (error.schema.json).
func ToCLIError(err error) contract.CLIError {
	class := ClassFailed
	rule := ""
	var e *Error
	switch {
	case errors.As(err, &e):
		class, rule = e.Class, e.Rule
	case errors.Is(err, context.Canceled):
		class = ClassCancelled
	}
	msg := string(class)
	if err != nil && err.Error() != "" {
		msg = err.Error()
	}
	out := contract.CLIError{
		SchemaVersion: cliErrorSchemaVersion,
		Code:          class.ExitCode(),
		Class:         string(class),
		Message:       msg,
	}
	if class == ClassRefused && rule == "" {
		rule = unnamedRule
	}
	if rule != "" {
		out.Rule = &rule
	}
	return out
}

// classify gives every error that is not already an *Error a class. Errors
// from cobra and pflag (unknown command or flag, bad flag value, wrong
// argument count) carry no type, and every command's own errors are typed by
// runE, so an untyped error here is a usage error.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) || errors.Is(err, context.Canceled) {
		return err
	}
	return &Error{Class: ClassUsage, Err: fmt.Errorf("%w: %w", ErrUsage, err)}
}
