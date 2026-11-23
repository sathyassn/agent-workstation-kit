package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// The enums below are named in SPC-001 "Interfaces and formats". They are
// written by hand until the schema generator (TSK-011) produces them from
// schema/; their values and names are the contract either way.

// Verb is a CLI action on a unit or the whole plan.
type Verb string

// Verbs a unit or the plan can be asked to perform.
const (
	VerbPlan     Verb = "plan"
	VerbApply    Verb = "apply"
	VerbVerify   Verb = "verify"
	VerbRevert   Verb = "revert"
	VerbStatus   Verb = "status"
	VerbExplain  Verb = "explain"
	VerbNext     Verb = "next"
	VerbDoctor   Verb = "doctor"
	VerbResume   Verb = "resume"
	VerbDisarm   Verb = "disarm"
	VerbAdopt    Verb = "adopt"
	VerbUnmanage Verb = "unmanage"
)

// AllVerbs returns every Verb in a stable order.
func AllVerbs() []Verb {
	return []Verb{
		VerbPlan, VerbApply, VerbVerify, VerbRevert, VerbStatus, VerbExplain,
		VerbNext, VerbDoctor, VerbResume, VerbDisarm, VerbAdopt, VerbUnmanage,
	}
}

// Valid reports whether v is a known Verb.
func (v Verb) Valid() bool { return contains(AllVerbs(), v) }

// String returns the verb as written on the command line.
func (v Verb) String() string { return string(v) }

// Outcome is the result of a unit's step, as reported by `next`.
// SPC-001: `next.outcome` is one of these values or null.
type Outcome string

// Outcomes a step can end in.
const (
	OutcomeVerified Outcome = "verified"
	OutcomeSkipped  Outcome = "skipped"
	OutcomeDeferred Outcome = "deferred"
	OutcomeFailed   Outcome = "failed"
	OutcomeStale    Outcome = "stale"
)

// AllOutcomes returns every Outcome in a stable order.
func AllOutcomes() []Outcome {
	return []Outcome{OutcomeVerified, OutcomeSkipped, OutcomeDeferred, OutcomeFailed, OutcomeStale}
}

// Valid reports whether o is a known Outcome.
func (o Outcome) Valid() bool { return contains(AllOutcomes(), o) }

// String returns the outcome as written in JSON.
func (o Outcome) String() string { return string(o) }

// EventKind classifies an Event.
type EventKind string

// Kinds of event a run emits.
const (
	EventStepStart EventKind = "step-start"
	EventStepEnd   EventKind = "step-end"
	EventChange    EventKind = "change"
	EventBackup    EventKind = "backup"
	EventCommand   EventKind = "command"
	EventOutput    EventKind = "output"
	EventWarning   EventKind = "warning"
	EventHandover  EventKind = "handover"
	EventFailure   EventKind = "failure"
	EventCoalesced EventKind = "coalesced"
	EventProgress  EventKind = "progress"
)

// AllEventKinds returns every EventKind in a stable order.
func AllEventKinds() []EventKind {
	return []EventKind{
		EventStepStart, EventStepEnd, EventChange, EventBackup, EventCommand,
		EventOutput, EventWarning, EventHandover, EventFailure, EventCoalesced,
		EventProgress,
	}
}

// Valid reports whether k is a known EventKind.
func (k EventKind) Valid() bool { return contains(AllEventKinds(), k) }

// String returns the kind as written in JSON.
func (k EventKind) String() string { return string(k) }

// StepID names a step inside a unit's run.
type StepID string

// Steps every unit run passes through.
const (
	StepDescribe StepID = "describe"
	StepPlan     StepID = "plan"
	StepBackup   StepID = "backup"
	StepIntent   StepID = "intent"
	StepApply    StepID = "apply"
	StepVerify   StepID = "verify"
	StepReceipt  StepID = "receipt"
	StepRevert   StepID = "revert"
)

// AllStepIDs returns every StepID in a stable order.
func AllStepIDs() []StepID {
	return []StepID{StepDescribe, StepPlan, StepBackup, StepIntent, StepApply, StepVerify, StepReceipt, StepRevert}
}

// Valid reports whether s is a known StepID.
func (s StepID) Valid() bool { return contains(AllStepIDs(), s) }

// String returns the step as written in JSON.
func (s StepID) String() string { return string(s) }

// DataKey is a key in an Event's Data map; keys are constants, never free text.
type DataKey string

// Keys an Event may carry.
const (
	DataPath     DataKey = "path"
	DataAction   DataKey = "action"
	DataDigest   DataKey = "digest"
	DataBackup   DataKey = "backup"
	DataArgv0    DataKey = "argv0"
	DataExitCode DataKey = "exit_code"
	DataCount    DataKey = "count"
)

// AllDataKeys returns every DataKey in a stable order.
func AllDataKeys() []DataKey {
	return []DataKey{DataPath, DataAction, DataDigest, DataBackup, DataArgv0, DataExitCode, DataCount}
}

// Valid reports whether k is a known DataKey.
func (k DataKey) Valid() bool { return contains(AllDataKeys(), k) }

// String returns the key as written in JSON.
func (k DataKey) String() string { return string(k) }

// ExitCode is a wskit process exit status (SPC-001 "Exit codes").
type ExitCode int

// Exit codes, with the meaning SPC-001 gives each.
const (
	ExitDone           ExitCode = 0   // done
	ExitFailed         ExitCode = 1   // failed
	ExitRefused        ExitCode = 2   // refused or usage
	ExitPersonMustAct  ExitCode = 3   // a person must act
	ExitRecoveryBlocks ExitCode = 4   // blocked by recovery: an interrupted step, an active executor or an armed timer
	ExitNotStagedRoot  ExitCode = 5   // not the staged binary as root
	ExitDriftFound     ExitCode = 6   // drift found
	ExitCancelled      ExitCode = 130 // cancelled
)

// AllExitCodes returns every ExitCode in ascending order.
func AllExitCodes() []ExitCode {
	return []ExitCode{
		ExitDone, ExitFailed, ExitRefused, ExitPersonMustAct, ExitRecoveryBlocks,
		ExitNotStagedRoot, ExitDriftFound, ExitCancelled,
	}
}

// Valid reports whether c is a known ExitCode.
func (c ExitCode) Valid() bool { return contains(AllExitCodes(), c) }

func contains[T comparable](values []T, v T) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}
	return false
}

// FormFactor is the machine's form factor (SPC-001 "Presets, form factor,
// topology and platform").
type FormFactor string

// Form factors.
const (
	FormFactorPortable FormFactor = "portable"
	FormFactorFixed    FormFactor = "fixed"
)

// AllFormFactors returns every FormFactor in a stable order.
func AllFormFactors() []FormFactor { return []FormFactor{FormFactorPortable, FormFactorFixed} }

// Valid reports whether f is a known FormFactor.
func (f FormFactor) Valid() bool { return contains(AllFormFactors(), f) }

// String returns the form factor as written in config and JSON.
func (f FormFactor) String() string { return string(f) }

// Topology is the account topology: whether the person and the agent use
// separate accounts or a single one.
type Topology string

// Account topologies.
const (
	TopologySeparate Topology = "separate"
	TopologySingle   Topology = "single"
)

// AllTopologies returns every Topology in a stable order.
func AllTopologies() []Topology { return []Topology{TopologySeparate, TopologySingle} }

// Valid reports whether t is a known Topology.
func (t Topology) Valid() bool { return contains(AllTopologies(), t) }

// String returns the topology as written in config and JSON.
func (t Topology) String() string { return string(t) }

// Decoding rejects unknown enum values, so a value read from JSON or config
// is always one the code knows. UnmarshalText also covers map keys.

// UnmarshalText decodes a Verb and rejects unknown values.
func (v *Verb) UnmarshalText(b []byte) error { return decodeEnum(b, v, Verb.Valid, "verb") }

// UnmarshalText decodes an Outcome and rejects unknown values.
func (o *Outcome) UnmarshalText(b []byte) error {
	return decodeEnum(b, o, Outcome.Valid, "outcome")
}

// UnmarshalText decodes an EventKind and rejects unknown values.
func (k *EventKind) UnmarshalText(b []byte) error {
	return decodeEnum(b, k, EventKind.Valid, "event kind")
}

// UnmarshalText decodes a StepID and rejects unknown values.
func (s *StepID) UnmarshalText(b []byte) error { return decodeEnum(b, s, StepID.Valid, "step") }

// UnmarshalText decodes a DataKey and rejects unknown values.
func (k *DataKey) UnmarshalText(b []byte) error {
	return decodeEnum(b, k, DataKey.Valid, "data key")
}

// UnmarshalText decodes a FormFactor and rejects unknown values.
func (f *FormFactor) UnmarshalText(b []byte) error {
	return decodeEnum(b, f, FormFactor.Valid, "form factor")
}

// UnmarshalText decodes a Topology and rejects unknown values.
func (t *Topology) UnmarshalText(b []byte) error {
	return decodeEnum(b, t, Topology.Valid, "topology")
}

// UnmarshalJSON decodes an ExitCode and rejects null and unknown values, so
// a missing code never reads as 0, which means success.
func (c *ExitCode) UnmarshalJSON(b []byte) error {
	if isJSONNull(b) {
		return fmt.Errorf("%w: exit code is null", ErrUnknownValue)
	}
	// Only a bare JSON integer is an exit code. Go 1.26's encoding/json
	// decodes a map key through UnmarshalJSON as a quoted string, the same
	// bytes as a quoted field value, so ExitCode cannot be a JSON map key:
	// no contract type uses it as one.
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("exit code: %w", err)
	}
	return c.set(n)
}

// UnmarshalText decodes an ExitCode map key and rejects unknown values.
func (c *ExitCode) UnmarshalText(b []byte) error {
	n, err := strconv.Atoi(string(b))
	if err != nil {
		return fmt.Errorf("%w: exit code %q", ErrUnknownValue, string(b))
	}
	return c.set(n)
}

func (c *ExitCode) set(n int) error {
	if !ExitCode(n).Valid() {
		return fmt.Errorf("%w: exit code %d", ErrUnknownValue, n)
	}
	*c = ExitCode(n)
	return nil
}

// ErrUnknownValue is declared in enums_gen.go, which the generator writes, so
// generated and hand-written decoders wrap the same sentinel.

func decodeEnum[T ~string](b []byte, dst *T, valid func(T) bool, name string) error {
	v := T(b)
	if !valid(v) {
		return fmt.Errorf("%w: %s %q", ErrUnknownValue, name, string(b))
	}
	*dst = v
	return nil
}

// decodeEnumJSON decodes a JSON string into a string enum. Unlike
// UnmarshalText, which encoding/json never calls for null, it refuses null,
// so a required enum field cannot be left empty or keep an old value.
func decodeEnumJSON[T ~string](b []byte, dst *T, valid func(T) bool, name string) error {
	if isJSONNull(b) {
		return fmt.Errorf("%w: %s is null", ErrUnknownValue, name)
	}
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return decodeEnum([]byte(raw), dst, valid, name)
}

func isJSONNull(b []byte) bool { return bytes.Equal(bytes.TrimSpace(b), []byte("null")) }

// UnmarshalJSON decodes a Verb and rejects null and unknown values.
func (v *Verb) UnmarshalJSON(b []byte) error { return decodeEnumJSON(b, v, Verb.Valid, "verb") }

// UnmarshalJSON decodes an Outcome and rejects null and unknown values. A
// nullable outcome is a *Outcome, which JSON null leaves nil.
func (o *Outcome) UnmarshalJSON(b []byte) error {
	return decodeEnumJSON(b, o, Outcome.Valid, "outcome")
}

// UnmarshalJSON decodes an EventKind and rejects null and unknown values.
func (k *EventKind) UnmarshalJSON(b []byte) error {
	return decodeEnumJSON(b, k, EventKind.Valid, "event kind")
}

// UnmarshalJSON decodes a StepID and rejects null and unknown values.
func (s *StepID) UnmarshalJSON(b []byte) error { return decodeEnumJSON(b, s, StepID.Valid, "step") }

// UnmarshalJSON decodes a DataKey and rejects null and unknown values.
func (k *DataKey) UnmarshalJSON(b []byte) error {
	return decodeEnumJSON(b, k, DataKey.Valid, "data key")
}

// UnmarshalJSON decodes a FormFactor and rejects null and unknown values.
func (f *FormFactor) UnmarshalJSON(b []byte) error {
	return decodeEnumJSON(b, f, FormFactor.Valid, "form factor")
}

// UnmarshalJSON decodes a Topology and rejects null and unknown values.
func (t *Topology) UnmarshalJSON(b []byte) error {
	return decodeEnumJSON(b, t, Topology.Valid, "topology")
}
