package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

const (
	recordVersionV1 = 1
	recordVersion   = 2
	recordFile      = "record.json"
	journalLock     = "journal.lock"
	receiptsDir     = "receipts"
	backupsDir      = "backups"
	manifestFile    = "manifest.json"
	privateMode     = 0o700
	jsonExt         = ".json"
)

// Operation names what a record is for (SPC-001 "Intent, recovery and
// revert"). This engine writes apply records; revert is TSK-020.
type Operation string

// Operations.
const (
	OpApply Operation = "apply"
)

// Status is where an operation stands.
type Status string

// Statuses of an apply record.
const (
	// StatusApplying: the record was written before any change.
	StatusApplying Status = "applying"
	// StatusRestoring: a restore from the manifest has started.
	StatusRestoring Status = "restoring"
	// StatusRestored: an interrupted run is restored and a reapply is due.
	StatusRestored Status = "restored"
	// StatusIdle: nothing is in progress.
	StatusIdle Status = "idle"
)

func (s Status) valid() bool {
	switch s {
	case StatusApplying, StatusRestoring, StatusRestored, StatusIdle:
		return true
	default:
		return false
	}
}

// CommandRecord names the containment of the command a run started last.
// It is written before the launch, so a restart can always empty it. The
// supervisor's identity is written before the launch gate opens, so a
// command with no supervisor recorded never ran. Finished is written only
// once the containment is proven empty.
type CommandRecord struct {
	Name               string                   `json:"name"`
	PackageTransaction bool                     `json:"package_transaction"`
	Supervisor         platform.ProcessIdentity `json:"supervisor"`
	Finished           bool                     `json:"finished"`
}

// Claim is a recovery claim: only its claimant may empty the containment
// and restore while it is live.
type Claim struct {
	RunID    contract.RunID           `json:"run_id"`
	Claimant platform.ProcessIdentity `json:"claimant"`
	Expires  time.Time                `json:"expires"`
}

// PlanDigestKind records how Record.PlanDigest was computed when an apply
// started so Resume compares on the exact same basis instead of guessing from
// the digest string's shape.
type PlanDigestKind string

// Plan digest kinds.
const (
	// PlanDigestVerbatim means PlanDigest came verbatim from the caller's
	// contract.Plan (for example a unit or test calling Apply without
	// Engine.Plan).
	PlanDigestVerbatim PlanDigestKind = "verbatim"
	// PlanDigestCanonical means PlanDigest is the RFC 8785 SHA-256 canonical
	// digest produced by Engine.Plan.
	PlanDigestCanonical PlanDigestKind = "canonical"
)

func (k PlanDigestKind) valid() bool {
	switch k {
	case "", PlanDigestVerbatim, PlanDigestCanonical:
		return true
	default:
		return false
	}
}

// Record is the journal's intent record.
type Record struct {
	Version          int                      `json:"version"`
	Op               Operation                `json:"op"`
	Status           Status                   `json:"status"`
	RunID            contract.RunID           `json:"run_id"`
	Unit             contract.UnitID          `json:"unit"`
	PlanID           contract.PlanID          `json:"plan_id"`
	PlanDigest       contract.Digest          `json:"plan_digest"`
	PlanDigestKind   PlanDigestKind           `json:"plan_digest_kind,omitempty"`
	PlanDroppedNotes []string                 `json:"plan_dropped_notes,omitempty"`
	Scope            contract.Scope           `json:"scope"`
	Executor         platform.ProcessIdentity `json:"executor"`
	CmdSeq           int                      `json:"cmd_seq,omitempty"`
	Command          *CommandRecord           `json:"command,omitempty"`
	Claim            *Claim                   `json:"claim,omitempty"`
	RestoreOnly      bool                     `json:"restore_only,omitempty"`
}

func (r *Record) unfinished() bool { return r != nil && r.Status != StatusIdle }

func (e *Engine) journalLockPath() string { return filepath.Join(e.opts.JournalDir, journalLock) }
func (e *Engine) recordPath() string      { return filepath.Join(e.opts.JournalDir, recordFile) }
func (e *Engine) receiptPath(id contract.RunID) string {
	return filepath.Join(e.opts.JournalDir, receiptsDir, string(id)+jsonExt)
}
func (e *Engine) backupDir(id contract.RunID) string {
	return filepath.Join(e.opts.JournalDir, backupsDir, string(id))
}

// readRecord reads the record; a missing record is nil. A record that does
// not decode strictly is ErrCorruptRecord. Call it under the journal lock.
func (e *Engine) readRecord() (*Record, error) {
	var r Record
	found, err := readJSON(e.recordPath(), &r)
	if err != nil || !found {
		return nil, err
	}
	if (r.Version != recordVersionV1 && r.Version != recordVersion) || r.Op != OpApply || !r.Status.valid() || !r.PlanDigestKind.valid() || platform.ValidateRunID(r.RunID) != nil {
		return nil, fmt.Errorf("%w: %q", ErrCorruptRecord, e.recordPath())
	}
	return &r, nil
}

// writeRecord makes r durable. Call it under the journal lock. Only the
// claimant writes a record that carries a claim, so the write also renews
// it: a recovery's own writes never put back an older expiry.
func (e *Engine) writeRecord(r *Record) error {
	if r.Claim != nil {
		r.Claim.Expires = e.opts.Now().Add(e.opts.ClaimTTL).UTC()
	}
	r.Version = recordVersion
	r.Op = OpApply
	return e.writeJSON(e.recordPath(), r)
}

func (e *Engine) readReceipt(id contract.RunID) (*contract.Receipt, error) {
	var r contract.Receipt
	found, err := readJSON(e.receiptPath(id), &r)
	if err != nil || !found {
		return nil, err
	}
	return &r, nil
}

func (e *Engine) writeReceipt(r contract.Receipt) error {
	return e.writeJSON(e.receiptPath(r.RunID), r)
}

func (e *Engine) writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %q: %w", path, err)
	}
	return e.atomicWrite(path, fileSpec{data: append(data, '\n'), perm: 0o600})
}

// readJSON decodes path strictly. It reports false for a missing file.
func readJSON(path string, v any) (bool, error) {
	data, err := readFileAt(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %q: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return false, fmt.Errorf("%w: %q: %w", ErrCorruptRecord, path, err)
	}
	return true, nil
}

// prepareJournal creates the journal folders durably and removes temporary
// files a kill left while writing a journal file (SPC-001 "Torn states",
// first row). Call it under the journal lock.
func (e *Engine) prepareJournal() error {
	for _, d := range []string{e.opts.JournalDir, filepath.Join(e.opts.JournalDir, receiptsDir), filepath.Join(e.opts.JournalDir, backupsDir)} {
		if err := e.mkdirDurable(d); err != nil {
			return err
		}
	}
	for _, d := range []string{e.opts.JournalDir, filepath.Join(e.opts.JournalDir, receiptsDir)} {
		dir, err := openDir(d)
		if err != nil {
			return err
		}
		names, err := listDir(dir)
		if err == nil {
			for _, n := range names {
				if strings.Contains(n, tempMarker) {
					if rmErr := unix.Unlinkat(int(dir.Fd()), n, 0); rmErr != nil && !errors.Is(rmErr, unix.ENOENT) {
						err = fmt.Errorf("remove stale journal temp %q: %w", n, rmErr)
						break
					}
				}
			}
		}
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
