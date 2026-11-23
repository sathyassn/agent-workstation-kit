package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// State key prefixes for the typed operations, as the contract fakes use.
const (
	keyPackage   = "package:"
	keyService   = "service:"
	keyUserGroup = "user-group:"
	newFileMode  = fs.FileMode(0o600)
	systemPath   = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// passEnv is the tool environment allowlist (SPC-001 "Interfaces and
// formats"), passed through when set. PATH is fixed instead.
func passEnv() []string {
	return []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM", "TMPDIR"}
}

// errReporter is a sink that reports a failed event write (TSK-009's event
// log). A step fails closed when it reports one.
type errReporter interface{ Err() error }

// tx is the contract.Tx a unit gets for one apply.
type tx struct {
	e     *Engine
	l     *locks
	desc  contract.Description
	m     *Manifest
	paths map[string]bool
	keys  map[contract.StateKey]bool
	env   map[string]bool

	// opMu serialises mutating calls; mu guards the fields below it.
	opMu    sync.Mutex
	mu      sync.Mutex
	rec     *Record
	changes []contract.AppliedChange
	events  []contract.Event
	seq     uint64
	cmdSeq  int
	failed  error
}

var _ contract.Tx = (*tx)(nil)

func newTx(e *Engine, l *locks, rec *Record, desc contract.Description, m *Manifest) *tx {
	t := &tx{
		e: e, l: l, rec: rec, desc: desc, m: m,
		paths: map[string]bool{}, keys: map[contract.StateKey]bool{}, env: map[string]bool{},
	}
	for _, en := range m.Entries {
		t.paths[en.Path] = true
	}
	for _, k := range desc.OwnedStateKeys {
		t.keys[k] = true
	}
	for _, k := range desc.DeclaredEnv {
		t.env[k] = true
	}
	return t
}

// Changes returns the changes recorded for the receipt.
func (t *tx) Changes() []contract.AppliedChange {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]contract.AppliedChange(nil), t.changes...)
}

// Events returns the events emitted so far.
func (t *tx) Events() []contract.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]contract.Event(nil), t.events...)
}

func (t *tx) sinkErr() error {
	if r, ok := t.e.opts.Sink.(errReporter); ok {
		if err := r.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrEventLog, err)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failed
}

// begin checks that a mutation may start: the target is declared, the step
// is not cancelled, and no earlier event or journal write has failed.
func (t *tx) begin(ctx context.Context, path string) (ManifestEntry, error) {
	en, ok := t.m.entry(path)
	if !ok || !t.paths[path] {
		return ManifestEntry{}, &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	if err := ctx.Err(); err != nil {
		return ManifestEntry{}, fmt.Errorf("tx: %w", err)
	}
	return en, t.sinkErr()
}

func (t *tx) record(en ManifestEntry, action contract.ChangeAction, after contract.Digest) {
	ch := contract.AppliedChange{
		Change: contract.Change{Target: en.Path, Action: action, Before: en.Digest, After: after, Risk: contract.RiskLow},
		Backup: contract.BackupRef(string(t.m.RunID) + ":" + en.Path),
	}
	t.mu.Lock()
	t.changes = append(t.changes, ch)
	t.mu.Unlock()
	t.Emit(contract.Event{Kind: contract.EventChange, Data: map[contract.DataKey]string{
		contract.DataPath: en.Path, contract.DataAction: string(action), contract.DataDigest: string(after),
	}})
	t.e.fault(PointMutation)
}

// WriteFile replaces path with data atomically.
func (t *tx) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode, owner contract.Owner) error {
	t.opMu.Lock()
	defer t.opMu.Unlock()
	en, err := t.begin(ctx, path)
	if err != nil {
		return err
	}
	p, err := openParent(t.e.real(path))
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.refuseMetadata(); err != nil {
		return err
	}
	if err := t.e.atomicWriteAt(p, fileSpec{data: data, perm: perm, owner: &owner}); err != nil {
		return err
	}
	action := contract.ActionReplace
	if en.Kind == KindAbsent {
		action = contract.ActionCreate
	}
	t.record(en, action, digest(data))
	return nil
}

// EnsureBlock puts block b into path, keeping everything outside it.
func (t *tx) EnsureBlock(ctx context.Context, path string, b contract.ManagedBlock) error {
	return t.editBlock(ctx, path, func(s string) (string, error) { return ensureBlock(s, b) })
}

// RemoveBlock removes block id from path, keeping everything outside it.
func (t *tx) RemoveBlock(ctx context.Context, path string, id contract.BlockID) error {
	return t.editBlock(ctx, path, func(s string) (string, error) { return removeBlock(s, id) })
}

func (t *tx) editBlock(ctx context.Context, path string, edit func(string) (string, error)) error {
	t.opMu.Lock()
	defer t.opMu.Unlock()
	en, err := t.begin(ctx, path)
	if err != nil {
		return err
	}
	p, err := openParent(t.e.real(path))
	if err != nil {
		return err
	}
	defer p.close()
	spec := fileSpec{perm: newFileMode}
	var cur []byte
	st, err := p.lstat()
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		// Editing through a link would replace the link with a file.
		return fmt.Errorf("%w: %q is not a regular file", ErrUnsupportedTarget, path)
	default:
		spec.perm = unixMode(st)
		f, err := p.open()
		if err != nil {
			return err
		}
		if err := checkMetadata(f); err != nil {
			_ = f.Close()
			return err
		}
		cur, err = io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
	}
	next, err := edit(string(cur))
	if err != nil {
		return fmt.Errorf("edit %q: %w", path, err)
	}
	if next == string(cur) {
		return nil
	}
	spec.data = []byte(next)
	if err := t.e.atomicWriteAt(p, spec); err != nil {
		return err
	}
	t.record(en, contract.ActionReplace, digest(spec.data))
	return nil
}

// Remove deletes path. A missing path is no change.
func (t *tx) Remove(ctx context.Context, path string) error {
	t.opMu.Lock()
	defer t.opMu.Unlock()
	en, err := t.begin(ctx, path)
	if err != nil {
		return err
	}
	p, err := openParent(t.e.real(path))
	if err != nil {
		return err
	}
	defer p.close()
	st, err := p.lstat()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return fmt.Errorf("%w: %q is a folder", ErrUnsupportedTarget, path)
	}
	if err := p.refuseMetadata(); err != nil {
		return err
	}
	if err := unix.Unlinkat(p.fd(), p.name, 0); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	if err := t.e.syncDir(p); err != nil {
		return err
	}
	t.record(en, contract.ActionRemove, "")
	return nil
}

// Run starts c inside a new containment of this run. The containment name is
// durable in the record before the tool starts, and the tool's identity
// after the launch acknowledgement. A package-manager transaction runs with
// a context that neither cancel nor its timeout can end.
func (t *tx) Run(ctx context.Context, c contract.Command) (contract.CommandResult, error) {
	for _, k := range c.EnvKeys {
		if !t.env[k] {
			return contract.CommandResult{}, &contract.UndeclaredTargetError{Kind: contract.TargetEnvKey, Target: k}
		}
		if platform.ForbiddenEnvKey(k) {
			return contract.CommandResult{}, fmt.Errorf("%w: declared key %s (LD_*, DYLD_* and SYSTEMD_* are refused)", platform.ErrForbiddenEnv, k)
		}
	}
	if len(c.Argv) == 0 {
		return contract.CommandResult{}, platform.ErrCommandEmpty
	}
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.CommandResult{}, fmt.Errorf("run: %w", err)
	}
	if err := t.sinkErr(); err != nil {
		return contract.CommandResult{}, err
	}
	bk := context.WithoutCancel(ctx)
	t.cmdSeq++
	name, err := t.e.opts.Containment.NameFor(t.m.RunID, t.cmdSeq)
	if err != nil {
		return contract.CommandResult{}, fmt.Errorf("containment name: %w", err)
	}
	if err := t.setCommand(bk, &CommandRecord{Name: name, PackageTransaction: c.PackageTransaction}); err != nil {
		return contract.CommandResult{}, err
	}
	t.e.fault(PointLaunch)

	var sup platform.ProcessIdentity
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	switch {
	case c.PackageTransaction:
		// Waited for, never killed: no cancel, grace or timeout ends it.
		runCtx = bk
	case c.Timeout > 0:
		runCtx, cancel = context.WithTimeout(ctx, c.Timeout)
	}
	defer cancel()
	t.Emit(contract.Event{Kind: contract.EventCommand, Data: map[contract.DataKey]string{contract.DataArgv0: c.Argv[0]}})
	res, err := t.e.opts.Containment.Run(runCtx, platform.LaunchSpec{
		Name: name, Scope: t.e.opts.Scope, Argv: c.Argv, Env: t.toolEnv(c.EnvKeys), Dir: c.Dir,
		PackageTransaction: c.PackageTransaction, Output: t.e.opts.ToolLog,
	}, func(ack platform.LaunchAck) error {
		// The supervisor waits at its gate until this is durable.
		sup = ack.Supervisor
		return t.setCommand(bk, &CommandRecord{Name: name, PackageTransaction: c.PackageTransaction, Supervisor: sup})
	})
	t.e.fault(PointCommandDone)
	if err != nil {
		err = fmt.Errorf("run %s: %w", c.Argv[0], err)
	}
	// Prove the containment empty before the unit moves on, whether the
	// tool succeeded, failed or was cancelled: a tool can leave descendants.
	// A package transaction's containment is only waited for.
	if emptyErr := t.e.empty(bk, name, sup, c.PackageTransaction, 0); emptyErr != nil {
		blocked := fmt.Errorf("%w: command %s: %w", ErrEmptinessUnknown, c.Argv[0], emptyErr)
		t.fail(blocked)
		return res, errors.Join(err, blocked)
	}
	if markErr := t.setCommand(bk, &CommandRecord{Name: name, PackageTransaction: c.PackageTransaction, Supervisor: sup, Finished: true}); markErr != nil {
		t.fail(markErr)
		return res, errors.Join(err, markErr)
	}
	t.e.fault(PointCommandEmptied)
	if err == nil && runCtx.Err() != nil {
		err = fmt.Errorf("run %s: %w", c.Argv[0], runCtx.Err())
	}
	if failErr := t.sinkErr(); failErr != nil {
		err = errors.Join(err, failErr)
	}
	t.Emit(contract.Event{Kind: contract.EventCommand, Data: map[contract.DataKey]string{
		contract.DataArgv0: c.Argv[0], contract.DataExitCode: strconv.Itoa(res.ExitCode),
	}})
	return res, err
}

func (t *tx) setCommand(ctx context.Context, cmd *CommandRecord) error {
	t.mu.Lock()
	t.rec.Command = cmd
	rec := *t.rec
	t.mu.Unlock()
	return t.l.withJournal(ctx, func() error { return t.e.writeRecord(&rec) })
}

func (t *tx) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed == nil {
		t.failed = err
	}
}

func (t *tx) toolEnv(keys []string) []string {
	env := []string{"PATH=" + systemPath}
	if runtime.GOOS == "linux" {
		env = append(env, "DEBIAN_FRONTEND=noninteractive")
	}
	for _, k := range append(passEnv(), keys...) {
		if v, ok := t.e.opts.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Emit numbers e, stamps it with the run, and delivers it. Deliver never
// blocks (contract.Sink).
func (t *tx) Emit(ev contract.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	ev.Seq = t.seq
	if ev.Time.IsZero() {
		ev.Time = t.e.opts.Now().UTC()
	}
	ev.RunID, ev.Unit = t.rec.RunID, t.desc.Unit
	if ev.Step == "" {
		ev.Step = contract.StepApply
	}
	t.events = append(t.events, ev)
	if t.e.opts.Sink != nil {
		t.e.opts.Sink.Deliver(ev)
	}
}

func (t *tx) checkKey(key contract.StateKey) error {
	if !t.keys[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	return fmt.Errorf("%w: state key %q", ErrUnsupported, key)
}

// RenderTemplate is not provided yet; the path check still runs first.
func (t *tx) RenderTemplate(ctx context.Context, path string, _ contract.TemplateID, _ contract.TemplateVars, _ fs.FileMode, _ contract.Owner) error {
	if _, err := t.begin(ctx, path); err != nil {
		return err
	}
	return fmt.Errorf("%w: RenderTemplate", ErrUnsupported)
}

// Package is not provided yet; the declared-key check still runs first.
func (t *tx) Package(_ context.Context, want contract.PackageState) error {
	return t.checkKey(contract.StateKey(keyPackage + want.Name))
}

// Service is not provided yet; the declared-key check still runs first.
func (t *tx) Service(_ context.Context, want contract.ServiceState) error {
	return t.checkKey(contract.StateKey(keyService + want.Name))
}

// UserGroup is not provided yet; the declared-key check still runs first.
func (t *tx) UserGroup(_ context.Context, want contract.UserGroupState) error {
	return t.checkKey(contract.StateKey(keyUserGroup + want.User + ":" + want.Group))
}

// SetState is not provided yet; the declared-key check still runs first.
func (t *tx) SetState(_ context.Context, _ contract.StateProvider, key contract.StateKey, _ contract.State) error {
	return t.checkKey(key)
}

// Check is not provided yet.
func (t *tx) Check(_ context.Context, c contract.CheckSpec) (contract.CheckResult, error) {
	return contract.CheckResult{Name: c.Name}, fmt.Errorf("%w: Check", ErrUnsupported)
}
