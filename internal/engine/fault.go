package engine

// Point names a boundary in the executor. The fault-injection harness kills
// the process at a chosen point, or at the Nth point reached; tests also use
// points to pause a run.
type Point string

// The executor's boundaries, in the order an apply reaches them.
const (
	// PointAtomicTemp: a temporary file is written and fsynced, not yet
	// renamed into place.
	PointAtomicTemp Point = "atomic.temp"
	// PointAtomicRenamed: the rename is done, the folder not yet fsynced.
	PointAtomicRenamed Point = "atomic.renamed"
	// PointBackupEntry: one declared path is backed up.
	PointBackupEntry Point = "backup.entry"
	// PointIntent: the apply record is durable; nothing is changed yet.
	PointIntent Point = "intent.written"
	// PointMutation: one target change is durable.
	PointMutation Point = "tx.mutation"
	// PointLaunch: the containment name is durable; the tool is not started.
	PointLaunch Point = "tx.launch"
	// PointCommandDone: a tool has ended.
	PointCommandDone Point = "tx.command-done"
	// PointCommandEmptied: a tool's containment is proven empty and the
	// command is durably finished.
	PointCommandEmptied Point = "tx.command-emptied"
	// PointApplied: the unit's Apply has returned.
	PointApplied Point = "apply.returned"
	// PointVerified: Verify has returned; no receipt yet.
	PointVerified Point = "verify.returned"
	// PointReceipt: the receipt is durable; the record still says applying.
	PointReceipt Point = "receipt.written"
	// PointIdle: the record says idle.
	PointIdle Point = "record.idle"
	// PointRestoreStart: the record says restoring.
	PointRestoreStart Point = "restore.start"
	// PointRestoreEntry: one manifest entry is restored.
	PointRestoreEntry Point = "restore.entry"
	// PointRestored: the restore and its failed receipt are durable.
	PointRestored Point = "restore.done"
	// PointClaimed: a recovery claim is durable.
	PointClaimed Point = "recover.claimed"
	// PointEmptied: the interrupted run's containment is empty.
	PointEmptied Point = "recover.emptied"
	// PointRecoverLocked: the recoverer holds the execution locks and has
	// re-read the record.
	PointRecoverLocked Point = "recover.locked"
)

// FaultFunc is called at every Point. The release build passes nil.
type FaultFunc func(Point)
