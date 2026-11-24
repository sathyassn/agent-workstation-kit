package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// State key prefixes for the typed operations, as the contract fakes use.
const (
	keyPackage    = "package:"
	keyService    = "service:"
	keyUserGroup  = "user-group:"
	stateOpPrefix = "state-op-"
	newFileMode   = fs.FileMode(0o600)
	systemPath    = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
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
	opMu         sync.Mutex
	mu           sync.Mutex
	rec          *Record
	changes      []contract.AppliedChange
	events       []contract.Event
	droppedNoted map[string]bool
	stateOpSeq   int
	failed       error
}

var _ contract.Tx = (*tx)(nil)

func newTx(e *Engine, l *locks, rec *Record, desc contract.Description, m *Manifest) *tx {
	t := &tx{
		e: e, l: l, rec: rec, desc: desc, m: m,
		paths:        map[string]bool{},
		keys:         map[contract.StateKey]bool{},
		env:          map[string]bool{},
		droppedNoted: map[string]bool{},
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

// sinkErr returns every failure that must stop the step: a failed event
// write and the latched failure (such as ErrEmptinessUnknown), joined. One
// never replaces the other, so a failing event log cannot hide a
// containment that was not proven empty.
func (t *tx) sinkErr() error {
	var sinkErr error
	if r, ok := t.e.opts.Sink.(errReporter); ok {
		if err := r.Err(); err != nil {
			sinkErr = fmt.Errorf("%w: %w", ErrEventLog, err)
		}
	}
	t.mu.Lock()
	failed := t.failed
	t.mu.Unlock()
	return errors.Join(sinkErr, failed)
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
	ref := contract.BackupRef(string(t.m.RunID) + ":" + en.Path)
	ch := contract.AppliedChange{
		Change: contract.Change{Target: en.Path, Action: action, Before: en.Digest, After: after, Risk: contract.RiskLow},
		Backup: ref,
	}
	var newDrops []string
	t.mu.Lock()
	t.changes = append(t.changes, ch)
	for _, attr := range en.DroppedXattrs {
		key := en.Path + "\x00" + attr
		if t.droppedNoted[key] {
			continue
		}
		t.droppedNoted[key] = true
		newDrops = append(newDrops, attr)
		t.changes = append(t.changes, contract.AppliedChange{
			Change: contract.Change{
				Target: en.Path + "#xattr:" + attr,
				Action: contract.ActionRemove,
				Risk:   contract.RiskMedium,
			},
			Backup: ref,
		})
	}
	t.mu.Unlock()
	t.Emit(contract.Event{Kind: contract.EventChange, Data: map[contract.DataKey]string{
		contract.DataPath: en.Path, contract.DataAction: string(action), contract.DataDigest: string(after),
	}})
	if len(newDrops) > 0 {
		t.Emit(contract.Event{
			Kind: contract.EventWarning,
			Text: fmt.Sprintf("replaced %s: dropped OS-protected extended attribute %s (per-app grant cannot be re-set)", en.Path, strings.Join(newDrops, ", ")),
			Data: map[contract.DataKey]string{
				contract.DataPath:   en.Path,
				contract.DataAction: string(action),
			},
		})
	}
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
	p, err := t.e.openParent(t.e.real(path))
	if err != nil {
		return err
	}
	defer p.close()
	if err := p.refuseUnsafeTarget(); err != nil {
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
	p, err := t.e.openParent(t.e.real(path))
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
		f, fst, err := p.openChecked(st)
		if err != nil {
			return err
		}
		if fst.Nlink > 1 {
			_ = f.Close()
			return fmt.Errorf("%w: %q has %d links", ErrHardLinkTarget, path, fst.Nlink)
		}
		spec.perm = unixMode(fst)
		spec.owner = &contract.Owner{UID: int(fst.Uid), GID: int(fst.Gid)}
		meta, metaErr := t.e.meta().Read(f)
		if metaErr != nil {
			_ = f.Close()
			return fmt.Errorf("read metadata of %q: %w", path, metaErr)
		}
		spec.meta = &meta
		cur, err = io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		if int64(len(cur)) != fst.Size {
			return fmt.Errorf("%w: %q size changed during read (%d vs %d)", ErrTargetChanged, path, len(cur), fst.Size)
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
	p, err := t.e.openParent(t.e.real(path))
	if err != nil {
		return err
	}
	defer p.close()
	if _, err := p.lstat(); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := p.refuseUnsafeTarget(); err != nil {
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

// sameProvider reports whether a and b refer to the exact same provider
// instance without panicking on uncomparable concrete types.
func sameProvider(a, b contract.StateProvider) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if !va.IsValid() || !vb.IsValid() || va.Type() != vb.Type() || !va.Type().Comparable() {
		return false
	}
	return a == b
}

// SetState updates one declared state key through p in SPC-001's exact order:
// refuse an undeclared key or unregistered provider before any provider call,
// take a Snapshot as the durable backup, call Write, and only then record the
// receipt row. A failed Write restores the snapshot under an uncancelled,
// bounded context and records no receipt row; a failed restore is joined as
// ErrStateRestoreFailed. One lock covers the whole sequence.
func (t *tx) SetState(ctx context.Context, p contract.StateProvider, key contract.StateKey, want contract.State) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("set state %q: %w", key, err)
	}
	if p == nil {
		return ErrNilStateProvider
	}
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if !t.keys[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	name := p.Name()
	reg, ok := t.e.lookupProvider(name)
	if !ok || !sameProvider(reg, p) {
		return fmt.Errorf("%w: %q", ErrUnregisteredProvider, name)
	}
	target := string(name) + ":" + string(key)
	return t.setStateLocked(ctx, p, key, target, want, contract.ActionSet, contract.RiskMedium)
}

func (t *tx) setStateLocked(
	ctx context.Context,
	p contract.StateProvider,
	key contract.StateKey,
	target string,
	want contract.State,
	action contract.ChangeAction,
	risk contract.Risk,
) error {
	if !t.keys[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("set state %q: %w", key, err)
	}
	if err := t.sinkErr(); err != nil {
		return err
	}
	name := p.Name()
	snap, err := p.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("snapshot %s before set state %q: %w", name, key, err)
	}
	if snap.Provider != name {
		return fmt.Errorf("%w: provider %q returned snapshot for %q", platform.ErrSnapshotProviderMismatch, name, snap.Provider)
	}

	var before contract.Digest
	prev, readErr := p.Read(ctx, key)
	switch {
	case readErr == nil:
		before = digest([]byte(prev.Value))
	case errors.Is(readErr, platform.ErrStateKeyNotFound) || errors.Is(readErr, fs.ErrNotExist):
		// Key absent before this write; before digest stays empty.
	default:
		return fmt.Errorf("read %s key %q before write: %w", name, key, readErr)
	}

	t.stateOpSeq++
	fileName := stateOpPrefix + strconv.Itoa(t.stateOpSeq)
	dir := t.e.backupDir(t.m.RunID)
	if err := t.e.atomicWrite(filepath.Join(dir, fileName), fileSpec{data: snap.Data, perm: 0o600}); err != nil {
		return err
	}
	if !t.m.hasState(name) {
		t.m.States = append(t.m.States, ManifestState{
			Provider: name,
			Digest:   digest(snap.Data),
			Data:     fileName,
		})
		if err := t.e.writeJSON(filepath.Join(dir, manifestFile), t.m); err != nil {
			return err
		}
	}

	t.e.fault(PointStateBackup)

	pkgTxn := name == contract.ProviderPackages
	writeCtx := platform.WithCommandRunner(ctx, t.containedRunner(pkgTxn))
	if err := p.Write(writeCtx, key, want); err != nil {
		writeErr := fmt.Errorf("write state %q: %w", key, err)
		if sinkErr := t.sinkErr(); errors.Is(sinkErr, ErrEmptinessUnknown) {
			return errors.Join(writeErr, sinkErr)
		}
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.e.opts.StateRestoreTimeout)
		defer cancel()
		restoreCtx = platform.WithCommandRunner(restoreCtx, t.containedRunner(pkgTxn))
		if restoreErr := p.Restore(restoreCtx, snap); restoreErr != nil {
			return errors.Join(writeErr, fmt.Errorf("%w for %q: %w", ErrStateRestoreFailed, key, restoreErr))
		}
		return writeErr
	}
	if sinkErr := t.sinkErr(); sinkErr != nil {
		return sinkErr
	}
	t.e.fault(PointStateWrite)

	ref := contract.BackupRef(string(t.m.RunID) + ":" + fileName)
	after := digest([]byte(want.Value))
	ch := contract.AppliedChange{
		Change: contract.Change{
			Target: target,
			Action: action,
			Before: before,
			After:  after,
			Risk:   risk,
		},
		Backup: ref,
	}
	t.mu.Lock()
	t.changes = append(t.changes, ch)
	t.mu.Unlock()
	t.Emit(contract.Event{Kind: contract.EventChange, Data: map[contract.DataKey]string{
		contract.DataPath: target, contract.DataAction: string(action), contract.DataDigest: string(after),
	}})
	t.e.fault(PointMutation)
	return nil
}

// containedCommandRunner adapts the engine's recorded containment to
// platform.CommandRunner so OS commands run by a StateProvider during SetState
// or recovery Restore are tracked in the intent record and drained before any
// subsequent restore.
type containedCommandRunner struct {
	e       *Engine
	l       *locks
	rec     *Record
	pkgTxn  bool
	onFail  func(error)
	sinkErr func() error
	emit    func(contract.Event)
	// mu guards rec when invoked from a tx; nil during single-threaded recovery.
	mu *sync.Mutex
}

func (e *Engine) containedStateRunner(
	l *locks,
	rec *Record,
	pkgTxn bool,
	onFail func(error),
	sinkErr func() error,
	emit func(contract.Event),
) platform.CommandRunner {
	return containedCommandRunner{
		e: e, l: l, rec: rec, pkgTxn: pkgTxn,
		onFail: onFail, sinkErr: sinkErr, emit: emit,
	}
}

func (t *tx) containedRunner(pkgTxn bool) platform.CommandRunner {
	return containedCommandRunner{
		e: t.e, l: t.l, rec: t.rec, pkgTxn: pkgTxn,
		onFail: t.fail, sinkErr: t.sinkErr, emit: t.Emit, mu: &t.mu,
	}
}

func (r containedCommandRunner) Run(ctx context.Context, spec platform.ExecSpec) (platform.ExecResult, error) {
	if len(spec.Argv) == 0 {
		return platform.ExecResult{}, platform.ErrCommandEmpty
	}
	if err := ctx.Err(); err != nil {
		return platform.ExecResult{}, fmt.Errorf("state command %s: %w", spec.Argv[0], err)
	}
	if r.sinkErr != nil {
		if err := r.sinkErr(); err != nil {
			return platform.ExecResult{}, err
		}
	}
	var outBuf, errBuf platform.SyncBuffer
	res, err := r.e.runContained(ctx, r.l, r.rec, r.mu, spec.Argv, spec.Env, spec.Dir, 0, r.pkgTxn, &outBuf, &errBuf, r.onFail, r.sinkErr, r.emit)
	return platform.ExecResult{
		ExitCode: res.ExitCode,
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		Duration: res.Duration,
	}, err
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
	return t.e.runContained(ctx, t.l, t.rec, &t.mu, c.Argv, t.toolEnv(c.EnvKeys), c.Dir, c.Timeout, c.PackageTransaction, t.e.opts.ToolLog, nil, t.fail, t.sinkErr, t.Emit)
}

func (e *Engine) nextCmdSeq(rec *Record, mu *sync.Mutex) int {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	rec.CmdSeq++
	return rec.CmdSeq
}

func (e *Engine) setRecordCommand(ctx context.Context, l *locks, rec *Record, mu *sync.Mutex, cmd *CommandRecord) error {
	var snap Record
	if mu != nil {
		mu.Lock()
		rec.Command = cmd
		snap = *rec
		mu.Unlock()
	} else {
		rec.Command = cmd
		snap = *rec
	}
	return l.withJournal(ctx, func() error { return e.writeRecord(&snap) })
}

func (e *Engine) runContained(
	ctx context.Context,
	l *locks,
	rec *Record,
	mu *sync.Mutex,
	argv, env []string,
	dir string,
	timeout time.Duration,
	pkgTxn bool,
	output, stderr io.Writer,
	onFail func(error),
	sinkErr func() error,
	emit func(contract.Event),
) (contract.CommandResult, error) {
	bk := context.WithoutCancel(ctx)
	seq := e.nextCmdSeq(rec, mu)
	name, err := e.opts.Containment.NameFor(rec.RunID, seq)
	if err != nil {
		return contract.CommandResult{}, fmt.Errorf("containment name: %w", err)
	}
	if err := e.setRecordCommand(bk, l, rec, mu, &CommandRecord{Name: name, PackageTransaction: pkgTxn}); err != nil {
		return contract.CommandResult{}, err
	}
	e.fault(PointLaunch)

	var sup platform.ProcessIdentity
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	switch {
	case pkgTxn:
		// Waited for, never killed: no cancel, grace or timeout ends it.
		runCtx = bk
	case timeout > 0:
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	if emit != nil {
		emit(contract.Event{Kind: contract.EventCommand, Data: map[contract.DataKey]string{contract.DataArgv0: argv[0]}})
	}
	res, err := e.opts.Containment.Run(runCtx, platform.LaunchSpec{
		Name: name, Scope: e.opts.Scope, Argv: argv, Env: env, Dir: dir,
		PackageTransaction: pkgTxn, Output: output, Stderr: stderr,
	}, func(ack platform.LaunchAck) error {
		// The supervisor waits at its gate until this is durable.
		sup = ack.Supervisor
		return e.setRecordCommand(bk, l, rec, mu, &CommandRecord{Name: name, PackageTransaction: pkgTxn, Supervisor: sup})
	})
	e.fault(PointCommandDone)
	if err != nil {
		err = fmt.Errorf("run %s: %w", argv[0], err)
	}
	// Prove the containment empty before moving on, whether the tool
	// succeeded, failed or was cancelled: a tool can leave descendants.
	// A package transaction's containment is only waited for.
	if emptyErr := e.empty(bk, name, sup, pkgTxn, 0); emptyErr != nil {
		blocked := fmt.Errorf("%w: command %s: %w", ErrEmptinessUnknown, argv[0], emptyErr)
		if onFail != nil {
			onFail(blocked)
		}
		return res, errors.Join(err, blocked)
	}
	if markErr := e.setRecordCommand(bk, l, rec, mu, &CommandRecord{Name: name, PackageTransaction: pkgTxn, Supervisor: sup, Finished: true}); markErr != nil {
		if onFail != nil {
			onFail(markErr)
		}
		return res, errors.Join(err, markErr)
	}
	e.fault(PointCommandEmptied)
	if err == nil && runCtx.Err() != nil {
		err = fmt.Errorf("run %s: %w", argv[0], runCtx.Err())
	}
	if sinkErr != nil {
		if failErr := sinkErr(); failErr != nil {
			err = errors.Join(err, failErr)
		}
	}
	if emit != nil {
		emit(contract.Event{Kind: contract.EventCommand, Data: map[contract.DataKey]string{
			contract.DataArgv0: argv[0], contract.DataExitCode: strconv.Itoa(res.ExitCode),
		}})
	}
	return res, err
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

// Emit numbers e, stamps it with the run, and delivers it. Progress events
// never wait on a slow non-progress log write (SPC-001 "Events").
func (t *tx) Emit(ev contract.Event) {
	if ev.Time.IsZero() {
		ev.Time = t.e.opts.Now().UTC()
	}
	ev.RunID, ev.Unit = t.m.RunID, t.desc.Unit
	if ev.Step == "" {
		ev.Step = contract.StepApply
	}
	ev = t.e.deliver(ev)
	t.mu.Lock()
	t.events = append(t.events, ev)
	t.mu.Unlock()
}

// RenderTemplate is not provided yet; the path check still runs first.
func (t *tx) RenderTemplate(ctx context.Context, path string, _ contract.TemplateID, _ contract.TemplateVars, _ fs.FileMode, _ contract.Owner) error {
	if _, err := t.begin(ctx, path); err != nil {
		return err
	}
	return fmt.Errorf("%w: RenderTemplate", ErrUnsupported)
}

// Package changes one declared package through the registered packages
// StateProvider (package manager only, never package-database files).
func (t *tx) Package(ctx context.Context, want contract.PackageState) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("package %q: %w", want.Name, err)
	}
	key := contract.StateKey(keyPackage + want.Name)
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if !t.keys[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	p, ok := t.e.lookupProvider(contract.ProviderPackages)
	if !ok {
		return fmt.Errorf("%w: state key %q", ErrUnsupported, key)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return fmt.Errorf("encode package %q: %w", want.Name, err)
	}
	action := contract.ActionCreate
	if !want.Present {
		action = contract.ActionRemove
	}
	return t.setStateLocked(ctx, p, key, string(key), contract.State{Value: string(raw)}, action, contract.RiskLow)
}

// Service is not provided yet; the declared-key check still runs first.
func (t *tx) Service(ctx context.Context, want contract.ServiceState) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service %q: %w", want.Name, err)
	}
	key := contract.StateKey(keyService + want.Name)
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if !t.keys[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	return fmt.Errorf("%w: state key %q", ErrUnsupported, key)
}

// UserGroup is not provided yet; the declared-key check still runs first.
func (t *tx) UserGroup(ctx context.Context, want contract.UserGroupState) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("user-group %s:%s: %w", want.User, want.Group, err)
	}
	key := contract.StateKey(keyUserGroup + want.User + ":" + want.Group)
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if !t.keys[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	return fmt.Errorf("%w: state key %q", ErrUnsupported, key)
}

// Check is not provided yet.
func (t *tx) Check(_ context.Context, c contract.CheckSpec) (contract.CheckResult, error) {
	return contract.CheckResult{Name: c.Name}, fmt.Errorf("%w: Check", ErrUnsupported)
}
