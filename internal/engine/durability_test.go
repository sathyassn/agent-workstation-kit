package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// powerLossModel is a filesystem fault model: a new or replaced folder
// entry survives power loss only once its folder has been fsynced after it.
type powerLossModel struct {
	mu      sync.Mutex
	pending map[string]bool
	durable map[string]bool
	// atIntent is the durable set when the intent record became durable.
	atIntent map[string]bool
	record   string
	sawRec   bool
}

func (m *powerLossModel) trace(op, dir, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch op {
	case traceEntry:
		p := filepath.Join(dir, name)
		m.pending[p] = true
		delete(m.durable, p)
		if p == m.record {
			m.sawRec = true
		}
	case traceSync:
		for p := range m.pending {
			if filepath.Dir(p) == dir {
				delete(m.pending, p)
				m.durable[p] = true
			}
		}
		if m.sawRec && m.atIntent == nil && m.durable[m.record] {
			m.atIntent = map[string]bool{}
			for p := range m.durable {
				m.atIntent[p] = true
			}
		}
	}
}

type oneFileUnit struct{ path string }

func (u oneFileUnit) ID() contract.UnitID { return contract.UnitShell }
func (u oneFileUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{Unit: contract.UnitShell, Scope: contract.ScopeUser, OwnedPaths: []string{u.path}}, nil
}
func (u oneFileUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"}, nil
}
func (u oneFileUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return tx.WriteFile(ctx, u.path, []byte("new\n"), 0o600, contract.Owner{UID: os.Getuid(), GID: os.Getgid()})
}
func (u oneFileUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitShell, Outcome: contract.OutcomeVerified}, nil
}
func (u oneFileUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u oneFileUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitShell}, nil
}

// TestBackupIsDurableBeforeIntent is finding 6: under the power-loss model,
// when the intent record becomes durable, the journal folders, the run's
// backup folder, every backup file and the manifest are durable too. A
// power cut can then never keep the intent and the changes but lose the
// backup.
func TestBackupIsDurableBeforeIntent(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "x.conf"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot")
	journal := filepath.Join(dir, "journal")
	e, err := New(Options{
		JournalDir: journal, HostLock: filepath.Join(dir, "host.lock"), Root: root, Scope: contract.ScopeUser,
		Containment: platformtest.NewContainment(table), Inspector: table.Inspector(table.Spawn()),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &powerLossModel{pending: map[string]bool{}, durable: map[string]bool{}, record: filepath.Join(journal, recordFile)}
	e.traceFn = m.trace
	if err := e.Apply(context.Background(), oneFileUnit{path: "/etc/x.conf"}, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"}); err != nil {
		t.Fatal(err)
	}
	if m.atIntent == nil {
		t.Fatal("the intent record never became durable")
	}
	rec, err := e.readRecord()
	if err != nil {
		t.Fatal(err)
	}
	run := e.backupDir(rec.RunID)
	for _, p := range []string{
		filepath.Join(journal, backupsDir),
		filepath.Join(journal, receiptsDir),
		run,
		filepath.Join(run, "0"),
		filepath.Join(run, manifestFile),
	} {
		if !m.atIntent[p] {
			var have []string
			for q := range m.atIntent {
				have = append(have, strings.TrimPrefix(q, dir))
			}
			t.Errorf("%s not durable when the intent was (durable: %v)", strings.TrimPrefix(p, dir), have)
		}
	}
}
