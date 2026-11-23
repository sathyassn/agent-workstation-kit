package contract

// This file defines the plain JSON value types for the published CLI schemas
// that do not already have a Go contract type in contract.go: next, handover,
// failure, explain and error (SPC-001 "Interfaces and formats").

// HandoverStatus is the lifecycle state of a human hand-over.
type HandoverStatus string

// Hand-over statuses from SPC-001.
const (
	HandoverOpen    HandoverStatus = "open"
	HandoverBlocked HandoverStatus = "blocked"
	HandoverDone    HandoverStatus = "done"
)

// AllHandoverStatuses returns every HandoverStatus in a stable order.
func AllHandoverStatuses() []HandoverStatus {
	return []HandoverStatus{HandoverOpen, HandoverBlocked, HandoverDone}
}

// Valid reports whether s is a known HandoverStatus.
func (s HandoverStatus) Valid() bool { return contains(AllHandoverStatuses(), s) }

// String returns the hand-over status as written in JSON.
func (s HandoverStatus) String() string { return string(s) }

// UnmarshalText decodes a HandoverStatus and rejects unknown values.
func (s *HandoverStatus) UnmarshalText(b []byte) error {
	return decodeEnum(b, s, HandoverStatus.Valid, "handover status")
}

// HandoverCommand is the command a person runs to complete a hand-over step.
type HandoverCommand struct {
	Argv    []string `json:"argv"`
	Display string   `json:"display"`
}

// HandoverVerify is the read-only command run afterwards to confirm a hand-over.
type HandoverVerify struct {
	Argv []string `json:"argv"`
}

// Probe is a read-only command that checks whether a gate or hand-over has cleared.
type Probe struct {
	Argv []string `json:"argv"`
}

// Handover is a structured hand-over block for a gate, root step, revert or
// proposed change that requires a person (handover.schema.json).
type Handover struct {
	SchemaVersion int             `json:"schema_version"`
	Machine       string          `json:"machine"`
	Account       string          `json:"account"`
	Step          string          `json:"step"`
	Command       HandoverCommand `json:"command"`
	Why           string          `json:"why"`
	Verify        HandoverVerify  `json:"verify"`
	Undo          *string         `json:"undo"`
	Probe         *Probe          `json:"probe"`
	Status        HandoverStatus  `json:"status"`
}

// Next is the next step returned by wskit next --json for the conductor loop
// (next.schema.json).
type Next struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         RunID     `json:"run_id"`
	Stage         *Stage    `json:"stage"`
	Unit          *UnitID   `json:"unit"`
	Owner         *string   `json:"owner"`
	Root          bool      `json:"root"`
	Gate          *string   `json:"gate"`
	PreviewArgv   []string  `json:"preview_argv"`
	ApplyArgv     []string  `json:"apply_argv"`
	Handover      *Handover `json:"handover"`
	Probe         *Probe    `json:"probe"`
	LogPath       *string   `json:"log_path"`
	BlockedBy     []string  `json:"blocked_by"`
	Outcome       *Outcome  `json:"outcome"`
}

// Failure is the structured record of a failed unit step (failure.schema.json).
type Failure struct {
	SchemaVersion int        `json:"schema_version"`
	Unit          UnitID     `json:"unit"`
	Step          StepID     `json:"step"`
	ErrorClass    string     `json:"error_class"`
	Message       string     `json:"message"`
	Suggested     string     `json:"suggested"`
	Log           string     `json:"log"`
	Backup        *BackupRef `json:"backup"`
}

// ExplainSource is one primary source entry in an Explain response.
type ExplainSource struct {
	URL          string `json:"url"`
	Purpose      string `json:"purpose"`
	LastVerified string `json:"last_verified"`
}

// Explain is the structured explanation for a unit or configuration field
// returned by wskit explain --json (explain.schema.json).
type Explain struct {
	SchemaVersion    int             `json:"schema_version"`
	Subject          string          `json:"subject"`
	What             string          `json:"what"`
	Why              string          `json:"why"`
	Options          []string        `json:"options"`
	Default          any             `json:"default"`
	Risk             string          `json:"risk"`
	Undo             string          `json:"undo"`
	ManualEquivalent string          `json:"manual_equivalent"`
	Sources          []ExplainSource `json:"sources"`
}

// CLIError is the structured CLI error or refusal returned in JSON mode
// (error.schema.json).
type CLIError struct {
	SchemaVersion int      `json:"schema_version"`
	Code          ExitCode `json:"code"`
	Class         string   `json:"class"`
	Message       string   `json:"message"`
	Rule          *string  `json:"rule"`
}
