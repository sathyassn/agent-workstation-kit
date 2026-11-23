package contracttest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// Compile-time interface assertions for all fakes.
var (
	_ contract.Sink          = (*FakeSink)(nil)
	_ contract.StateProvider = (*FakeStateProvider)(nil)
	_ contract.DeclarativeTx = (*FakeDeclarativeTx)(nil)
	_ contract.Tx            = (*FakeTx)(nil)
	_ contract.Unit          = (*FileUnit)(nil)
	_ TxInspector            = (*FakeDeclarativeTx)(nil)
	_ TxInspector            = (*FakeTx)(nil)
)

// Sentinel errors returned by the in-memory fakes.
var (
	// ErrStateKeyNotFound reports a missing key in FakeStateProvider.
	ErrStateKeyNotFound = errors.New("state key not found")
	// ErrSnapshotProviderMismatch reports Restore called with another provider's snapshot.
	ErrSnapshotProviderMismatch = errors.New("snapshot provider mismatch")
	// ErrNilStateProvider reports SetState called with a nil StateProvider.
	ErrNilStateProvider = errors.New("state provider is nil")
	// ErrTemplateNotFound reports RenderTemplate called with an unknown TemplateID.
	ErrTemplateNotFound = errors.New("template not found")
	// ErrStateRestoreFailed reports that SetState could not restore the
	// provider's snapshot after a failed Write, so the provider may hold a
	// partial write.
	ErrStateRestoreFailed = errors.New("restore after failed state write")
)

const (
	defaultSinkCapacity = 256
	defaultFilePerm     = fs.FileMode(0o600)
	checkDetailOK       = "ok"
	// stateRestoreTimeout bounds the restore SetState runs after a failed
	// Write. The restore uses its own context, so a cancelled caller cannot
	// skip it.
	stateRestoreTimeout   = 30 * time.Second
	targetPrefixPackage   = "package:"
	targetPrefixService   = "service:"
	targetPrefixUserGroup = "user-group:"
)

// TxInspector exposes recorded changes, events and backing state from a
// transaction under test so contract tests can verify receipt recording,
// event order and that refused calls mutate nothing.
type TxInspector interface {
	Changes() []contract.AppliedChange
	Events() []contract.Event
	BackingState() []byte
	// Backup returns the bytes recorded under ref, so a check can restore
	// from the backup a receipt row names.
	Backup(ref contract.BackupRef) ([]byte, bool)
}

type blockKey struct {
	path string
	id   contract.BlockID
}

type blockBounds struct {
	begin string
	end   string
}

// FileEntry holds one in-memory file stored by FakeDeclarativeTx.
type FileEntry struct {
	Data  []byte
	Perm  fs.FileMode
	Owner contract.Owner
}

// FakeSink is a non-blocking, bounded in-memory implementation of contract.Sink.
type FakeSink struct {
	mu       sync.Mutex
	capacity int
	events   []contract.Event
	dropped  int
}

// NewFakeSink returns a FakeSink with the given event capacity. A non-positive
// capacity uses a sensible default.
func NewFakeSink(capacity int) *FakeSink {
	if capacity <= 0 {
		capacity = defaultSinkCapacity
	}
	return &FakeSink{capacity: capacity}
}

// Deliver records e without blocking. When full, excess events are counted in
// Dropped rather than waiting.
func (s *FakeSink) Deliver(e contract.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.events) >= s.capacity {
		s.dropped++
		return
	}
	s.events = append(s.events, cloneEvent(e))
}

// Events returns a fresh copy of delivered events in delivery order.
func (s *FakeSink) Events() []contract.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]contract.Event, len(s.events))
	for i, e := range s.events {
		out[i] = cloneEvent(e)
	}
	return out
}

// Dropped returns the number of events dropped because the sink was full.
func (s *FakeSink) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dropped
}

// FakeStateProvider is an in-memory implementation of contract.StateProvider.
type FakeStateProvider struct {
	mu     sync.Mutex
	name   contract.StateProviderName
	values map[contract.StateKey]contract.State
}

// NewFakeStateProvider creates a FakeStateProvider seeded with initial state.
func NewFakeStateProvider(name contract.StateProviderName, initial map[contract.StateKey]contract.State) *FakeStateProvider {
	vals := make(map[contract.StateKey]contract.State, len(initial))
	for k, v := range initial {
		vals[k] = v
	}
	return &FakeStateProvider{name: name, values: vals}
}

// Name returns the provider's canonical name.
func (p *FakeStateProvider) Name() contract.StateProviderName {
	return p.name
}

// Read returns the current value for key or ErrStateKeyNotFound.
func (p *FakeStateProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	if err := ctx.Err(); err != nil {
		return contract.State{}, fmt.Errorf("read state %q: %w", key, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	v, ok := p.values[key]
	if !ok {
		return contract.State{}, fmt.Errorf("state key %q: %w", key, ErrStateKeyNotFound)
	}
	return v, nil
}

// Write sets key to want in memory.
func (p *FakeStateProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write state %q: %w", key, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	p.values[key] = want
	return nil
}

// Snapshot serialises the provider's full state for later Restore.
func (p *FakeStateProvider) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, fmt.Errorf("snapshot %q: %w", p.name, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := json.Marshal(p.values)
	if err != nil {
		return contract.Snapshot{}, fmt.Errorf("marshal snapshot for %q: %w", p.name, err)
	}
	return contract.Snapshot{Provider: p.name, Data: data}, nil
}

// Restore replaces the provider's state from s after checking s.Provider.
func (p *FakeStateProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("restore %q: %w", p.name, err)
	}
	if s.Provider != p.name {
		return fmt.Errorf("snapshot for %q cannot restore %q: %w", s.Provider, p.name, ErrSnapshotProviderMismatch)
	}
	var restored map[contract.StateKey]contract.State
	if err := json.Unmarshal(s.Data, &restored); err != nil {
		return fmt.Errorf("unmarshal snapshot for %q: %w", p.name, err)
	}
	if restored == nil {
		restored = make(map[contract.StateKey]contract.State)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	p.values = restored
	return nil
}

// FakeDeclarativeTx is an in-memory implementation of contract.DeclarativeTx
// that enforces Description boundaries, backs up before mutation, records
// changes for receipts and keeps emitted events in order.
type FakeDeclarativeTx struct {
	mu           sync.Mutex
	desc         contract.Description
	ownedPaths   map[string]bool
	ownedStates  map[contract.StateKey]bool
	declaredEnv  map[string]bool
	sink         contract.Sink
	files        map[string]FileEntry
	blockMarkers map[blockKey]blockBounds
	templates    map[contract.TemplateID]string
	packages     map[string]contract.PackageState
	services     map[string]contract.ServiceState
	userGroups   map[string]contract.UserGroupState
	checks       map[contract.CheckName]contract.CheckResult
	backups      map[contract.BackupRef][]byte
	changes      []contract.AppliedChange
	events       []contract.Event
	nextBackup   int
}

// NewFakeDeclarativeTx creates an in-memory DeclarativeTx bound to desc and an
// optional sink.
func NewFakeDeclarativeTx(desc contract.Description, sink contract.Sink) *FakeDeclarativeTx {
	ownedPaths := make(map[string]bool, len(desc.OwnedPaths))
	for _, p := range desc.OwnedPaths {
		ownedPaths[filepath.Clean(p)] = true
	}
	ownedStates := make(map[contract.StateKey]bool, len(desc.OwnedStateKeys))
	for _, k := range desc.OwnedStateKeys {
		ownedStates[k] = true
	}
	declaredEnv := make(map[string]bool, len(desc.DeclaredEnv))
	for _, k := range desc.DeclaredEnv {
		declaredEnv[k] = true
	}
	return &FakeDeclarativeTx{
		desc:         desc,
		ownedPaths:   ownedPaths,
		ownedStates:  ownedStates,
		declaredEnv:  declaredEnv,
		sink:         sink,
		files:        make(map[string]FileEntry),
		blockMarkers: make(map[blockKey]blockBounds),
		templates:    make(map[contract.TemplateID]string),
		packages:     make(map[string]contract.PackageState),
		services:     make(map[string]contract.ServiceState),
		userGroups:   make(map[string]contract.UserGroupState),
		checks:       make(map[contract.CheckName]contract.CheckResult),
		backups:      make(map[contract.BackupRef][]byte),
	}
}

// SetTemplate registers an in-memory template body for RenderTemplate.
func (tx *FakeDeclarativeTx) SetTemplate(id contract.TemplateID, body string) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.templates[id] = body
}

// SeedFile populates an existing file before apply without recording a change.
func (tx *FakeDeclarativeTx) SeedFile(path string, data []byte, perm fs.FileMode, owner contract.Owner) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.files[filepath.Clean(path)] = FileEntry{
		Data:  bytes.Clone(data),
		Perm:  perm,
		Owner: owner,
	}
}

// SeedBlockMarkers records begin and end markers for a pre-seeded managed block.
func (tx *FakeDeclarativeTx) SeedBlockMarkers(path string, id contract.BlockID, begin, end string) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.blockMarkers[blockKey{path: filepath.Clean(path), id: id}] = blockBounds{begin: begin, end: end}
}

// SeedPackage populates an existing package state without recording a change.
func (tx *FakeDeclarativeTx) SeedPackage(state contract.PackageState) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.packages[state.Name] = state
}

// SeedService populates an existing service state without recording a change.
func (tx *FakeDeclarativeTx) SeedService(state contract.ServiceState) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.services[state.Name] = state
}

// SeedUserGroup populates an existing user-group state without recording a change.
func (tx *FakeDeclarativeTx) SeedUserGroup(state contract.UserGroupState) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.userGroups[state.User+":"+state.Group] = state
}

// SetCheckResult registers a canned response for Check.
func (tx *FakeDeclarativeTx) SetCheckResult(res contract.CheckResult) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	tx.checks[res.Name] = res
}

// WriteFile writes data to a declared path, backing up any prior content and
// recording the change for the receipt.
func (tx *FakeDeclarativeTx) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode, owner contract.Owner) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	clean := filepath.Clean(path)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedPaths[clean] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	tx.writeFileLocked(clean, data, perm, owner)
	return nil
}

// RenderTemplate renders a registered template into a declared path.
func (tx *FakeDeclarativeTx) RenderTemplate(ctx context.Context, path string, tpl contract.TemplateID, vars contract.TemplateVars, perm fs.FileMode, owner contract.Owner) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("render template %q: %w", path, err)
	}
	clean := filepath.Clean(path)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedPaths[clean] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	body, ok := tx.templates[tpl]
	if !ok {
		return fmt.Errorf("template %q: %w", tpl, ErrTemplateNotFound)
	}
	rendered := body
	for k, v := range vars {
		rendered = strings.ReplaceAll(rendered, "{{"+k+"}}", v)
	}
	tx.writeFileLocked(clean, []byte(rendered), perm, owner)
	return nil
}

// EnsureBlock inserts or replaces a marked block in a declared path while
// keeping surrounding bytes identical.
func (tx *FakeDeclarativeTx) EnsureBlock(ctx context.Context, path string, b contract.ManagedBlock) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ensure block %q: %w", path, err)
	}
	clean := filepath.Clean(path)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedPaths[clean] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	prev := tx.files[clean]
	block := b.Begin + "\n" + b.Body + "\n" + b.End + "\n"
	existing := string(prev.Data)
	var next string
	if start, end, found := findBlockRange(existing, b.Begin, b.End); found {
		next = existing[:start] + block + existing[end:]
	} else {
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		next = existing + block
	}
	perm := prev.Perm
	if perm == 0 {
		perm = defaultFilePerm
	}
	tx.blockMarkers[blockKey{path: clean, id: b.ID}] = blockBounds{begin: b.Begin, end: b.End}
	tx.writeFileLocked(clean, []byte(next), perm, prev.Owner)
	return nil
}

// RemoveBlock removes the exact marked block between the begin and end markers
// recorded for id in a declared path, leaving surrounding bytes identical.
func (tx *FakeDeclarativeTx) RemoveBlock(ctx context.Context, path string, id contract.BlockID) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("remove block %q: %w", path, err)
	}
	clean := filepath.Clean(path)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedPaths[clean] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	prev, exists := tx.files[clean]
	if !exists {
		return nil
	}
	bounds, ok := tx.blockMarkers[blockKey{path: clean, id: id}]
	if !ok {
		return nil
	}
	existing := string(prev.Data)
	start, end, found := findBlockRange(existing, bounds.begin, bounds.end)
	if !found {
		return nil
	}
	next := existing[:start] + existing[end:]
	tx.writeFileLocked(clean, []byte(next), prev.Perm, prev.Owner)
	return nil
}

// findBlockRange locates the exact byte range spanning a whole-line begin
// marker through its matching whole-line end marker (including the end line's
// trailing newline, if any).
func findBlockRange(content, begin, end string) (int, int, bool) {
	blockStart := -1
	offset := 0
	for offset < len(content) {
		rel := strings.IndexByte(content[offset:], '\n')
		var lineEnd, nextOffset int
		if rel < 0 {
			lineEnd = len(content)
			nextOffset = len(content)
		} else {
			lineEnd = offset + rel
			nextOffset = lineEnd + 1
		}
		line := strings.TrimSuffix(content[offset:lineEnd], "\r")
		if blockStart < 0 {
			if line == begin {
				blockStart = offset
			}
		} else if line == end {
			return blockStart, nextOffset, true
		}
		offset = nextOffset
	}
	return 0, 0, false
}

// Remove deletes a declared path after backing up any existing content.
func (tx *FakeDeclarativeTx) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	clean := filepath.Clean(path)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedPaths[clean] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetPath, Target: path}
	}
	prev, exists := tx.files[clean]
	var before contract.Digest
	var backup contract.BackupRef
	if exists {
		before = digestBytes(prev.Data)
		backup = tx.recordBackupLocked(prev.Data)
		delete(tx.files, clean)
	}
	tx.changes = append(tx.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: clean,
			Action: contract.ActionRemove,
			Before: before,
			Risk:   contract.RiskLow,
		},
		Backup: backup,
	})
	return nil
}

// Package records the wanted package state after checking that the target is
// declared in Description.OwnedStateKeys, snapshotting prior state and
// recording a backup reference for the receipt.
func (tx *FakeDeclarativeTx) Package(ctx context.Context, want contract.PackageState) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("package %q: %w", want.Name, err)
	}
	key := contract.StateKey(targetPrefixPackage + want.Name)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedStates[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	prev, existed := tx.packages[want.Name]
	if !existed {
		prev = contract.PackageState{Name: want.Name}
	}
	priorData, err := json.Marshal(prev)
	if err != nil {
		return fmt.Errorf("snapshot package %q: %w", want.Name, err)
	}
	wantData, err := json.Marshal(want)
	if err != nil {
		return fmt.Errorf("marshal package %q: %w", want.Name, err)
	}
	var before contract.Digest
	if existed {
		before = digestBytes(priorData)
	}
	backup := tx.recordBackupLocked(priorData)
	tx.packages[want.Name] = want
	action := contract.ActionCreate
	switch {
	case !want.Present:
		action = contract.ActionRemove
	case existed:
		action = contract.ActionReplace
	}
	tx.changes = append(tx.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: string(key),
			Action: action,
			Before: before,
			After:  digestBytes(wantData),
			Risk:   contract.RiskLow,
		},
		Backup: backup,
	})
	return nil
}

// Service records the wanted service state after checking that the target is
// declared in Description.OwnedStateKeys, snapshotting prior state and
// recording a backup reference for the receipt.
func (tx *FakeDeclarativeTx) Service(ctx context.Context, want contract.ServiceState) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service %q: %w", want.Name, err)
	}
	key := contract.StateKey(targetPrefixService + want.Name)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedStates[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	prev, existed := tx.services[want.Name]
	if !existed {
		prev = contract.ServiceState{Name: want.Name}
	}
	priorData, err := json.Marshal(prev)
	if err != nil {
		return fmt.Errorf("snapshot service %q: %w", want.Name, err)
	}
	wantData, err := json.Marshal(want)
	if err != nil {
		return fmt.Errorf("marshal service %q: %w", want.Name, err)
	}
	var before contract.Digest
	if existed {
		before = digestBytes(priorData)
	}
	backup := tx.recordBackupLocked(priorData)
	tx.services[want.Name] = want
	tx.changes = append(tx.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: string(key),
			Action: contract.ActionSet,
			Before: before,
			After:  digestBytes(wantData),
			Risk:   contract.RiskMedium,
		},
		Backup: backup,
	})
	return nil
}

// UserGroup records the wanted user-group membership after checking that the
// target is declared in Description.OwnedStateKeys, snapshotting prior state
// and recording a backup reference for the receipt.
func (tx *FakeDeclarativeTx) UserGroup(ctx context.Context, want contract.UserGroupState) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("user-group %s:%s: %w", want.User, want.Group, err)
	}
	ugKey := want.User + ":" + want.Group
	key := contract.StateKey(targetPrefixUserGroup + ugKey)
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if !tx.ownedStates[key] {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}
	prev, existed := tx.userGroups[ugKey]
	if !existed {
		prev = contract.UserGroupState{User: want.User, Group: want.Group}
	}
	priorData, err := json.Marshal(prev)
	if err != nil {
		return fmt.Errorf("snapshot user-group %q: %w", ugKey, err)
	}
	wantData, err := json.Marshal(want)
	if err != nil {
		return fmt.Errorf("marshal user-group %q: %w", ugKey, err)
	}
	var before contract.Digest
	if existed {
		before = digestBytes(priorData)
	}
	backup := tx.recordBackupLocked(priorData)
	tx.userGroups[ugKey] = want
	tx.changes = append(tx.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: string(key),
			Action: contract.ActionSet,
			Before: before,
			After:  digestBytes(wantData),
			Risk:   contract.RiskMedium,
		},
		Backup: backup,
	})
	return nil
}

// Check runs a read-only check against canned results (defaulting to OK).
func (tx *FakeDeclarativeTx) Check(ctx context.Context, c contract.CheckSpec) (contract.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.CheckResult{}, fmt.Errorf("check %q: %w", c.Name, err)
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if res, ok := tx.checks[c.Name]; ok {
		return res, nil
	}
	return contract.CheckResult{Name: c.Name, OK: true, Detail: checkDetailOK}, nil
}

// Emit appends e in emission order and forwards it to the configured Sink.
func (tx *FakeDeclarativeTx) Emit(e contract.Event) {
	copied := cloneEvent(e)

	tx.mu.Lock()
	tx.events = append(tx.events, copied)
	sink := tx.sink
	tx.mu.Unlock()

	if sink != nil {
		sink.Deliver(copied)
	}
}

// Changes returns a fresh copy of all recorded receipt changes.
func (tx *FakeDeclarativeTx) Changes() []contract.AppliedChange {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	out := make([]contract.AppliedChange, len(tx.changes))
	copy(out, tx.changes)
	return out
}

// Events returns a fresh copy of all emitted events in order.
func (tx *FakeDeclarativeTx) Events() []contract.Event {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	out := make([]contract.Event, len(tx.events))
	for i, e := range tx.events {
		out[i] = cloneEvent(e)
	}
	return out
}

// File reads an in-memory file written through the transaction.
func (tx *FakeDeclarativeTx) File(path string) (FileEntry, bool) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	entry, ok := tx.files[filepath.Clean(path)]
	if !ok {
		return FileEntry{}, false
	}
	entry.Data = bytes.Clone(entry.Data)
	return entry, true
}

// Backup returns the bytes saved under ref before a mutation.
func (tx *FakeDeclarativeTx) Backup(ref contract.BackupRef) ([]byte, bool) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	data, ok := tx.backups[ref]
	if !ok {
		return nil, false
	}
	return bytes.Clone(data), true
}

// Receipt constructs a contract.Receipt from the transaction's recorded changes.
func (tx *FakeDeclarativeTx) Receipt(runID contract.RunID, planDigest contract.Digest, outcome contract.Outcome) contract.Receipt {
	return contract.Receipt{
		RunID:      runID,
		Unit:       tx.desc.Unit,
		PlanDigest: planDigest,
		Changes:    tx.Changes(),
		Time:       time.Now().UTC(),
		Outcome:    outcome,
	}
}

func (tx *FakeDeclarativeTx) writeFileLocked(clean string, data []byte, perm fs.FileMode, owner contract.Owner) {
	prev, existed := tx.files[clean]
	action := contract.ActionCreate
	var before contract.Digest
	var backup contract.BackupRef
	if existed {
		action = contract.ActionReplace
		before = digestBytes(prev.Data)
		backup = tx.recordBackupLocked(prev.Data)
	}
	tx.files[clean] = FileEntry{
		Data:  bytes.Clone(data),
		Perm:  perm,
		Owner: owner,
	}
	tx.changes = append(tx.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: clean,
			Action: action,
			Before: before,
			After:  digestBytes(data),
			Risk:   contract.RiskLow,
		},
		Backup: backup,
	})
}

func (tx *FakeDeclarativeTx) recordBackupLocked(data []byte) contract.BackupRef {
	tx.nextBackup++
	ref := contract.BackupRef(fmt.Sprintf("backup-%04d", tx.nextBackup))
	tx.backups[ref] = bytes.Clone(data)
	return ref
}

type declarativeBackingState struct {
	Files      map[string]FileEntry               `json:"files"`
	Packages   map[string]contract.PackageState   `json:"packages"`
	Services   map[string]contract.ServiceState   `json:"services"`
	UserGroups map[string]contract.UserGroupState `json:"user_groups"`
	Backups    map[contract.BackupRef][]byte      `json:"backups"`
	Changes    []contract.AppliedChange           `json:"changes"`
}

func (tx *FakeDeclarativeTx) declarativeStateLocked() declarativeBackingState {
	return declarativeBackingState{
		Files:      tx.files,
		Packages:   tx.packages,
		Services:   tx.services,
		UserGroups: tx.userGroups,
		Backups:    tx.backups,
		Changes:    tx.changes,
	}
}

// BackingState returns a deterministic snapshot of the fake transaction's
// backing state so contract tests can prove refused calls leave state intact.
func (tx *FakeDeclarativeTx) BackingState() []byte {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	raw, err := json.Marshal(tx.declarativeStateLocked())
	if err != nil {
		panic(fmt.Sprintf("marshal fake backing state: %v", err))
	}
	return raw
}

// FakeTx is an in-memory implementation of contract.Tx extending
// FakeDeclarativeTx with SetState and Run.
type FakeTx struct {
	*FakeDeclarativeTx
	// stateMu serializes SetState across snapshot, write and receipt, so
	// two calls never interleave their backups and writes.
	stateMu  sync.Mutex
	runMu    sync.Mutex
	commands []contract.Command
	runFn    func(ctx context.Context, c contract.Command) (contract.CommandResult, error)
}

// NewFakeTx creates an in-memory Tx bound to desc and an optional sink.
func NewFakeTx(desc contract.Description, sink contract.Sink) *FakeTx {
	return &FakeTx{
		FakeDeclarativeTx: NewFakeDeclarativeTx(desc, sink),
	}
}

// BackingState returns a deterministic snapshot of the fake transaction's
// backing state including recorded commands.
func (tx *FakeTx) BackingState() []byte {
	tx.mu.Lock()
	decl := tx.declarativeStateLocked()
	tx.runMu.Lock()
	full := struct {
		Declarative declarativeBackingState `json:"declarative"`
		Commands    []contract.Command      `json:"commands"`
	}{
		Declarative: decl,
		Commands:    tx.commands,
	}
	raw, err := json.Marshal(full)
	tx.runMu.Unlock()
	tx.mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("marshal fake tx backing state: %v", err))
	}
	return raw
}

// SetRunner installs an optional handler invoked by Run after env checks pass.
func (tx *FakeTx) SetRunner(fn func(ctx context.Context, c contract.Command) (contract.CommandResult, error)) {
	tx.runMu.Lock()
	defer tx.runMu.Unlock()

	tx.runFn = fn
}

// SetState refuses an undeclared key before any provider call, snapshots the
// provider as the backup, writes the key through the contract's Write, and
// only then logs the change for the receipt. A failed Write is undone by
// restoring the snapshot; a failed restore is reported as
// ErrStateRestoreFailed alongside the Write error. The restore runs under
// its own bounded context, so it still runs when the caller's context was
// cancelled during the Write. No receipt row is recorded for a failed Write.
// One mutex covers the whole sequence.
func (tx *FakeTx) SetState(ctx context.Context, p contract.StateProvider, key contract.StateKey, want contract.State) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("set state %q: %w", key, err)
	}
	if p == nil {
		return ErrNilStateProvider
	}

	tx.stateMu.Lock()
	defer tx.stateMu.Unlock()

	tx.mu.Lock()
	allowed := tx.ownedStates[key]
	tx.mu.Unlock()
	if !allowed {
		return &contract.UndeclaredTargetError{Kind: contract.TargetStateKey, Target: string(key)}
	}

	snap, err := p.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("snapshot before set state %q: %w", key, err)
	}

	var before contract.Digest
	if prev, readErr := p.Read(ctx, key); readErr == nil {
		before = digestBytes([]byte(prev.Value))
	}

	if err := p.Write(ctx, key, want); err != nil {
		writeErr := fmt.Errorf("write state %q: %w", key, err)
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stateRestoreTimeout)
		defer cancel()
		if restoreErr := p.Restore(restoreCtx, snap); restoreErr != nil {
			return errors.Join(writeErr, fmt.Errorf("%w for %q: %w", ErrStateRestoreFailed, key, restoreErr))
		}
		return writeErr
	}

	tx.mu.Lock()
	backup := tx.recordBackupLocked(snap.Data)
	tx.changes = append(tx.changes, contract.AppliedChange{
		Change: contract.Change{
			Target: string(p.Name()) + ":" + string(key),
			Action: contract.ActionSet,
			Before: before,
			After:  digestBytes([]byte(want.Value)),
			Risk:   contract.RiskMedium,
		},
		Backup: backup,
	})
	tx.mu.Unlock()
	return nil
}

// Run records c after verifying every requested environment key is declared in
// Description.DeclaredEnv.
func (tx *FakeTx) Run(ctx context.Context, c contract.Command) (contract.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.CommandResult{}, fmt.Errorf("run command: %w", err)
	}
	tx.mu.Lock()
	for _, k := range c.EnvKeys {
		if !tx.declaredEnv[k] {
			tx.mu.Unlock()
			return contract.CommandResult{}, &contract.UndeclaredTargetError{Kind: contract.TargetEnvKey, Target: k}
		}
	}
	tx.mu.Unlock()

	tx.runMu.Lock()
	tx.commands = append(tx.commands, c)
	fn := tx.runFn
	tx.runMu.Unlock()

	if fn != nil {
		res, err := fn(ctx, c)
		if err != nil {
			return res, fmt.Errorf("run command %v: %w", c.Argv, err)
		}
		return res, nil
	}
	return contract.CommandResult{ExitCode: 0, Duration: 0}, nil
}

// Commands returns a fresh copy of all commands passed to Run.
func (tx *FakeTx) Commands() []contract.Command {
	tx.runMu.Lock()
	defer tx.runMu.Unlock()

	out := make([]contract.Command, len(tx.commands))
	copy(out, tx.commands)
	return out
}

// FileUnit is a fake contract.Unit that declares and writes one file.
type FileUnit struct {
	mu      sync.Mutex
	id      contract.UnitID
	scope   contract.Scope
	path    string
	content []byte
	applied bool
	seq     uint64
}

// NewFileUnit returns a FileUnit that declares path and writes content.
func NewFileUnit(id contract.UnitID, path string, content []byte) *FileUnit {
	return &FileUnit{
		id:      id,
		scope:   contract.ScopeUser,
		path:    filepath.Clean(path),
		content: bytes.Clone(content),
	}
}

// ID returns the unit's identifier.
func (u *FileUnit) ID() contract.UnitID {
	return u.id
}

// Describe declares the single file path this unit owns.
func (u *FileUnit) Describe(ctx context.Context) (contract.Description, error) {
	if err := ctx.Err(); err != nil {
		return contract.Description{}, fmt.Errorf("describe %s: %w", u.id, err)
	}
	return contract.Description{
		Unit:           u.id,
		Scope:          u.scope,
		DependsOn:      []contract.UnitID{},
		Capabilities:   []contract.Capability{},
		OwnedPaths:     []string{u.path},
		OwnedStateKeys: []contract.StateKey{},
		Irreversible:   []string{},
		DeclaredEnv:    []string{},
	}, nil
}

// Plan returns a deterministic single-file change plan.
func (u *FileUnit) Plan(ctx context.Context, _ contract.Inputs) (contract.Plan, error) {
	if err := ctx.Err(); err != nil {
		return contract.Plan{}, fmt.Errorf("plan %s: %w", u.id, err)
	}
	after := digestBytes(u.content)
	digest := digestBytes([]byte(string(u.id) + ":" + u.path + ":" + string(after)))
	return contract.Plan{
		ID:     contract.PlanID("plan-" + string(u.id)),
		Unit:   u.id,
		Digest: digest,
		Changes: []contract.Change{
			{
				Target: u.path,
				Action: contract.ActionCreate,
				After:  after,
				Risk:   contract.RiskLow,
			},
		},
		NonDefaults: []contract.ConfigKey{},
		BlockedBy:   []string{},
		CannotUndo:  []string{},
		Outcome:     nil,
	}, nil
}

// Apply writes the declared file through tx and emits ordered events.
func (u *FileUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("apply %s: %w", u.id, err)
	}
	tx.Emit(u.nextEvent(contract.StepApply, contract.EventStepStart, "starting file apply", nil))
	if err := tx.WriteFile(ctx, u.path, u.content, defaultFilePerm, contract.Owner{}); err != nil {
		return fmt.Errorf("apply %s write %q: %w", u.id, u.path, err)
	}
	tx.Emit(u.nextEvent(contract.StepApply, contract.EventChange, "wrote declared file", map[contract.DataKey]string{
		contract.DataPath:   u.path,
		contract.DataAction: string(contract.ActionCreate),
		contract.DataDigest: string(digestBytes(u.content)),
	}))
	tx.Emit(u.nextEvent(contract.StepApply, contract.EventStepEnd, "finished file apply", nil))

	u.mu.Lock()
	u.applied = true
	u.mu.Unlock()
	return nil
}

// Verify checks whether Apply has been run on this fake unit.
func (u *FileUnit) Verify(ctx context.Context, _ contract.Plan) (contract.Result, error) {
	if err := ctx.Err(); err != nil {
		return contract.Result{}, fmt.Errorf("verify %s: %w", u.id, err)
	}
	u.mu.Lock()
	applied := u.applied
	u.mu.Unlock()

	outcome := contract.OutcomeFailed
	detail := "declared file not applied"
	if applied {
		outcome = contract.OutcomeVerified
		detail = checkDetailOK
	}
	return contract.Result{
		Unit:    u.id,
		Outcome: outcome,
		Checks: []contract.CheckResult{
			{Name: "file-present", OK: applied, Detail: detail},
		},
	}, nil
}

// Revert removes the declared file through tx.
func (u *FileUnit) Revert(ctx context.Context, tx contract.Tx, _ contract.Receipt) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("revert %s: %w", u.id, err)
	}
	tx.Emit(u.nextEvent(contract.StepRevert, contract.EventStepStart, "starting file revert", nil))
	if err := tx.Remove(ctx, u.path); err != nil {
		return fmt.Errorf("revert %s remove %q: %w", u.id, u.path, err)
	}
	tx.Emit(u.nextEvent(contract.StepRevert, contract.EventStepEnd, "finished file revert", nil))

	u.mu.Lock()
	u.applied = false
	u.mu.Unlock()
	return nil
}

// Status reports whether the declared file is currently applied.
func (u *FileUnit) Status(ctx context.Context) (contract.Status, error) {
	if err := ctx.Err(); err != nil {
		return contract.Status{}, fmt.Errorf("status %s: %w", u.id, err)
	}
	u.mu.Lock()
	applied := u.applied
	u.mu.Unlock()

	return contract.Status{
		Unit:    u.id,
		Applied: applied,
		Drift:   []string{},
	}, nil
}

func (u *FileUnit) nextEvent(step contract.StepID, kind contract.EventKind, text string, data map[contract.DataKey]string) contract.Event {
	u.mu.Lock()
	u.seq++
	seq := u.seq
	u.mu.Unlock()

	return contract.Event{
		Seq:   seq,
		Time:  time.Now().UTC(),
		RunID: contract.RunID("run-lab-ws-01"),
		Unit:  u.id,
		Step:  step,
		Kind:  kind,
		Text:  text,
		Data:  data,
	}
}

func digestBytes(data []byte) contract.Digest {
	sum := sha256.Sum256(data)
	return contract.Digest(hex.EncodeToString(sum[:]))
}

func cloneEvent(e contract.Event) contract.Event {
	if e.Data == nil {
		return e
	}
	cp := make(map[contract.DataKey]string, len(e.Data))
	for k, v := range e.Data {
		cp[k] = v
	}
	e.Data = cp
	return e
}
