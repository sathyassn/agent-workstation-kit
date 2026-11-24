// Package engine implements the transaction layer (ADR 0005 section 5,
// SPC-001 "Intent, recovery and revert"). This is the minimal engine of
// TSK-013: an apply backs up every declared path, writes a durable intent
// record, runs the unit through engine.Tx, verifies, and writes a receipt;
// a failure restores. Resume recovers an interrupted apply under a recovery
// claim: it empties the run's containment, restores and reapplies. The host
// lock is taken before the journal lock, and every journal write is a
// fsynced temporary file renamed into place with its folder fsynced.
//
// Fault injection: every boundary is a Point. A binary built with the
// faultinject tag kills itself at the point named by WSKIT_FAULT_KILL_AT;
// the release build has no hook.
package engine
