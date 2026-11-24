package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// Containment is what the engine needs from an OS containment adapter
// (platform/linux, platform/darwin).
type Containment interface {
	NameFor(runID contract.RunID, seq int) (string, error)
	// Run holds the supervisor at a gate until onSupervisor has returned
	// nil, so its identity is durable before anything runs.
	Run(ctx context.Context, spec platform.LaunchSpec, onSupervisor func(platform.LaunchAck) error) (contract.CommandResult, error)
	Empty(ctx context.Context, name string, opts platform.EmptyOptions) error
}

// ProcessInspector tells a live process from a dead one by its identity.
type ProcessInspector interface {
	Self(ctx context.Context) (platform.ProcessIdentity, error)
	Alive(ctx context.Context, id platform.ProcessIdentity) (bool, error)
}

// RestoreValidator checks host configuration after a restore replays the
// manifest (for example sshd -t, visudo -c or ufw status). A non-nil error
// fails the restore closed with exit 4, leaving the intent unfinished.
type RestoreValidator interface {
	Name() string
	Validate(ctx context.Context) error
}

type validatorFunc struct {
	name string
	fn   func(ctx context.Context) error
}

func (v validatorFunc) Name() string                       { return v.name }
func (v validatorFunc) Validate(ctx context.Context) error { return v.fn(ctx) }

// NewValidator adapts a named function into a RestoreValidator.
func NewValidator(name string, fn func(ctx context.Context) error) RestoreValidator {
	return validatorFunc{name: name, fn: fn}
}

// Defaults for the bounds in Options.
const (
	DefaultGrace               = 10 * time.Second
	DefaultKillWait            = 10 * time.Second
	DefaultClaimTTL            = 5 * time.Minute
	DefaultClaimRenew          = time.Minute
	DefaultLockPoll            = 50 * time.Millisecond
	DefaultCallTimeout         = 10 * time.Second
	DefaultStateRestoreTimeout = 30 * time.Second
)

// Options configure an Engine. JournalDir, HostLock, Containment and
// Inspector are required.
type Options struct {
	// JournalDir is this journal's private folder: the record, receipts and
	// backups.
	JournalDir string
	// HostLock is the host lock file, shared by every journal on the host.
	HostLock string
	// Root is the test root: every declared path is resolved under it. Empty
	// means the real filesystem root.
	Root string
	// Scope is the journal's scope; it selects the service manager.
	Scope       contract.Scope
	Containment Containment
	Inspector   ProcessInspector
	// Providers maps each StateProviderName to its StateProvider for
	// pre-apply snapshots, declarative operations (Package, Service,
	// UserGroup) and cross-process recovery restores.
	Providers map[contract.StateProviderName]contract.StateProvider
	// Validators run after a restore replays and verifies the manifest
	// (ADR 0005 section 5). Any failure leaves the intent unfinished (exit 4).
	Validators []RestoreValidator
	// Sink receives events. If it also has an Err method that reports a
	// failed write, the step fails closed on that error.
	Sink contract.Sink
	// ToolLog receives tool output. Output never goes to events, which are
	// shown on the terminal. Nil discards it.
	ToolLog io.Writer
	// LookupEnv reads a declared environment variable; nil is os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Grace is how long a step may run on after a cancel request.
	Grace time.Duration
	// KillWait bounds terminate-then-kill when a containment is emptied.
	KillWait time.Duration
	// ClaimTTL and ClaimRenew are the recovery claim's lifetime and renewal
	// interval.
	ClaimTTL   time.Duration
	ClaimRenew time.Duration
	// LockPoll is how often a blocked lock is retried.
	LockPoll time.Duration
	// CallTimeout bounds each supervisor inspection and signal.
	CallTimeout time.Duration
	// StateRestoreTimeout bounds the provider Restore call SetState runs
	// after a failed Write, under an uncancelled context.
	StateRestoreTimeout time.Duration
	// traceFn, set only by tests in this package, sees every folder entry
	// and folder fsync for the durability model.
	traceFn func(op, dir, name string)
	// metaOps, set only by tests in this package, overrides OS metadata
	// capture and application (for example to simulate ENOTSUP).
	metaOps metadataOps
	// extraPackageDBPaths, set only by tests in this package, adds real
	// directories whose (dev, ino) identities are treated as package-DB roots.
	extraPackageDBPaths []string
	// afterLstat, set only by tests in this package, runs between lstat and
	// open in backupOne to exercise the TOCTOU window deterministically.
	afterLstat func(path string)
	// Fault is called at every Point. Nil uses the build's default, which
	// is no hook unless the binary is built with the faultinject tag.
	Fault FaultFunc
}

// Engine runs unit transactions against one journal.
type Engine struct {
	opts     Options
	provMu   sync.RWMutex
	pkgDBIDs map[platform.FileID]string
}

func (e *Engine) lookupProvider(name contract.StateProviderName) (contract.StateProvider, bool) {
	e.provMu.RLock()
	defer e.provMu.RUnlock()
	p, ok := e.opts.Providers[name]
	return p, ok
}

// New checks opts and fills in defaults.
func New(opts Options) (*Engine, error) {
	switch {
	case opts.JournalDir == "" || !filepath.IsAbs(opts.JournalDir):
		return nil, fmt.Errorf("%w: JournalDir must be absolute", ErrInvalidOptions)
	case opts.HostLock == "" || !filepath.IsAbs(opts.HostLock):
		return nil, fmt.Errorf("%w: HostLock must be absolute", ErrInvalidOptions)
	case opts.Root != "" && !filepath.IsAbs(opts.Root):
		return nil, fmt.Errorf("%w: Root must be absolute", ErrInvalidOptions)
	case opts.Containment == nil || opts.Inspector == nil:
		return nil, fmt.Errorf("%w: Containment and Inspector are required", ErrInvalidOptions)
	case !opts.Scope.Valid():
		return nil, fmt.Errorf("%w: scope %q", ErrInvalidOptions, opts.Scope)
	}
	if len(opts.Providers) > 0 {
		copied := make(map[contract.StateProviderName]contract.StateProvider, len(opts.Providers))
		for name, p := range opts.Providers {
			if p == nil || name == "" || p.Name() != name {
				return nil, fmt.Errorf("%w: invalid state provider entry %q", ErrInvalidOptions, name)
			}
			copied[name] = p
		}
		opts.Providers = copied
	}
	if len(opts.Validators) > 0 {
		copied := make([]RestoreValidator, len(opts.Validators))
		for i, v := range opts.Validators {
			if v == nil || v.Name() == "" {
				return nil, fmt.Errorf("%w: validator %d must be non-nil with a name", ErrInvalidOptions, i)
			}
			copied[i] = v
		}
		opts.Validators = copied
	}
	if err := checkExistingAncestors(opts.JournalDir); err != nil {
		return nil, fmt.Errorf("journal folder: %w", err)
	}
	if err := (&Engine{opts: opts}).mkdirAllDurable(opts.JournalDir); err != nil {
		return nil, fmt.Errorf("create journal folder: %w", err)
	}
	if err := platform.CheckPrivateDir(opts.JournalDir); err != nil {
		return nil, fmt.Errorf("journal folder: %w", err)
	}
	// Resolve the configured paths once; every later operation walks these
	// canonical paths without following links.
	var err error
	if opts.JournalDir, err = canonical(opts.JournalDir); err != nil {
		return nil, err
	}
	lockDir, err := canonical(filepath.Dir(opts.HostLock))
	if err != nil {
		return nil, err
	}
	opts.HostLock = filepath.Join(lockDir, filepath.Base(opts.HostLock))
	if err := platform.CheckTrustedAncestors(lockDir); err != nil {
		return nil, fmt.Errorf("host lock folder: %w", err)
	}
	if opts.Root != "" {
		if opts.Root, err = canonical(opts.Root); err != nil {
			return nil, err
		}
	}
	setDefault(&opts.Grace, DefaultGrace)
	setDefault(&opts.KillWait, DefaultKillWait)
	setDefault(&opts.ClaimTTL, DefaultClaimTTL)
	setDefault(&opts.ClaimRenew, DefaultClaimRenew)
	setDefault(&opts.LockPoll, DefaultLockPoll)
	setDefault(&opts.CallTimeout, DefaultCallTimeout)
	setDefault(&opts.StateRestoreTimeout, DefaultStateRestoreTimeout)
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if opts.metaOps == nil {
		opts.metaOps = osMetadataOps{}
	}
	if opts.Fault == nil {
		opts.Fault = defaultFault()
	}
	roots := append(packageDBPrefixes(), opts.extraPackageDBPaths...)
	return &Engine{opts: opts, pkgDBIDs: resolvePackageDBIDs(roots)}, nil
}

func (e *Engine) meta() metadataOps {
	if e.opts.metaOps != nil {
		return e.opts.metaOps
	}
	return osMetadataOps{}
}

func setDefault(d *time.Duration, v time.Duration) {
	if *d <= 0 {
		*d = v
	}
}

func (e *Engine) fault(p Point) {
	if e.opts.Fault != nil {
		e.opts.Fault(p)
	}
}

func (e *Engine) real(path string) string {
	if e.opts.Root == "" {
		return path
	}
	return filepath.Join(e.opts.Root, path)
}

// empty proves a command's containment empty within its bound: wait for
// grace, terminate, kill. A package transaction is only waited for, for the
// grace period plus the kill wait, and never signalled. The whole call has
// a deadline, and each inspection and signal its own.
func (e *Engine) empty(ctx context.Context, name string, sup platform.ProcessIdentity, pkg bool, grace time.Duration) error {
	opts := platform.EmptyOptions{
		Scope: e.opts.Scope, Grace: grace, KillWait: e.opts.KillWait, PackageTransaction: pkg,
		Supervisor: sup, CallTimeout: e.opts.CallTimeout,
	}
	if pkg {
		opts.Grace = e.opts.Grace
	}
	bound := opts.Grace + opts.KillWait + 4*e.opts.CallTimeout
	cctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	if err := e.opts.Containment.Empty(cctx, name, opts); err != nil {
		return fmt.Errorf("empty %s: %w", name, err)
	}
	return nil
}

// Apply runs one unit's apply as a transaction: back up every declared path
// and matching state provider, write the intent record, apply, verify, write
// the receipt. A failure restores the backup and writes a failed receipt.
// Apply holds the host lock throughout and the journal lock only while it
// touches the journal.
//
// If ctx ends, the step runs on for the grace period; if it has not finished
// by then, its context is cancelled, the record is left for the next start,
// and Apply returns ErrCancelled. A package-manager transaction is never
// cancelled.
func (e *Engine) Apply(ctx context.Context, u contract.Unit, p contract.Plan) error {
	desc, err := u.Describe(ctx)
	if err != nil {
		return fmt.Errorf("describe %s: %w", u.ID(), err)
	}
	if desc.Unit != u.ID() || p.Unit != u.ID() {
		return fmt.Errorf("%w: unit %q, description %q, plan %q", ErrUnitMismatch, u.ID(), desc.Unit, p.Unit)
	}
	self, err := e.opts.Inspector.Self(ctx)
	if err != nil {
		return fmt.Errorf("own identity: %w", err)
	}
	l := &locks{e: e}
	if err := l.takeHost(ctx); err != nil {
		return err
	}
	defer l.releaseHost()
	var rec *Record
	if err := l.withJournal(ctx, func() error {
		if err := e.prepareJournal(); err != nil {
			return err
		}
		if rec, err = e.readRecord(); err != nil || rec.unfinished() {
			return err
		}
		return e.removeOrphanBackups(rec)
	}); err != nil {
		return err
	}
	if rec.unfinished() {
		return e.blockedBy(ctx, rec)
	}
	return e.runApply(ctx, l, self, u, desc, p)
}

// blockedBy says why an unfinished record blocks a new apply.
func (e *Engine) blockedBy(ctx context.Context, rec *Record) error {
	alive, err := e.opts.Inspector.Alive(ctx, rec.Executor)
	if err != nil {
		return fmt.Errorf("%w: cannot inspect executor of run %s: %w", ErrActiveExecutor, rec.RunID, err)
	}
	if alive {
		return fmt.Errorf("%w: run %s", ErrActiveExecutor, rec.RunID)
	}
	return fmt.Errorf("%w: run %s is %s", ErrRecoveryPending, rec.RunID, rec.Status)
}

// runApply is the transaction. The caller holds the host lock.
func (e *Engine) runApply(ctx context.Context, l *locks, self platform.ProcessIdentity, u contract.Unit, desc contract.Description, p contract.Plan) error {
	// Durable bookkeeping runs on even after a cancel request; only the
	// unit's step is cancelled, and only at the end of the grace period.
	bk := context.WithoutCancel(ctx)
	runID, err := newRunID()
	if err != nil {
		return err
	}
	m, err := e.backup(bk, runID, desc)
	if err != nil {
		return err
	}
	rec := &Record{
		Status: StatusApplying, RunID: runID, Unit: desc.Unit, PlanID: p.ID,
		PlanDigest: p.Digest, Scope: e.opts.Scope, Executor: self,
	}
	if err := l.withJournal(bk, func() error { return e.writeRecord(rec) }); err != nil {
		return err
	}
	e.fault(PointIntent)

	t := newTx(e, l, rec, desc, m)
	step, stop := e.withGrace(ctx)
	defer stop()
	stepErr := e.step(step, t, u, p)
	if errors.Is(stepErr, ErrEmptinessUnknown) {
		// Restoring over processes that may still run could be undone by
		// them; leave the record for a start that can empty the containment.
		return stepErr
	}
	if stepErr != nil && errors.Is(context.Cause(step), ErrCancelled) {
		// Leave the record: the next start empties, restores and reapplies.
		return fmt.Errorf("%w: run %s stopped after the grace period: %w", ErrCancelled, runID, stepErr)
	}
	if stepErr != nil {
		return e.failRun(bk, l, rec, m, stepErr)
	}
	if err := e.writeReceipt(contract.Receipt{
		RunID: runID, Unit: desc.Unit, PlanDigest: p.Digest, Changes: t.Changes(),
		Time: e.opts.Now().UTC(), Outcome: contract.OutcomeVerified,
	}); err != nil {
		return err
	}
	e.fault(PointReceipt)
	rec.Status = StatusIdle
	if err := l.withJournal(bk, func() error { return e.writeRecord(rec) }); err != nil {
		return err
	}
	e.fault(PointIdle)
	return nil
}

// step runs the unit's Apply and Verify.
func (e *Engine) step(ctx context.Context, t *tx, u contract.Unit, p contract.Plan) error {
	// A latched failure (a failed event write, or a containment not proven
	// empty) is always joined to the unit's own result, so a unit error can
	// never hide it and let a restore run over surviving processes.
	err := u.Apply(ctx, t, p)
	e.fault(PointApplied)
	if err = errors.Join(err, t.sinkErr()); err != nil {
		return fmt.Errorf("apply %s: %w", u.ID(), err)
	}
	res, err := u.Verify(ctx, p)
	e.fault(PointVerified)
	if err == nil && res.Outcome != contract.OutcomeVerified {
		err = fmt.Errorf("%w: %s reported %q", ErrVerifyFailed, u.ID(), res.Outcome)
	}
	if err = errors.Join(err, t.sinkErr()); err != nil {
		return fmt.Errorf("verify %s: %w", u.ID(), err)
	}
	return nil
}

// failRun restores a failed run and closes it with a failed receipt. If the
// restore or its validators fail, the intent stays StatusRestoring and the
// error maps to exit 4.
func (e *Engine) failRun(ctx context.Context, l *locks, rec *Record, m *Manifest, cause error) error {
	if err := e.restoreRun(ctx, l, rec, m, StatusIdle); err != nil {
		return errors.Join(cause, err)
	}
	return fmt.Errorf("%w: %w", ErrApplyFailed, cause)
}

// restoreRun marks the record restoring, restores and validates the manifest,
// writes a failed receipt for the run and moves the record to next.
func (e *Engine) restoreRun(ctx context.Context, l *locks, rec *Record, m *Manifest, next Status) error {
	rec.Status = StatusRestoring
	if err := l.withJournal(ctx, func() error { return e.writeRecord(rec) }); err != nil {
		return err
	}
	e.fault(PointRestoreStart)
	if err := e.restore(ctx, m); err != nil {
		return err
	}
	if err := e.writeReceipt(contract.Receipt{
		RunID: rec.RunID, Unit: rec.Unit, PlanDigest: rec.PlanDigest,
		Time: e.opts.Now().UTC(), Outcome: contract.OutcomeFailed,
	}); err != nil {
		return err
	}
	e.fault(PointRestored)
	rec.Status = next
	return l.withJournal(ctx, func() error { return e.writeRecord(rec) })
}

// withGrace returns the step's context. It ignores ctx's end until the
// grace period after it has passed, then is cancelled with ErrCancelled.
func (e *Engine) withGrace(ctx context.Context) (context.Context, func()) {
	step, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		t := time.NewTimer(e.opts.Grace)
		defer t.Stop()
		select {
		case <-t.C:
			cancel(ErrCancelled)
		case <-step.Done():
		}
	})
	return step, func() {
		stop()
		cancel(nil)
	}
}

func newRunID() (contract.RunID, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random run ID: %w", err)
	}
	return contract.RunID("r" + hex.EncodeToString(b[:])), nil
}
