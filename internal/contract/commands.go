package contract

// CommandScope names the privilege or ownership scope in SPC-001's command
// matrix.
type CommandScope string

// Command scopes from SPC-001's command matrix.
const (
	CommandScopeAny        CommandScope = "any"
	CommandScopeUser       CommandScope = "user"
	CommandScopeRoot       CommandScope = "root"
	CommandScopePerJournal CommandScope = "per journal"
	CommandScopePerUnit    CommandScope = "per unit"
)

// AllCommandScopes returns every CommandScope in a stable order.
func AllCommandScopes() []CommandScope {
	return []CommandScope{
		CommandScopeAny,
		CommandScopeUser,
		CommandScopeRoot,
		CommandScopePerJournal,
		CommandScopePerUnit,
	}
}

// Valid reports whether s is a known CommandScope.
func (s CommandScope) Valid() bool { return contains(AllCommandScopes(), s) }

// String returns s as written in SPC-001.
func (s CommandScope) String() string { return string(s) }

// RunsAs describes the execution account requirement for a CLI command.
type RunsAs string

// Execution account rules from SPC-001's command matrix.
const (
	RunsAsAnyAccount           RunsAs = "any account"
	RunsAsTargetAccount        RunsAs = "that account"
	RunsAsSudoStagedBinary     RunsAs = "sudo staged binary"
	RunsAsJournalAccount       RunsAs = "that journal's account"
	RunsAsSudoOtherSession     RunsAs = "sudo, another login session"
	RunsAsUnitAccountDraftRoot RunsAs = "the unit's account; root and fleet configs draft and stop"
)

// AllRunsAs returns every RunsAs value in a stable order.
func AllRunsAs() []RunsAs {
	return []RunsAs{
		RunsAsAnyAccount,
		RunsAsTargetAccount,
		RunsAsSudoStagedBinary,
		RunsAsJournalAccount,
		RunsAsSudoOtherSession,
		RunsAsUnitAccountDraftRoot,
	}
}

// Valid reports whether r is a known RunsAs rule.
func (r RunsAs) Valid() bool { return contains(AllRunsAs(), r) }

// String returns r as written in SPC-001.
func (r RunsAs) String() string { return string(r) }

// CommandMode is an execution mode supported by a CLI command.
type CommandMode string

// Execution modes from SPC-001's command matrix.
const (
	ModeReadOnly          CommandMode = "read only"
	ModePreview           CommandMode = "preview"
	ModeApply             CommandMode = "--apply"
	ModeApplyWithApproval CommandMode = "--apply with the approval phrase"
	ModePreviewForWrites  CommandMode = "preview for writes"
)

// AllCommandModes returns every CommandMode in a stable order.
func AllCommandModes() []CommandMode {
	return []CommandMode{
		ModeReadOnly,
		ModePreview,
		ModeApply,
		ModeApplyWithApproval,
		ModePreviewForWrites,
	}
}

// Valid reports whether m is a known CommandMode.
func (m CommandMode) Valid() bool { return contains(AllCommandModes(), m) }

// String returns m as written in SPC-001.
func (m CommandMode) String() string { return string(m) }

// NonUnitCommand names a documented CLI command in SPC-001's command matrix
// that is not a unit Verb.
type NonUnitCommand string

// Documented non-unit CLI commands from SPC-001's command matrix.
const (
	CmdSetup     NonUnitCommand = "setup"
	CmdConfig    NonUnitCommand = "config"
	CmdInterview NonUnitCommand = "interview"
	CmdMigrate   NonUnitCommand = "migrate"
)

// AllNonUnitCommands returns every NonUnitCommand in a stable order.
func AllNonUnitCommands() []NonUnitCommand {
	return []NonUnitCommand{CmdSetup, CmdConfig, CmdInterview, CmdMigrate}
}

// Valid reports whether c is a known NonUnitCommand.
func (c NonUnitCommand) Valid() bool { return contains(AllNonUnitCommands(), c) }

// String returns c as written on the command line.
func (c NonUnitCommand) String() string { return string(c) }

// CommandSpec is one row of SPC-001's command matrix (who may run what).
type CommandSpec struct {
	Command  string
	Verbs    []Verb
	NonUnits []NonUnitCommand
	Scope    CommandScope
	RunsAs   RunsAs
	Modes    []CommandMode
}

// CommandMatrix returns a fresh copy of SPC-001's command matrix, one row per
// table line.
func CommandMatrix() []CommandSpec {
	return []CommandSpec{
		{
			Command: "plan, explain, status, next, doctor",
			Verbs:   []Verb{VerbPlan, VerbExplain, VerbStatus, VerbNext, VerbDoctor},
			Scope:   CommandScopeAny,
			RunsAs:  RunsAsAnyAccount,
			Modes:   []CommandMode{ModeReadOnly},
		},
		{
			Command:  "setup --stage S, UNIT apply|revert (user units)",
			Verbs:    []Verb{VerbApply, VerbRevert},
			NonUnits: []NonUnitCommand{CmdSetup},
			Scope:    CommandScopeUser,
			RunsAs:   RunsAsTargetAccount,
			Modes:    []CommandMode{ModePreview, ModeApply},
		},
		{
			Command:  "setup --stage S, UNIT apply|revert (root units)",
			Verbs:    []Verb{VerbApply, VerbRevert},
			NonUnits: []NonUnitCommand{CmdSetup},
			Scope:    CommandScopeRoot,
			RunsAs:   RunsAsSudoStagedBinary,
			Modes:    []CommandMode{ModePreview, ModeApplyWithApproval},
		},
		{
			Command: "resume, revert --to",
			Verbs:   []Verb{VerbResume, VerbRevert},
			Scope:   CommandScopePerJournal,
			RunsAs:  RunsAsJournalAccount,
			Modes:   []CommandMode{ModePreview, ModeApply},
		},
		{
			Command: "disarm",
			Verbs:   []Verb{VerbDisarm},
			Scope:   CommandScopeRoot,
			RunsAs:  RunsAsSudoOtherSession,
			Modes:   []CommandMode{ModeApply},
		},
		{
			Command: "adopt, unmanage",
			Verbs:   []Verb{VerbAdopt, VerbUnmanage},
			Scope:   CommandScopePerUnit,
			RunsAs:  RunsAsUnitAccountDraftRoot,
			Modes:   []CommandMode{ModePreview, ModeApply},
		},
		{
			Command:  "config get|set|explain|validate, interview, migrate",
			NonUnits: []NonUnitCommand{CmdConfig, CmdInterview, CmdMigrate},
			Scope:    CommandScopeUser,
			RunsAs:   RunsAsAnyAccount,
			Modes:    []CommandMode{ModePreviewForWrites},
		},
	}
}
