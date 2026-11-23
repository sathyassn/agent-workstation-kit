package contract

import (
	"context"
	"io/fs"
	"time"
)

// This file publishes SPC-001's contracts ("Interfaces and formats"). It holds
// types and interfaces only, with no behaviour: the engine, units and platform
// adapters each import this package, and nothing here imports them.

// Unit is one independently plannable, appliable and revertible piece of
// setup. Units never print and never exit; they report through Tx.Emit.
type Unit interface {
	ID() UnitID
	Describe(ctx context.Context) (Description, error)
	Plan(ctx context.Context, in Inputs) (Plan, error)
	Apply(ctx context.Context, tx Tx, p Plan) error
	Verify(ctx context.Context, p Plan) (Result, error)
	Revert(ctx context.Context, tx Tx, r Receipt) error
	Status(ctx context.Context) (Status, error)
}

// DeclarativeTx is what declarative units get: typed operations only, no Run.
// Every mutating call refuses a path or state not declared in Describe, backs
// it up first, and records it for the receipt.
type DeclarativeTx interface {
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode, owner Owner) error
	RenderTemplate(ctx context.Context, path string, tpl TemplateID, vars TemplateVars, perm fs.FileMode, owner Owner) error
	EnsureBlock(ctx context.Context, path string, b ManagedBlock) error
	RemoveBlock(ctx context.Context, path string, id BlockID) error
	Remove(ctx context.Context, path string) error
	Package(ctx context.Context, want PackageState) error
	Service(ctx context.Context, want ServiceState) error
	UserGroup(ctx context.Context, want UserGroupState) error
	Check(ctx context.Context, c CheckSpec) (CheckResult, error)
	Emit(e Event)
}

// Tx is what kit units get: the typed operations plus SetState and Run.
type Tx interface {
	DeclarativeTx
	SetState(ctx context.Context, p StateProvider, key StateKey, want State) error
	// Run executes argv with only the declared environment, a timeout, and
	// inside the run's containment.
	Run(ctx context.Context, c Command) (CommandResult, error)
}

// Sink receives events. Deliver never blocks; a slow sink is coalesced or
// spilled by the engine (SPC-001 "Events").
type Sink interface {
	Deliver(e Event)
}

// StateProvider is a named source of machine state that is not a file, such
// as the hostname, the firewall or sysctl values.
type StateProvider interface {
	Name() StateProviderName
	Read(ctx context.Context, key StateKey) (State, error)
	Snapshot(ctx context.Context) (Snapshot, error)
	Restore(ctx context.Context, s Snapshot) error
}

// Identifiers. Each is a distinct type so values cannot be mixed up.
type (
	// RunID identifies one wskit run.
	RunID string
	// PlanID identifies one plan.
	PlanID string
	// Digest is a lowercase hex SHA-256 of canonical content.
	Digest string
	// ConfigKey is a dotted configuration key, such as "network.ssh.port".
	ConfigKey string
	// Capability names something a unit needs from the platform.
	Capability string
	// StateKey names one value inside a StateProvider.
	StateKey string
	// StateProviderName names a StateProvider.
	StateProviderName string
	// TemplateID names an embedded template.
	TemplateID string
	// BlockID names a managed block inside a file the kit does not own.
	BlockID string
	// BackupRef points to a backup taken before a change.
	BackupRef string
	// CheckName names a read-only check a unit can ask for.
	CheckName string
)

// State providers named in SPC-001.
const (
	ProviderHostname    StateProviderName = "hostname"
	ProviderUFW         StateProviderName = "ufw"
	ProviderUnitMasks   StateProviderName = "unit-masks"
	ProviderSysctl      StateProviderName = "sysctl"
	ProviderPackages    StateProviderName = "packages"
	ProviderAccounts    StateProviderName = "accounts"
	ProviderLaunchd     StateProviderName = "launchd"
	ProviderAppFirewall StateProviderName = "app-firewall"
)

// Description is what a unit declares before anything runs: the only paths,
// state keys and environment it may touch.
type Description struct {
	Unit           UnitID       `json:"unit"`
	Scope          Scope        `json:"scope"`
	DependsOn      []UnitID     `json:"depends_on"`
	Capabilities   []Capability `json:"capabilities"`
	OwnedPaths     []string     `json:"owned_paths"`
	OwnedStateKeys []StateKey   `json:"owned_state_keys"`
	Irreversible   []string     `json:"irreversible"`
	DeclaredEnv    []string     `json:"declared_env"`
}

// PlatformFacts are the facts about the machine a plan depends on.
type PlatformFacts struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	OSVersion string `json:"os_version"`
}

// Inputs are everything a unit's Plan reads.
type Inputs struct {
	Config     map[ConfigKey]string `json:"config"`
	Platform   PlatformFacts        `json:"platform"`
	FormFactor FormFactor           `json:"form_factor"`
	Topology   Topology             `json:"topology"`
}

// ChangeAction is what a planned change does to its target.
type ChangeAction string

// Change actions.
const (
	ActionCreate  ChangeAction = "create"
	ActionReplace ChangeAction = "replace"
	ActionRemove  ChangeAction = "remove"
	ActionSet     ChangeAction = "set"
	ActionNone    ChangeAction = "none"
)

// AllChangeActions returns every ChangeAction in a stable order.
func AllChangeActions() []ChangeAction {
	return []ChangeAction{ActionCreate, ActionReplace, ActionRemove, ActionSet, ActionNone}
}

// Valid reports whether a is a known ChangeAction.
func (a ChangeAction) Valid() bool { return contains(AllChangeActions(), a) }

// UnmarshalText decodes a ChangeAction and rejects unknown values.
func (a *ChangeAction) UnmarshalText(b []byte) error {
	return decodeEnum(b, a, ChangeAction.Valid, "change action")
}

// Risk grades a change for the preview.
type Risk string

// Risk levels.
const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// AllRisks returns every Risk in a stable order.
func AllRisks() []Risk { return []Risk{RiskLow, RiskMedium, RiskHigh} }

// Valid reports whether r is a known Risk.
func (r Risk) Valid() bool { return contains(AllRisks(), r) }

// UnmarshalText decodes a Risk and rejects unknown values.
func (r *Risk) UnmarshalText(b []byte) error { return decodeEnum(b, r, Risk.Valid, "risk") }

// Change is one planned change to one target.
type Change struct {
	Target string       `json:"target"`
	Action ChangeAction `json:"action"`
	Before Digest       `json:"before,omitempty"`
	After  Digest       `json:"after,omitempty"`
	Risk   Risk         `json:"risk"`
}

// Plan is what a unit would do, fixed by its digest before approval.
type Plan struct {
	ID          PlanID      `json:"id"`
	Unit        UnitID      `json:"unit"`
	Digest      Digest      `json:"digest"`
	Changes     []Change    `json:"changes"`
	NonDefaults []ConfigKey `json:"non_defaults"`
	BlockedBy   []string    `json:"blocked_by"`
	CannotUndo  []string    `json:"cannot_undo"`
	Outcome     *Outcome    `json:"outcome"`
}

// Result is what Verify found.
type Result struct {
	Unit    UnitID        `json:"unit"`
	Outcome Outcome       `json:"outcome"`
	Checks  []CheckResult `json:"checks"`
}

// AppliedChange is a change that was made, with the backup taken first.
type AppliedChange struct {
	Change
	Backup BackupRef `json:"backup,omitempty"`
}

// Receipt records what an apply did, so revert can undo exactly that.
type Receipt struct {
	RunID      RunID           `json:"run_id"`
	Unit       UnitID          `json:"unit"`
	PlanDigest Digest          `json:"plan_digest"`
	Changes    []AppliedChange `json:"changes"`
	Time       time.Time       `json:"time"`
	Outcome    Outcome         `json:"outcome"`
}

// Status is a unit's current state on the machine.
type Status struct {
	Unit        UnitID   `json:"unit"`
	Applied     bool     `json:"applied"`
	Drift       []string `json:"drift"`
	LastReceipt *Receipt `json:"last_receipt,omitempty"`
}

// Command is one process a kit unit runs through Tx.Run.
type Command struct {
	Argv               []string      `json:"argv"`
	EnvKeys            []string      `json:"env_keys"`
	Dir                string        `json:"dir"`
	Timeout            time.Duration `json:"timeout"`
	PackageTransaction bool          `json:"package_transaction"`
}

// CommandResult is how a Command ended. Output is reported through events,
// redacted, never returned raw.
type CommandResult struct {
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
}

// State is one value read from or written to a StateProvider.
type State struct {
	Value string `json:"value"`
}

// Snapshot is an opaque copy of a StateProvider's state, for Restore.
type Snapshot struct {
	Provider StateProviderName `json:"provider"`
	Data     []byte            `json:"data"`
}

// Owner is the numeric owner of a file the kit writes.
type Owner struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

// TemplateVars are the values a template is rendered with.
type TemplateVars map[string]string

// ManagedBlock is the kit's marked region inside a file it does not own.
// Content outside Begin and End stays byte-identical.
type ManagedBlock struct {
	ID    BlockID `json:"id"`
	Begin string  `json:"begin"`
	End   string  `json:"end"`
	Body  string  `json:"body"`
}

// PackageState is the wanted state of one package.
type PackageState struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Version string `json:"version,omitempty"`
}

// ServiceState is the wanted state of one service.
type ServiceState struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
}

// UserGroupState is the wanted membership of one user in one group.
type UserGroupState struct {
	User    string `json:"user"`
	Group   string `json:"group"`
	Present bool   `json:"present"`
}

// CheckSpec names a read-only check a unit asks the transaction to run.
// DeclarativeTx has typed operations only, so a check is a name the engine
// resolves, never a command line.
type CheckSpec struct {
	Name CheckName `json:"name"`
}

// CheckResult is the result of one check.
type CheckResult struct {
	Name   CheckName `json:"name"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail"`
}

// Event is one line of a run's event stream.
type Event struct {
	Seq   uint64             `json:"seq"`
	Time  time.Time          `json:"time"`
	RunID RunID              `json:"run_id"`
	Unit  UnitID             `json:"unit"`
	Step  StepID             `json:"step"`
	Kind  EventKind          `json:"kind"`
	Text  string             `json:"text"` // redacted, one line
	Data  map[DataKey]string `json:"data"` // keys are constants
}
