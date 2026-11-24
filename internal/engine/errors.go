package engine

import (
	"errors"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// Sentinel errors. They are for errors.Is and are never reassigned.
var (
	// ErrActiveExecutor reports that the journal's record belongs to an
	// executor that is still running, told by its boot ID, PID and start
	// time. Exit 4.
	ErrActiveExecutor = errors.New("another executor is running this journal")
	// ErrRecoveryClaimed reports a live recovery claim by another starter.
	// Exit 4.
	ErrRecoveryClaimed = errors.New("another starter is recovering this journal")
	// ErrRecoveryPending reports an interrupted run that resume must handle
	// first. Exit 4.
	ErrRecoveryPending = errors.New("an interrupted run must be resumed first")
	// ErrCancelled reports a step stopped by a cancel request at the end of
	// its grace period. The intent record is left for the next start.
	// Exit 130.
	ErrCancelled = errors.New("cancelled")
	// ErrEmptinessUnknown reports a containment that could not be proven
	// empty. Nothing is restored over processes that may still run: the
	// record stays unfinished for the next start. Exit 4.
	ErrEmptinessUnknown = errors.New("containment not proven empty")
	// ErrApplyFailed reports an apply that failed and was restored; a
	// failed receipt records it. Exit 1.
	ErrApplyFailed = errors.New("apply failed and was restored")
	// ErrVerifyFailed reports a Verify that did not return verified.
	ErrVerifyFailed = errors.New("verify did not pass")
	// ErrEventLog reports that an event could not be written, so the step
	// fails closed (SPC-001 "Events").
	ErrEventLog = errors.New("event log write failed")
	// ErrUnsupported reports a Tx operation this engine does not provide
	// yet. Declared-target checks still run first.
	ErrUnsupported = errors.New("operation not supported by this engine")
	// ErrUnsupportedTarget reports a declared path that is neither a regular
	// file nor absent, so it cannot be backed up.
	ErrUnsupportedTarget = errors.New("target cannot be backed up")
	// ErrCorruptRecord reports a journal file that does not decode into a
	// known record. The engine refuses to guess.
	ErrCorruptRecord = errors.New("journal record is corrupt")
	// ErrBackupMismatch reports a backup whose digest no longer matches its
	// manifest entry. Exit 4, intent left unfinished.
	ErrBackupMismatch = errors.New("backup does not match its manifest")
	// ErrRestoreMismatch reports restored content, metadata or state that
	// failed verification against the manifest. Exit 4, intent left
	// unfinished.
	ErrRestoreMismatch = errors.New("restored target does not match its manifest")
	// ErrRestoreValidation reports a post-restore validator that failed.
	// Exit 4, intent left unfinished.
	ErrRestoreValidation = errors.New("post-restore validation failed")
	// ErrRestoreFailed wraps any failure during the restore phase so every
	// restore error maps to exit 4 with the intent left unfinished.
	ErrRestoreFailed = errors.New("restore failed; intent left unfinished")
	// ErrUnregisteredProvider reports SetState called with a StateProvider
	// not registered in Options.Providers (so recovery could not replay it).
	ErrUnregisteredProvider = errors.New("state provider is not registered on this engine")
	// ErrPackageDBPath reports a declared or manifest path inside a
	// package-manager database; packages are changed and reverted only
	// through the package manager.
	ErrPackageDBPath = errors.New("package-database path cannot be backed up or restored as a file")
	// ErrPackageDBScan reports that the startup package-database directory
	// walk could not complete (for example due to hitting the directory cap or
	// an unexpected OS read/fd error), failing closed rather than silently
	// leaving package-database paths unguarded.
	ErrPackageDBScan = errors.New("package-database directory scan incomplete")
	// ErrTargetChanged reports a target whose device, inode, file type or size
	// changed between stat and open/read.
	ErrTargetChanged = errors.New("target changed between stat and open")
	// ErrHardLinkTarget reports a regular-file target with multiple hard links
	// (Nlink > 1), which is unsafe to back up, replace or remove as a single
	// path.
	ErrHardLinkTarget = errors.New("target has multiple hard links")
	// ErrProtectedMetadata reports a target carrying OS-protected metadata
	// (such as macOS SIP com.apple.rootless) that cannot be preserved across
	// replacement or restore.
	ErrProtectedMetadata = errors.New("target carries OS-protected metadata that cannot be preserved")
	// ErrNilStateProvider reports SetState called with a nil StateProvider.
	ErrNilStateProvider = errors.New("state provider is nil")
	// ErrStateRestoreFailed reports that SetState could not restore the
	// provider's snapshot after a failed Write, so the provider may hold a
	// partial write.
	ErrStateRestoreFailed = errors.New("restore after failed state write")
	// ErrPlanChanged reports that the plan resolved for a reapply has a
	// different digest from the interrupted run's plan.
	ErrPlanChanged = errors.New("plan changed since the interrupted run")
	// ErrBlockMarkers reports managed-block markers that are missing,
	// repeated or out of order.
	ErrBlockMarkers = errors.New("managed block markers are invalid")
	// ErrOwnerChange reports a requested owner this process cannot set.
	ErrOwnerChange = errors.New("cannot set the requested owner")
	// ErrLockOrder reports an attempt to take the host lock while holding
	// the journal lock (SPC-001 "Locks").
	ErrLockOrder = errors.New("host lock requested while holding the journal lock")
	// ErrInvalidOptions reports engine options that cannot work.
	ErrInvalidOptions = errors.New("invalid engine options")
	// ErrUnitMismatch reports a plan or description for another unit.
	ErrUnitMismatch = errors.New("plan or description names another unit")
)

// ExitCode maps an engine error to the process exit code (SPC-001 "Exit
// codes"). Only cmd/ calls os.Exit with it.
func ExitCode(err error) contract.ExitCode {
	switch {
	case err == nil:
		return contract.ExitDone
	case errors.Is(err, ErrCancelled):
		return contract.ExitCancelled
	case errors.Is(err, ErrActiveExecutor),
		errors.Is(err, ErrRecoveryClaimed),
		errors.Is(err, ErrRecoveryPending),
		errors.Is(err, ErrEmptinessUnknown),
		errors.Is(err, ErrRestoreFailed),
		errors.Is(err, ErrBackupMismatch),
		errors.Is(err, ErrRestoreMismatch),
		errors.Is(err, ErrRestoreValidation),
		errors.Is(err, platform.ErrContainmentNotEmpty),
		errors.Is(err, platform.ErrSupervisorState):
		return contract.ExitRecoveryBlocks
	case errors.Is(err, contract.ErrUndeclaredTarget),
		errors.Is(err, ErrPackageDBPath),
		errors.Is(err, ErrPackageDBScan),
		errors.Is(err, ErrTargetChanged),
		errors.Is(err, ErrHardLinkTarget),
		errors.Is(err, ErrProtectedMetadata),
		errors.Is(err, ErrUnregisteredProvider),
		errors.Is(err, platform.ErrForbiddenEnv):
		return contract.ExitRefused
	default:
		return contract.ExitFailed
	}
}
