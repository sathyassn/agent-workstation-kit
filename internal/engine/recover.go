package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// Resolver returns the unit and its current plan for a reapply.
type Resolver func(ctx context.Context, id contract.UnitID) (contract.Unit, contract.Plan, error)

// ResumeOptions select how Resume handles an interrupted apply.
type ResumeOptions struct {
	// RestoreOnly restores and stops; it never reapplies.
	RestoreOnly bool
}

// ResumeAction is what Resume did.
type ResumeAction string

// Resume actions.
const (
	// ResumeNothing: the journal had no unfinished run, or another starter
	// finished it first.
	ResumeNothing ResumeAction = "nothing"
	// ResumeCompleted: the run's verified receipt existed, so the record was
	// closed without a restore.
	ResumeCompleted ResumeAction = "completed"
	// ResumeRestored: the run was restored and not reapplied.
	ResumeRestored ResumeAction = "restored"
	// ResumeReapplied: the run was restored, then applied again.
	ResumeReapplied ResumeAction = "reapplied"
)

// ResumeResult reports what Resume did.
type ResumeResult struct {
	Action ResumeAction
	// RunID is the interrupted run.
	RunID contract.RunID
}

// Resume recovers an interrupted apply (SPC-001 "Intent, recovery and
// revert", apply machine). In order:
//
//  1. Under the journal lock, it reads the record. A live executor, told by
//     boot ID, PID and start time, or a live claim by another starter
//     returns exit 4. Otherwise it writes its own recovery claim.
//  2. Under the claim and before taking the host lock, it empties the run's
//     containment within its bound. If it cannot, it withdraws the claim
//     and returns exit 4 without taking the host lock.
//  3. It takes the host lock, then the journal lock, and re-reads the
//     record and receipts: another starter may have finished meanwhile.
//  4. It restores from the manifest, closes the run with a failed receipt,
//     and reapplies the unit unless RestoreOnly is set.
func (e *Engine) Resume(ctx context.Context, resolve Resolver, o ResumeOptions) (ResumeResult, error) {
	bk := context.WithoutCancel(ctx)
	self, err := e.opts.Inspector.Self(ctx)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("own identity: %w", err)
	}
	l := &locks{e: e}
	rec, err := e.claim(ctx, l, self)
	if err != nil || rec == nil {
		return ResumeResult{Action: ResumeNothing}, err
	}
	res := ResumeResult{Action: ResumeNothing, RunID: rec.RunID}
	e.fault(PointClaimed)

	renewDone := e.keepClaim(bk, rec.RunID, self)
	defer renewDone()

	if cmd := rec.Command; cmd != nil && !cmd.Finished {
		// A live recorded supervisor, or a containment the OS still
		// reports, is not empty; neither is one whose state is unknown.
		if err := e.empty(bk, cmd.Name, cmd.Supervisor, cmd.PackageTransaction, e.opts.Grace); err != nil {
			return res, errors.Join(fmt.Errorf("%w: run %s: %w", ErrEmptinessUnknown, rec.RunID, err), e.dropClaim(bk, l, rec.RunID, self))
		}
	}
	e.fault(PointEmptied)

	if err := l.takeHost(ctx); err != nil {
		return res, errors.Join(err, e.dropClaim(bk, l, rec.RunID, self))
	}
	defer l.releaseHost()
	var cur *Record
	if err := l.withJournal(bk, func() error {
		cur, err = e.readRecord()
		return err
	}); err != nil {
		return res, err
	}
	if !cur.unfinished() || cur.RunID != rec.RunID || cur.Claim == nil || cur.Claim.Claimant != self {
		return res, nil
	}
	e.fault(PointRecoverLocked)
	return e.recoverLocked(ctx, l, self, cur, resolve, o)
}

// claim reads the record and, if it is unfinished and abandoned, writes this
// starter's claim. It returns nil when there is nothing to recover.
func (e *Engine) claim(ctx context.Context, l *locks, self platform.ProcessIdentity) (*Record, error) {
	var rec *Record
	err := l.withJournal(ctx, func() error {
		if err := e.prepareJournal(); err != nil {
			return err
		}
		r, err := e.readRecord()
		if err != nil || !r.unfinished() {
			return err
		}
		if r.Executor != self {
			if err := e.blockedBy(ctx, r); errors.Is(err, ErrActiveExecutor) {
				return err
			}
		}
		if r.Claim != nil && r.Claim.Claimant != self {
			live, err := e.claimLive(ctx, r.Claim)
			if err != nil {
				return err
			}
			if live {
				return fmt.Errorf("%w: run %s, claimant pid %d", ErrRecoveryClaimed, r.RunID, r.Claim.Claimant.PID)
			}
		}
		r.Claim = &Claim{RunID: r.RunID, Claimant: self, Expires: e.opts.Now().Add(e.opts.ClaimTTL).UTC()}
		if err := e.writeRecord(r); err != nil {
			return err
		}
		rec = r
		return nil
	})
	return rec, err
}

// claimLive reports whether another starter's claim still holds: its
// claimant is alive and it has not expired. When the claimant cannot be
// inspected, the claim counts as live.
func (e *Engine) claimLive(ctx context.Context, c *Claim) (bool, error) {
	if !e.opts.Now().Before(c.Expires) {
		return false, nil
	}
	alive, err := e.opts.Inspector.Alive(ctx, c.Claimant)
	if err != nil {
		return false, fmt.Errorf("%w: cannot inspect claimant: %w", ErrRecoveryClaimed, err)
	}
	return alive, nil
}

// keepClaim renews this starter's claim every ClaimRenew until the returned
// function is called.
func (e *Engine) keepClaim(ctx context.Context, runID contract.RunID, self platform.ProcessIdentity) func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(e.opts.ClaimRenew)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// A separate locks value: the renewer never shares the
				// recovery's lock bookkeeping.
				rl := &locks{e: e}
				_ = rl.withJournal(ctx, func() error {
					r, err := e.readRecord()
					if err != nil || r == nil || r.RunID != runID || r.Claim == nil || r.Claim.Claimant != self {
						return err
					}
					r.Claim.Expires = e.opts.Now().Add(e.opts.ClaimTTL).UTC()
					return e.writeRecord(r)
				})
			}
		}
	})
	return func() {
		close(stop)
		wg.Wait()
	}
}

// dropClaim withdraws this starter's claim after it gave up.
func (e *Engine) dropClaim(ctx context.Context, l *locks, runID contract.RunID, self platform.ProcessIdentity) error {
	return l.withJournal(ctx, func() error {
		r, err := e.readRecord()
		if err != nil || r == nil || r.RunID != runID || r.Claim == nil || r.Claim.Claimant != self {
			return err
		}
		r.Claim = nil
		return e.writeRecord(r)
	})
}

// recoverLocked restores and reapplies. The caller holds the host lock and
// the claim on rec.
func (e *Engine) recoverLocked(ctx context.Context, l *locks, self platform.ProcessIdentity, rec *Record, resolve Resolver, o ResumeOptions) (ResumeResult, error) {
	bk := context.WithoutCancel(ctx)
	res := ResumeResult{RunID: rec.RunID}
	receipt, err := e.readReceipt(rec.RunID)
	if err != nil {
		return res, err
	}
	// Receipts are the authority for what is done.
	if receipt != nil && receipt.Outcome == contract.OutcomeVerified {
		rec.Status, rec.Claim = StatusIdle, nil
		res.Action = ResumeCompleted
		return res, l.withJournal(bk, func() error { return e.writeRecord(rec) })
	}
	next := StatusRestored
	if o.RestoreOnly || rec.RestoreOnly {
		next = StatusIdle
	}
	if receipt == nil || rec.Status != StatusRestored {
		m, err := e.readManifest(rec.RunID)
		if err != nil {
			return res, err
		}
		rec.RestoreOnly = rec.RestoreOnly || o.RestoreOnly
		if err := e.restoreRun(bk, l, rec, m, next); err != nil {
			return res, err
		}
	}
	res.Action = ResumeRestored
	if next == StatusIdle {
		rec.Claim = nil
		return res, l.withJournal(bk, func() error { return e.writeRecord(rec) })
	}

	u, p, err := resolve(ctx, rec.Unit)
	if err != nil {
		return res, fmt.Errorf("resolve %s for reapply: %w", rec.Unit, err)
	}
	if p.Digest != rec.PlanDigest {
		rec.Status, rec.Claim = StatusIdle, nil
		return res, errors.Join(
			fmt.Errorf("%w: %s plan %s, run %s had %s", ErrPlanChanged, rec.Unit, p.Digest, rec.RunID, rec.PlanDigest),
			l.withJournal(bk, func() error { return e.writeRecord(rec) }))
	}
	desc, err := u.Describe(ctx)
	if err != nil {
		return res, fmt.Errorf("describe %s: %w", rec.Unit, err)
	}
	if desc.Unit != rec.Unit || u.ID() != rec.Unit || p.Unit != rec.Unit {
		return res, fmt.Errorf("%w: resolver returned %q for %q", ErrUnitMismatch, u.ID(), rec.Unit)
	}
	if err := e.runApply(ctx, l, self, u, desc, p); err != nil {
		return res, err
	}
	res.Action = ResumeReapplied
	return res, nil
}
