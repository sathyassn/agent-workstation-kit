package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// Sentinel errors returned by the containment and process adapters. They are
// sentinels for errors.Is and are never reassigned.
var (
	// ErrContainmentNotEmpty reports that a containment still held processes
	// when its bound ran out. A package-manager transaction ends here too,
	// because it is waited for and never killed.
	ErrContainmentNotEmpty = errors.New("containment could not be emptied within its bound")
	// ErrInvalidContainmentName reports a run ID or containment name that
	// fails validation.
	ErrInvalidContainmentName = errors.New("invalid containment name")
	// ErrLaunchNotAcknowledged reports that the OS supervisor did not
	// acknowledge a launch.
	ErrLaunchNotAcknowledged = errors.New("containment launch not acknowledged")
	// ErrInvalidProcessIdentity reports an identity that lacks a field, or
	// OS output that does not parse into one.
	ErrInvalidProcessIdentity = errors.New("invalid process identity")
	// ErrCommandEmpty reports a launch with an empty argv.
	ErrCommandEmpty = errors.New("command argv is empty")
	// ErrNoUserManager reports that a user-scope containment was asked for
	// where no user service manager runs. The adapter refuses rather than
	// run the command uncontained.
	ErrNoUserManager = errors.New("no user service manager for a user-scope containment")
	// ErrSupervisorState reports output from the OS supervisor that the
	// adapter cannot interpret, so it cannot tell whether a containment is
	// empty. Callers treat it as not empty.
	ErrSupervisorState = errors.New("supervisor state unknown")
	// ErrIdentityChanged reports a PID that now belongs to a process with
	// another start time; nothing is signalled.
	ErrIdentityChanged = errors.New("process identity changed; not signalled")
	// ErrForbiddenEnv reports a tool variable that would steer the dynamic
	// loader or the supervisor itself: LD_*, DYLD_* or SYSTEMD_*.
	ErrForbiddenEnv = errors.New("environment variable not allowed for a tool")
	// ErrUnsafeDir reports a private folder that is missing, not a folder,
	// or writable by others.
	ErrUnsafeDir = errors.New("folder is not private")
)

// validRunID bounds run IDs to characters that are safe in a systemd unit
// name, a launchd label and a file name. It is compiled once and never
// changed.
var validRunID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateRunID checks that runID is safe to use in containment names and
// file paths.
func ValidateRunID(runID contract.RunID) error {
	if !validRunID.MatchString(string(runID)) {
		return fmt.Errorf("%w: run ID %q", ErrInvalidContainmentName, runID)
	}
	return nil
}

// ProcessIdentity names one process instance. The PID alone is reused by the
// OS; the boot ID and the start time make the identity unique (SPC-001
// "Intent, recovery and revert").
type ProcessIdentity struct {
	BootID    string `json:"boot_id"`
	PID       int    `json:"pid"`
	StartTime string `json:"start_time"`
}

// Valid reports whether id carries all three fields.
func (id ProcessIdentity) Valid() bool {
	return id.BootID != "" && id.PID > 0 && id.StartTime != ""
}

// LaunchSpec is one command started inside a named containment.
type LaunchSpec struct {
	// Name is the containment name, written into the intent record before
	// the launch.
	Name string
	// Scope selects the system manager (root) or the user's manager.
	Scope contract.Scope
	Argv  []string
	// Env is the complete environment; nothing is inherited.
	Env []string
	Dir string
	// PackageTransaction marks a package-manager transaction. Adapters never
	// signal one; the engine also runs it with a context that cancel and
	// grace cannot end.
	PackageTransaction bool
	// Output receives the tool's output. It may be nil.
	Output io.Writer
}

// LaunchAck names the supervisor process an adapter started for a launch.
// The adapter holds the supervisor at a gate until the callback that
// receives this has made it durable; only then may anything run.
type LaunchAck struct {
	Name       string
	Supervisor ProcessIdentity
}

// ContainmentState is what the OS supervisor reports for a containment.
type ContainmentState int

// Containment states.
const (
	// StateGone: not loaded, or loaded with no process left.
	StateGone ContainmentState = iota
	// StatePending: loaded and due to start; it may start at any moment.
	StatePending
	// StateActive: it holds at least one process.
	StateActive
)

// ProcessControl inspects and signals a process by its full identity, so a
// reused PID is never signalled.
type ProcessControl interface {
	Alive(ctx context.Context, id ProcessIdentity) (bool, error)
	Signal(ctx context.Context, id ProcessIdentity, sig syscall.Signal) error
}

// EmptyOptions bounds how a containment is emptied: wait for Grace, then
// terminate, then kill, each step bounded. A package-manager transaction is
// only waited for, for Grace plus KillWait, and never signalled.
type EmptyOptions struct {
	Scope              contract.Scope
	Grace              time.Duration
	KillWait           time.Duration
	PollInterval       time.Duration
	PackageTransaction bool
	// Supervisor is the launch's recorded supervisor. While it lives, the
	// containment is not empty: it may still register or start the tool.
	// Zero means the launch gate never opened, so nothing ran.
	Supervisor ProcessIdentity
	// CallTimeout bounds every single inspection and signal. Zero is ten
	// seconds.
	CallTimeout time.Duration
}

// ForbiddenEnvKey reports whether a tool variable could steer the dynamic
// loader or the supervisor that starts the tool: LD_*, DYLD_* or SYSTEMD_*.
func ForbiddenEnvKey(key string) bool {
	for _, p := range []string{"LD_", "DYLD_", "SYSTEMD_"} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// CheckToolEnv refuses a tool environment holding a forbidden key.
func CheckToolEnv(env []string) error {
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if ForbiddenEnvKey(k) {
			return fmt.Errorf("%w: %s (LD_*, DYLD_* and SYSTEMD_* are refused)", ErrForbiddenEnv, k)
		}
	}
	return nil
}
