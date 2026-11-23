package platformtest

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sathyassn/agent-workstation-kit/internal/platform"
)

// Systemd is a fake of systemd-run --scope and systemctl, shaped like the
// real tools, for driving the Linux adapter through the engine:
//
//   - systemd-run starts a supervisor process, holds it at the launch gate,
//     then registers the scope and runs the tool in it with the supervisor's
//     identity, as systemd-run --scope execs the tool in place;
//   - systemctl show answers LoadState and ActiveState: a scope that never
//     registered, or was cleared, is "not-found"/"inactive"; a scope whose
//     processes all ended is unloaded on success and stays loaded as
//     "failed" otherwise, since the adapter does not pass --collect;
//   - systemctl kill signals every process in the scope; reset-failed
//     clears a failed scope.
//
// Registration can be held back (HoldRegistration), as when the engine dies
// between starting systemd-run and the scope appearing: the supervisor then
// stays alive and registers only on CompleteRegistrations, if still alive.
type Systemd struct {
	mu    sync.Mutex
	table *ProcessTable
	tool  func(ctx context.Context, s *Scope) int
	hold  func(name string)
	late  func()
	units map[string]*unit
	held  map[string]heldLaunch
}

type unit struct {
	members map[platform.ProcessIdentity]func()
	failed  bool
}

type heldLaunch struct {
	sup  platform.ProcessIdentity
	late func()
}

// NewSystemd returns a fake whose processes live in table.
func NewSystemd(table *ProcessTable) *Systemd {
	return &Systemd{table: table, units: map[string]*unit{}, held: map[string]heldLaunch{}}
}

// SetTool installs what every launched tool does. Its return is the exit
// code.
func (f *Systemd) SetTool(fn func(ctx context.Context, s *Scope) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tool = fn
}

// HoldRegistration calls fn between the gate opening and the scope
// registering. If fn panics, as a simulated engine crash does, the
// supervisor stays alive and the launch stays held; late then runs on
// CompleteRegistrations if the supervisor is still alive.
func (f *Systemd) HoldRegistration(fn func(name string), late func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold, f.late = fn, late
}

// CompleteRegistrations lets every held supervisor that is still alive
// register its scope and run its late work. It reports how many ran.
func (f *Systemd) CompleteRegistrations() int {
	f.mu.Lock()
	held := f.held
	f.held = map[string]heldLaunch{}
	f.mu.Unlock()
	ran := 0
	for _, h := range held {
		if alive, _ := f.table.Alive(context.Background(), h.sup); alive {
			ran++
			if h.late != nil {
				h.late()
			}
		}
	}
	return ran
}

// RunDelayed runs the delayed work of every process still alive in any
// scope, as descendants that outlived their tool would. It reports how many
// ran.
func (f *Systemd) RunDelayed() int {
	f.mu.Lock()
	var work []func()
	for _, u := range f.units {
		for id, fn := range u.members {
			if alive, _ := f.table.Alive(context.Background(), id); alive && fn != nil {
				work = append(work, fn)
			}
		}
	}
	f.mu.Unlock()
	for _, fn := range work {
		fn()
	}
	return len(work)
}

// Scope is a running scope, handed to the fake tool.
type Scope struct {
	f    *Systemd
	name string
}

// Spawn adds a descendant to the scope that keeps running after the tool
// ends. later is its delayed work; unkillable makes it ignore signals.
func (s *Scope) Spawn(later func(), unkillable bool) platform.ProcessIdentity {
	id := s.f.table.Spawn()
	if unkillable {
		s.f.table.SetUnkillable(id)
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.units[s.name].members[id] = later
	return id
}

// Run answers systemd-run and systemctl command lines.
func (f *Systemd) Run(ctx context.Context, spec platform.ExecSpec) (platform.ExecResult, error) {
	switch filepath.Base(spec.Argv[0]) {
	case "systemd-run":
		return f.systemdRun(ctx, spec)
	case "systemctl":
		return f.systemctl(spec.Argv[1:])
	}
	return platform.ExecResult{ExitCode: 127}, nil
}

func flagValue(argv []string, prefix string) string {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v
		}
	}
	return ""
}

func (f *Systemd) systemdRun(ctx context.Context, spec platform.ExecSpec) (platform.ExecResult, error) {
	name := flagValue(spec.Argv, "--unit=")
	sup := f.table.Spawn()
	if spec.Gate != nil {
		if err := spec.Gate(sup.PID); err != nil {
			f.table.Kill(sup)
			return platform.ExecResult{ExitCode: 125}, fmt.Errorf("%w: %w", platform.ErrGateClosed, err)
		}
	}
	f.mu.Lock()
	hold, late := f.hold, f.late
	f.hold, f.late = nil, nil
	f.held[name] = heldLaunch{sup: sup, late: late}
	f.mu.Unlock()
	if hold != nil {
		hold(name) // may panic: the launch then stays held
	}
	f.mu.Lock()
	delete(f.held, name)
	f.units[name] = &unit{members: map[platform.ProcessIdentity]func(){sup: nil}}
	tool := f.tool
	f.mu.Unlock()

	code := 0
	if tool != nil {
		code = tool(ctx, &Scope{f: f, name: name})
	}
	f.table.Kill(sup)
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.units[name]
	delete(u.members, sup)
	f.settleLocked(name, code != 0)
	return platform.ExecResult{ExitCode: code}, nil
}

// settleLocked unloads a scope with no live process, or marks it failed.
func (f *Systemd) settleLocked(name string, failed bool) {
	u := f.units[name]
	for id := range u.members {
		if f.table.isAlive(id) {
			return
		}
	}
	if failed {
		u.failed = true
		return
	}
	delete(f.units, name)
}

func (f *Systemd) systemctl(args []string) (platform.ExecResult, error) {
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--user" })
	name := args[len(args)-1]
	f.mu.Lock()
	defer f.mu.Unlock()
	u, loaded := f.units[name]
	switch args[0] {
	case "show":
		if !loaded {
			return platform.ExecResult{Stdout: "LoadState=not-found\nActiveState=inactive\n"}, nil
		}
		for id := range u.members {
			if f.table.isAlive(id) {
				return platform.ExecResult{Stdout: "LoadState=loaded\nActiveState=active\n"}, nil
			}
		}
		return platform.ExecResult{Stdout: "LoadState=loaded\nActiveState=failed\n"}, nil
	case "kill":
		if !loaded {
			return platform.ExecResult{ExitCode: 1}, nil
		}
		for id := range u.members {
			_ = f.table.Signal(context.Background(), id, 0)
		}
		f.settleLocked(name, true)
		return platform.ExecResult{}, nil
	case "reset-failed":
		if loaded && u.failed {
			delete(f.units, name)
			return platform.ExecResult{}, nil
		}
		return platform.ExecResult{ExitCode: 1}, nil
	}
	return platform.ExecResult{ExitCode: 1}, nil
}

// Loaded reports whether systemd still knows the scope.
func (f *Systemd) Loaded(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.units[name]
	return ok
}
