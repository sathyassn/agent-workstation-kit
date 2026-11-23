package contract

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
)

// AllEventKinds returns every EventKind in a stable order.
func AllEventKinds() []EventKind {
	return []EventKind{
		EventStepStart, EventStepEnd, EventChange, EventBackup, EventCommand,
		EventOutput, EventWarning, EventHandover, EventFailure, EventCoalesced,
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
