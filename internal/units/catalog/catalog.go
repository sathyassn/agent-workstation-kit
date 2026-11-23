package catalog

import "github.com/sathyassn/agent-workstation-kit/internal/contract"

// Condition identifies a conditional dependency guard in registry/units.toml.
type Condition string

// Barrier identifies a synchronisation barrier named in waits_for.
type Barrier string

// Constructor builds a unit instance. Real constructors are wired once
// internal/contract defines the Unit interface and unit packages land.
type Constructor func() (any, error)

// ConditionalDep lists units required when Condition holds on an OS.
type ConditionalDep struct {
	Condition Condition
	Units     []contract.UnitID
}

// PlatformMeta describes how a unit runs on one operating system.
type PlatformMeta struct {
	Supported bool
	Stage     contract.Stage
	Privilege contract.Privilege
	Scope     contract.Scope
	Depends   []contract.UnitID
	DependsIf []ConditionalDep
}

// Entry holds the catalog metadata for a single unit.
type Entry struct {
	ID        contract.UnitID
	Domain    contract.Domain
	Linux     PlatformMeta
	MacOS     PlatformMeta
	WaitsFor  []Barrier
	Epic      string
	Milestone string
	New       Constructor
}
