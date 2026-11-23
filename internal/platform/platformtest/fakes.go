// Package platformtest holds in-memory fakes of the platform adapters for
// engine tests: a process table with identities, and a containment.
package platformtest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// ErrInspect is returned by Alive after FailInspection.
var ErrInspect = errors.New("fake inspection failure")

// ProcessTable is a fake set of live processes on one boot.
type ProcessTable struct {
	mu         sync.Mutex
	boot       string
	alive      map[platform.ProcessIdentity]bool
	next       int
	failErr    error
	unkillable map[platform.ProcessIdentity]bool
	signalled  []platform.ProcessIdentity
}

// NewProcessTable returns an empty table on boot.
func NewProcessTable(boot string) *ProcessTable {
	return &ProcessTable{boot: boot, alive: map[platform.ProcessIdentity]bool{}, next: 1000}
}

// Spawn adds a new live process and returns its identity.
func (t *ProcessTable) Spawn() platform.ProcessIdentity {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	id := platform.ProcessIdentity{BootID: t.boot, PID: t.next, StartTime: "t" + strconv.Itoa(t.next)}
	t.alive[id] = true
	return id
}

// Kill marks id dead.
func (t *ProcessTable) Kill(id platform.ProcessIdentity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.alive, id)
}

// Revive marks id alive, as if it were a process on this table's boot.
func (t *ProcessTable) Revive(id platform.ProcessIdentity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.alive[id] = true
}

// FailInspection makes Alive return err (nil clears it).
func (t *ProcessTable) FailInspection(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failErr = err
}

// Identity returns the live process with this PID.
func (t *ProcessTable) Identity(_ context.Context, pid int) (platform.ProcessIdentity, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.alive {
		if id.PID == pid {
			return id, nil
		}
	}
	return platform.ProcessIdentity{}, fmt.Errorf("%w: no process %d", platform.ErrInvalidProcessIdentity, pid)
}

// Alive reports whether id is a live process on the table's boot.
func (t *ProcessTable) Alive(ctx context.Context, id platform.ProcessIdentity) (bool, error) {
	return t.Inspector(platform.ProcessIdentity{}).Alive(ctx, id)
}

// isAlive reports whether id is alive, ignoring FailInspection.
func (t *ProcessTable) isAlive(id platform.ProcessIdentity) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.alive[id]
}

// SetUnkillable makes signals to id have no effect.
func (t *ProcessTable) SetUnkillable(id platform.ProcessIdentity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.unkillable == nil {
		t.unkillable = map[platform.ProcessIdentity]bool{}
	}
	t.unkillable[id] = true
}

// Signal kills id unless it is unkillable. A dead process is not an error.
func (t *ProcessTable) Signal(_ context.Context, id platform.ProcessIdentity, _ syscall.Signal) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.signalled = append(t.signalled, id)
	if !t.unkillable[id] {
		delete(t.alive, id)
	}
	return nil
}

// Signalled returns every identity signalled so far.
func (t *ProcessTable) Signalled() []platform.ProcessIdentity {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]platform.ProcessIdentity(nil), t.signalled...)
}

// Inspector returns an inspector whose own identity is self.
func (t *ProcessTable) Inspector(self platform.ProcessIdentity) *Inspector {
	return &Inspector{table: t, self: self}
}

// Inspector is a fake process inspector over a ProcessTable.
type Inspector struct {
	table *ProcessTable
	self  platform.ProcessIdentity
}

// Self returns the inspector's own identity.
func (i *Inspector) Self(ctx context.Context) (platform.ProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return platform.ProcessIdentity{}, fmt.Errorf("fake self: %w", err)
	}
	return i.self, nil
}

// Alive reports whether id is a live process on the table's boot.
func (i *Inspector) Alive(ctx context.Context, id platform.ProcessIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("fake alive: %w", err)
	}
	if !id.Valid() {
		return false, fmt.Errorf("%w: %+v", platform.ErrInvalidProcessIdentity, id)
	}
	i.table.mu.Lock()
	defer i.table.mu.Unlock()
	if i.table.failErr != nil {
		return false, i.table.failErr
	}
	return id.BootID == i.table.boot && i.table.alive[id], nil
}

// EmptyCall records one call to Containment.Empty.
type EmptyCall struct {
	Name string
	Opts platform.EmptyOptions
}

// RunFunc is a fake tool. It runs while the containment is busy.
type RunFunc func(ctx context.Context, spec platform.LaunchSpec) (contract.CommandResult, error)

// Containment is a fake containment. A name is busy while a fake tool runs
// in it, or after SetBusy, as if a crashed run had left processes behind.
type Containment struct {
	mu         sync.Mutex
	table      *ProcessTable
	run        RunFunc
	empty      func(name string, opts platform.EmptyOptions)
	busy       map[string]bool
	unkillable map[string]bool
	killed     map[string]bool
	empties    []EmptyCall
	launched   []platform.LaunchSpec
}

// NewContainment returns a fake containment whose tools get identities from
// table.
func NewContainment(table *ProcessTable) *Containment {
	return &Containment{
		table:      table,
		busy:       map[string]bool{},
		unkillable: map[string]bool{},
		killed:     map[string]bool{},
	}
}

// SetRun installs the fake tool every Run executes.
func (c *Containment) SetRun(fn RunFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.run = fn
}

// OnEmpty installs a hook called at the start of every Empty.
func (c *Containment) OnEmpty(fn func(name string, opts platform.EmptyOptions)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.empty = fn
}

// SetBusy marks name as holding processes, or not.
func (c *Containment) SetBusy(name string, busy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy[name] = busy
}

// SetUnkillable makes Empty fail for name even after it signals.
func (c *Containment) SetUnkillable(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unkillable[name] = true
}

// Killed reports whether Empty signalled name.
func (c *Containment) Killed(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killed[name]
}

// Busy reports whether name holds processes.
func (c *Containment) Busy(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy[name]
}

// EmptyCalls returns every Empty call so far.
func (c *Containment) EmptyCalls() []EmptyCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]EmptyCall(nil), c.empties...)
}

// Launched returns every launch so far.
func (c *Containment) Launched() []platform.LaunchSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]platform.LaunchSpec(nil), c.launched...)
}

// NameFor returns "fake-<run>-<seq>".
func (c *Containment) NameFor(runID contract.RunID, seq int) (string, error) {
	if err := platform.ValidateRunID(runID); err != nil {
		return "", fmt.Errorf("fake name: %w", err)
	}
	return "fake-" + string(runID) + "-" + strconv.Itoa(seq), nil
}

// Run starts a supervisor identity, hands it to onSupervisor and runs the
// fake tool only if that succeeds, as the launch gate does.
func (c *Containment) Run(ctx context.Context, spec platform.LaunchSpec, onSupervisor func(platform.LaunchAck) error) (contract.CommandResult, error) {
	if len(spec.Argv) == 0 {
		return contract.CommandResult{}, platform.ErrCommandEmpty
	}
	c.mu.Lock()
	c.launched = append(c.launched, spec)
	c.busy[spec.Name] = true
	run := c.run
	c.mu.Unlock()
	tool := c.table.Spawn()
	if onSupervisor != nil {
		if err := onSupervisor(platform.LaunchAck{Name: spec.Name, Supervisor: tool}); err != nil {
			c.table.Kill(tool)
			c.SetBusy(spec.Name, false)
			return contract.CommandResult{}, fmt.Errorf("%w: %w", platform.ErrGateClosed, err)
		}
	}
	res := contract.CommandResult{}
	var err error
	if run != nil {
		res, err = run(ctx, spec)
	}
	c.table.Kill(tool)
	c.SetBusy(spec.Name, false)
	return res, err
}

// Empty empties name. A package-manager transaction is waited for, for Grace
// plus KillWait, and never signalled.
func (c *Containment) Empty(ctx context.Context, name string, opts platform.EmptyOptions) error {
	c.mu.Lock()
	c.empties = append(c.empties, EmptyCall{Name: name, Opts: opts})
	hook := c.empty
	c.mu.Unlock()
	if hook != nil {
		hook(name, opts)
	}
	if opts.PackageTransaction {
		deadline := time.Now().Add(opts.Grace + opts.KillWait)
		for c.Busy(name) {
			if time.Now().After(deadline) {
				return fmt.Errorf("%w: package-manager transaction in %q", platform.ErrContainmentNotEmpty, name)
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("fake empty: %w", ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.busy[name] {
		return nil
	}
	c.killed[name] = true
	if c.unkillable[name] {
		return fmt.Errorf("%w: %q", platform.ErrContainmentNotEmpty, name)
	}
	c.busy[name] = false
	return nil
}

// Runner is a scripted platform.CommandRunner. Respond answers each call;
// Calls lists every argv it was given.
type Runner struct {
	mu      sync.Mutex
	respond func(argv []string) (platform.ExecResult, error)
	calls   [][]string
	envs    [][]string
}

// NewRunner returns a runner that answers with respond.
func NewRunner(respond func(argv []string) (platform.ExecResult, error)) *Runner {
	return &Runner{respond: respond}
}

// Run records spec.Argv, opens a gate or calls OnStart with PID 4242,
// and answers.
func (r *Runner) Run(_ context.Context, spec platform.ExecSpec) (platform.ExecResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), spec.Argv...))
	r.envs = append(r.envs, append([]string(nil), spec.Env...))
	respond := r.respond
	r.mu.Unlock()
	if spec.Gate != nil {
		if err := spec.Gate(4242); err != nil {
			return platform.ExecResult{ExitCode: 125}, fmt.Errorf("%w: %w", platform.ErrGateClosed, err)
		}
	}
	if spec.OnStart != nil {
		spec.OnStart(4242)
	}
	return respond(spec.Argv)
}

// Envs returns the environment of every call so far, in call order.
func (r *Runner) Envs() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.envs...)
}

// Calls returns every argv run so far.
func (r *Runner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}
