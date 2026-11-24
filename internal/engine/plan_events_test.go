package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine/events"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

// droppedXattrMetaOps is a platform-neutral metadataOps fake that simulates
// OS-protected extended attributes (such as macOS com.apple.macl) being
// detected on Read so Plan, Backup, and Apply sequence/digest tests run on
// both Linux and macOS CI.
type droppedXattrMetaOps struct {
	dropped []string
}

func (m droppedXattrMetaOps) Read(f *os.File) (FileMetadata, error) {
	meta, err := osMetadataOps{}.Read(f)
	if err != nil {
		return FileMetadata{}, err
	}
	if len(m.dropped) > 0 {
		meta.DroppedXattrs = append([]string(nil), m.dropped...)
	}
	return meta, nil
}

func (m droppedXattrMetaOps) Apply(f *os.File, meta FileMetadata) error {
	return osMetadataOps{}.Apply(f, meta)
}

type planDropTestUnit struct {
	path   string
	planID contract.PlanID
}

func (u planDropTestUnit) ID() contract.UnitID { return contract.UnitShell }

func (u planDropTestUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:       contract.UnitShell,
		Scope:      contract.ScopeUser,
		OwnedPaths: []string{u.path},
	}, nil
}

func (u planDropTestUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return contract.Plan{
		ID:     u.planID,
		Unit:   contract.UnitShell,
		Digest: "stub-digest",
		Changes: []contract.Change{
			{Target: u.path, Action: contract.ActionReplace, Risk: contract.RiskLow},
		},
	}, nil
}

func (u planDropTestUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return tx.WriteFile(ctx, u.path, []byte("updated\n"), 0o600, contract.Owner{UID: os.Getuid(), GID: os.Getgid()})
}

func (u planDropTestUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitShell, Outcome: contract.OutcomeVerified}, nil
}

func (u planDropTestUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }

func (u planDropTestUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitShell}, nil
}

func compileEventSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()

	entries, err := fs.ReadDir(wskit.SchemaFS(), "schema/json")
	if err != nil {
		t.Fatalf("read embedded schema/json: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		schemaPath := path.Join("schema/json", entry.Name())
		raw, err := fs.ReadFile(wskit.SchemaFS(), schemaPath)
		if err != nil {
			t.Fatalf("read embedded schema %s: %v", schemaPath, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("unmarshal schema %s: %v", schemaPath, err)
		}
		url := "https://github.com/sathyassn/agent-workstation-kit/" + schemaPath
		if err := compiler.AddResource(url, doc); err != nil {
			t.Fatalf("add resource %s: %v", url, err)
		}
	}
	const eventURL = "https://github.com/sathyassn/agent-workstation-kit/schema/json/event.schema.json"
	schema, err := compiler.Compile(eventURL)
	if err != nil {
		t.Fatalf("compile event schema: %v", err)
	}
	return schema
}

func validateEventLogAgainstSchema(t *testing.T, logPath string) int {
	t.Helper()
	schema := compileEventSchema(t)
	raw, err := os.ReadFile(logPath) //nolint:gosec // test temp event log file
	if err != nil {
		t.Fatalf("read event log %q: %v", logPath, err)
	}
	count := 0
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		count++
		val, err := jsonschema.UnmarshalJSON(bytes.NewReader(line))
		if err != nil {
			t.Fatalf("line %d: unmarshal JSON: %v\n%s", count, err, line)
		}
		if err := schema.Validate(val); err != nil {
			t.Fatalf("line %d: event.schema.json validation failed: %v\n%s", count, err, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan event log: %v", err)
	}
	return count
}

func newRealEventSink(t *testing.T, dir string, uiBuf int) (*events.Sink, chan contract.Event, string) {
	t.Helper()
	uiCh := make(chan contract.Event, uiBuf)
	logPath := filepath.Join(dir, "events.jsonl")
	logFile, err := os.Create(logPath) //nolint:gosec // test temp log file
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	sink, err := events.New(events.Options{
		Log:     logFile,
		OpenLog: func() (io.ReadCloser, error) { return os.Open(logPath) }, //nolint:gosec // test temp log file
		UI:      uiCh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sink, uiCh, logPath
}

// TestEnginePlanDigestCoversCannotUndo is F2: Engine.Plan must recompute the
// canonical RFC 8785 SHA-256 plan digest after mutating Plan.CannotUndo so one
// approval digest never matches both the plain plan and a plan with drop warnings.
func TestEnginePlanDigestCoversCannotUndo(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-plan-digest.conf"
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := platformtest.NewProcessTable("boot-plan-digest")
	newEng := func(name string, ops metadataOps) *Engine {
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal-"+name),
			HostLock:    filepath.Join(dir, "host-"+name+".lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			metaOps:     ops,
		})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	unit := planDropTestUnit{path: target, planID: "plan-digest-1"}
	planWithout, err := newEng("plain", droppedXattrMetaOps{}).Plan(context.Background(), unit, contract.Inputs{})
	if err != nil {
		t.Fatalf("Plan without drop: %v", err)
	}
	planWith, err := newEng("macl", droppedXattrMetaOps{dropped: []string{"com.apple.macl"}}).Plan(context.Background(), unit, contract.Inputs{})
	if err != nil {
		t.Fatalf("Plan with drop: %v", err)
	}
	sha256Hex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !sha256Hex.MatchString(string(planWithout.Digest)) {
		t.Fatalf("planWithout.Digest = %q, want 64 lowercase hex SHA-256", planWithout.Digest)
	}
	if !sha256Hex.MatchString(string(planWith.Digest)) {
		t.Fatalf("planWith.Digest = %q, want 64 lowercase hex SHA-256", planWith.Digest)
	}
	if planWithout.Digest == planWith.Digest {
		t.Fatalf("Plan.Digest did not change when CannotUndo gained com.apple.macl warning: both %q (cannot_undo=%v)", planWith.Digest, planWith.CannotUndo)
	}
}

// TestEnginePlanEventsHaveRunIDAndMatchEventSchema is F3: plan-time warning
// events must carry a non-empty RunID and conform to schema/json/event.schema.json.
func TestEnginePlanEventsHaveRunIDAndMatchEventSchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		planID contract.PlanID
	}{
		{name: "with_plan_id", planID: "plan-schema-1"},
		{name: "empty_plan_id_fallback", planID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "root")
			target := "/etc/wskit-plan-schema.conf"
			if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			sink, uiCh, logPath := newRealEventSink(t, dir, 16)
			table := platformtest.NewProcessTable("boot-plan-schema")
			e, err := New(Options{
				JournalDir:  filepath.Join(dir, "journal"),
				HostLock:    filepath.Join(dir, "host.lock"),
				Root:        root,
				Scope:       contract.ScopeUser,
				Containment: platformtest.NewContainment(table),
				Inspector:   table.Inspector(table.Spawn()),
				Sink:        sink,
				metaOps:     droppedXattrMetaOps{dropped: []string{"com.apple.macl"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Plan(context.Background(), planDropTestUnit{path: target, planID: tc.planID}, contract.Inputs{}); err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if err := sink.Close(context.Background()); err != nil {
				t.Fatalf("sink.Close: %v", err)
			}
			close(uiCh)
			var got []contract.Event
			for ev := range uiCh {
				got = append(got, ev)
			}
			if len(got) == 0 {
				t.Fatal("expected at least one StepPlan EventWarning in UI channel")
			}
			for _, ev := range got {
				if ev.RunID == "" {
					t.Fatalf("plan event has empty RunID: %+v", ev)
				}
			}
			if n := validateEventLogAgainstSchema(t, logPath); n != len(got) {
				t.Fatalf("validated %d JSONL events, want %d", n, len(got))
			}
		})
	}
}

// outOfOrderProbeSink wraps a real events.Sink to deterministically expose any
// gap between sequence assignment and Sink.Deliver: when Seq 1 enters Deliver,
// it waits briefly for Seq 2 to preempt it. If Seq assignment and Deliver are
// not serialised under one mutex, Seq 2 overtakes Seq 1 and events.Sink drops
// Seq 1. When serialised, Seq 2 cannot enter until Seq 1 returns, so both are
// delivered in order with zero drops.
type outOfOrderProbeSink struct {
	inner contract.Sink
	seq2  chan struct{}
	once  sync.Once
}

func (s *outOfOrderProbeSink) Deliver(ev contract.Event) {
	switch ev.Seq {
	case 1:
		select {
		case <-s.seq2:
		case <-time.After(15 * time.Millisecond):
		}
	case 2:
		s.once.Do(func() { close(s.seq2) })
	}
	s.inner.Deliver(ev)
}

// TestEngineConcurrentEmitMonotonicSeqInRealSink is F4 + F5: concurrent
// emitters delivering into a real events.Sink must serialise sequence
// assignment with delivery so events.Sink never drops out-of-order events.
// Platform-neutral so it runs on both Linux and macOS CI.
func TestEngineConcurrentEmitMonotonicSeqInRealSink(t *testing.T) {
	t.Run("serialised_seq_and_deliver", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-concurrent-seq.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sink, uiCh, logPath := newRealEventSink(t, dir, 16)
		probe := &outOfOrderProbeSink{inner: sink, seq2: make(chan struct{})}
		table := platformtest.NewProcessTable("boot-concurrent-seq")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			Sink:        probe,
			metaOps:     droppedXattrMetaOps{dropped: []string{"com.apple.macl"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		unit := planDropTestUnit{path: target, planID: "plan-concurrent-1"}
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, perr := e.Plan(context.Background(), unit, contract.Inputs{})
				errs <- perr
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for perr := range errs {
			if perr != nil {
				t.Fatalf("concurrent Plan: %v", perr)
			}
		}
		if err := sink.Close(context.Background()); err != nil {
			t.Fatalf("sink.Close: %v", err)
		}
		close(uiCh)
		var got []contract.Event
		for ev := range uiCh {
			got = append(got, ev)
		}
		if len(got) != 2 {
			t.Fatalf("real events.Sink UI received %d events, want 2 (dropped out-of-order event): %+v", len(got), got)
		}
		if got[0].Seq != 1 || got[1].Seq != 2 {
			t.Fatalf("UI event seqs = [%d, %d], want [1, 2]", got[0].Seq, got[1].Seq)
		}
		if n := validateEventLogAgainstSchema(t, logPath); n != 2 {
			t.Fatalf("validated %d JSONL events, want 2", n)
		}
	})

	t.Run("concurrent_burst_under_race", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-burst-seq.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		const goroutines = 8
		const perG = 10
		const wantTotal = goroutines * perG
		sink, uiCh, logPath := newRealEventSink(t, dir, wantTotal+16)
		table := platformtest.NewProcessTable("boot-burst-seq")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			Sink:        sink,
			metaOps:     droppedXattrMetaOps{dropped: []string{"com.apple.macl"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		unit := planDropTestUnit{path: target, planID: "plan-burst-1"}
		start := make(chan struct{})
		var wg sync.WaitGroup
		errCh := make(chan error, wantTotal)
		for range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range perG {
					_, perr := e.Plan(context.Background(), unit, contract.Inputs{})
					if perr != nil {
						errCh <- perr
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errCh)
		for perr := range errCh {
			t.Fatalf("burst Plan: %v", perr)
		}
		if err := sink.Close(context.Background()); err != nil {
			t.Fatalf("sink.Close: %v", err)
		}
		close(uiCh)
		var prevSeq uint64
		count := 0
		for ev := range uiCh {
			count++
			if ev.Seq <= prevSeq {
				t.Fatalf("non-increasing Seq %d after %d", ev.Seq, prevSeq)
			}
			prevSeq = ev.Seq
		}
		if count != wantTotal {
			t.Fatalf("UI received %d events, want %d (dropped %d)", count, wantTotal, wantTotal-count)
		}
		if n := validateEventLogAgainstSchema(t, logPath); n != wantTotal {
			t.Fatalf("validated %d JSONL events, want %d", n, wantTotal)
		}
	})
}

// TestEnginePlanBackupApplySeqMonotonicInRealSink is F5: platform-neutral
// counterpart to the darwin metadata sequence test so Linux CI also exercises
// unified monotonic Seq across Plan, Backup, and Apply in a real events.Sink.
func TestEnginePlanBackupApplySeqMonotonicInRealSink(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	target := "/etc/wskit-neutral-seq.conf"
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink, uiCh, logPath := newRealEventSink(t, dir, 32)
	table := platformtest.NewProcessTable("boot-neutral-seq")
	e, err := New(Options{
		JournalDir:  filepath.Join(dir, "journal"),
		HostLock:    filepath.Join(dir, "host.lock"),
		Root:        root,
		Scope:       contract.ScopeUser,
		Containment: platformtest.NewContainment(table),
		Inspector:   table.Inspector(table.Spawn()),
		Sink:        sink,
		metaOps:     droppedXattrMetaOps{dropped: []string{"com.apple.macl"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unit := planDropTestUnit{path: target, planID: "plan-neutral-1"}
	plan, err := e.Plan(context.Background(), unit, contract.Inputs{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := e.Apply(context.Background(), unit, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("sink.Close: %v", err)
	}
	close(uiCh)
	var got []contract.Event
	var prevSeq uint64
	var sawPlanWarn, sawBackupWarn, sawChange, sawApplyWarn bool
	for ev := range uiCh {
		got = append(got, ev)
		if ev.Seq <= prevSeq {
			t.Fatalf("non-increasing Seq %d after %d: %+v", ev.Seq, prevSeq, got)
		}
		prevSeq = ev.Seq
		if ev.Kind == contract.EventWarning && ev.Step == contract.StepPlan {
			sawPlanWarn = true
		}
		if ev.Kind == contract.EventWarning && ev.Step == contract.StepBackup {
			sawBackupWarn = true
		}
		if ev.Kind == contract.EventChange && ev.Step == contract.StepApply {
			sawChange = true
		}
		if ev.Kind == contract.EventWarning && ev.Step == contract.StepApply {
			sawApplyWarn = true
		}
	}
	if !sawPlanWarn || !sawBackupWarn || !sawChange || !sawApplyWarn {
		t.Fatalf("real events.Sink UI dropped events (planWarn=%v backupWarn=%v change=%v applyWarn=%v): %+v", sawPlanWarn, sawBackupWarn, sawChange, sawApplyWarn, got)
	}
	if n := validateEventLogAgainstSchema(t, logPath); n != len(got) {
		t.Fatalf("validated %d JSONL events, want %d", n, len(got))
	}
}

// TestResumeMatchesEnginePlanDigest is G2: Engine.Plan and Engine.Resume must
// compute and compare plan digests on the same canonical basis so an
// interrupted Apply started from Engine.Plan resumes cleanly when neither the
// unit plan nor the target changed, while still refusing an actual plan change.
func TestResumeMatchesEnginePlanDigest(t *testing.T) {
	type crashSentinel struct{}
	runInterrupted := func(t *testing.T, e *Engine, u contract.Unit, p contract.Plan) {
		t.Helper()
		var panicked bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(crashSentinel); ok {
						panicked = true
						return
					}
					panic(r)
				}
			}()
			_ = e.Apply(context.Background(), u, p)
		}()
		if !panicked {
			t.Fatal("expected simulated crash during Apply")
		}
	}

	for _, tc := range []struct {
		name    string
		dropped []string
	}{
		{name: "plain_target_via_unit_plan"},
		{name: "dropped_xattr_target_via_unit_plan", dropped: []string{"com.apple.macl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "root")
			target := "/etc/wskit-resume-digest.conf"
			if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			table := platformtest.NewProcessTable("boot-resume-digest")
			cont := platformtest.NewContainment(table)
			journalDir := filepath.Join(dir, "journal")
			hostLock := filepath.Join(dir, "host.lock")
			ops := droppedXattrMetaOps{dropped: tc.dropped}

			firstID := table.Spawn()
			firstEng, err := New(Options{
				JournalDir:  journalDir,
				HostLock:    hostLock,
				Root:        root,
				Scope:       contract.ScopeUser,
				Containment: cont,
				Inspector:   table.Inspector(firstID),
				metaOps:     ops,
				Fault: func(pt Point) {
					if pt == PointMutation {
						panic(crashSentinel{})
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			unit := planDropTestUnit{path: target, planID: "plan-resume-1"}
			plan, err := firstEng.Plan(context.Background(), unit, contract.Inputs{})
			if err != nil {
				t.Fatalf("Engine.Plan: %v", err)
			}
			runInterrupted(t, firstEng, unit, plan)
			table.Kill(firstID)

			resumer, err := New(Options{
				JournalDir:  journalDir,
				HostLock:    hostLock,
				Root:        root,
				Scope:       contract.ScopeUser,
				Containment: cont,
				Inspector:   table.Inspector(table.Spawn()),
				metaOps:     ops,
			})
			if err != nil {
				t.Fatal(err)
			}
			resolve := func(ctx context.Context, _ contract.UnitID) (contract.Unit, contract.Plan, error) {
				p, perr := unit.Plan(ctx, contract.Inputs{})
				return unit, p, perr
			}
			res, err := resumer.Resume(context.Background(), resolve, ResumeOptions{})
			if err != nil || res.Action != ResumeReapplied {
				t.Fatalf("Resume = %+v, %v; want ResumeReapplied", res, err)
			}
			gotData, err := os.ReadFile(filepath.Join(root, target)) //nolint:gosec // test root path
			if err != nil {
				t.Fatal(err)
			}
			if string(gotData) != "updated\n" {
				t.Fatalf("target content after reapply = %q, want %q", gotData, "updated\n")
			}
		})
	}

	t.Run("changed_plan_still_refused", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-resume-changed.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, target), []byte("orig\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-resume-changed")
		cont := platformtest.NewContainment(table)
		journalDir := filepath.Join(dir, "journal")
		hostLock := filepath.Join(dir, "host.lock")

		firstID := table.Spawn()
		firstEng, err := New(Options{
			JournalDir:  journalDir,
			HostLock:    hostLock,
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: cont,
			Inspector:   table.Inspector(firstID),
			metaOps:     droppedXattrMetaOps{},
			Fault: func(pt Point) {
				if pt == PointMutation {
					panic(crashSentinel{})
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		unit := planDropTestUnit{path: target, planID: "plan-resume-1"}
		plan, err := firstEng.Plan(context.Background(), unit, contract.Inputs{})
		if err != nil {
			t.Fatalf("Engine.Plan: %v", err)
		}
		runInterrupted(t, firstEng, unit, plan)
		table.Kill(firstID)

		resumer, err := New(Options{
			JournalDir:  journalDir,
			HostLock:    hostLock,
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: cont,
			Inspector:   table.Inspector(table.Spawn()),
			metaOps:     droppedXattrMetaOps{},
		})
		if err != nil {
			t.Fatal(err)
		}
		changedUnit := planDropTestUnit{path: target, planID: "plan-resume-2"}
		resolve := func(ctx context.Context, _ contract.UnitID) (contract.Unit, contract.Plan, error) {
			p, perr := changedUnit.Plan(ctx, contract.Inputs{})
			return changedUnit, p, perr
		}
		if _, err := resumer.Resume(context.Background(), resolve, ResumeOptions{}); !errors.Is(err, ErrPlanChanged) {
			t.Fatalf("Resume with changed plan err = %v, want ErrPlanChanged", err)
		}
		gotData, err := os.ReadFile(filepath.Join(root, target)) //nolint:gosec // test root path
		if err != nil {
			t.Fatal(err)
		}
		if string(gotData) != "orig\n" {
			t.Fatalf("target content after refused reapply = %q, want %q", gotData, "orig\n")
		}
	})
}

// blockingNonProgressSink blocks the first non-progress Deliver until release
// is closed while letting progress Deliver calls return immediately, and
// records all delivered events in arrival order.
type blockingNonProgressSink struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	got     []contract.Event
}

func (s *blockingNonProgressSink) Deliver(ev contract.Event) {
	if ev.Kind != contract.EventProgress && ev.Kind != contract.EventCoalesced {
		block := false
		s.once.Do(func() {
			block = true
			close(s.entered)
		})
		if block {
			<-s.release
		}
	}
	s.mu.Lock()
	s.got = append(s.got, ev)
	s.mu.Unlock()
}

// blockingLogWriter wraps an io.Writer and blocks the first Write call until
// release is closed.
type blockingLogWriter struct {
	inner   io.Writer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingLogWriter) Write(p []byte) (int, error) {
	block := false
	w.once.Do(func() {
		block = true
		close(w.entered)
	})
	if block {
		<-w.release
	}
	return w.inner.Write(p)
}

type progressEmitUnit struct {
	path  string
	apply func(ctx context.Context, tx contract.Tx) error
}

func (u progressEmitUnit) ID() contract.UnitID { return contract.UnitShell }

func (u progressEmitUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:       contract.UnitShell,
		Scope:      contract.ScopeUser,
		OwnedPaths: []string{u.path},
	}, nil
}

func (u progressEmitUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return contract.Plan{ID: "plan-progress-1", Unit: contract.UnitShell, Digest: "d"}, nil
}

func (u progressEmitUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return u.apply(ctx, tx)
}

func (u progressEmitUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitShell, Outcome: contract.OutcomeVerified}, nil
}

func (u progressEmitUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }

func (u progressEmitUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitShell}, nil
}

// TestDeliverProgressNotBlockedBySlowNonProgressWrite is G3 (SPC-001:366):
// a blocked non-progress Deliver / run-log write must never stall concurrent
// EventProgress delivery, while non-progress order stays strictly monotonic
// under -race.
func TestDeliverProgressNotBlockedBySlowNonProgressWrite(t *testing.T) {
	t.Run("custom_blocking_sink_via_tx_emit", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-progress-custom.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		sink := &blockingNonProgressSink{
			entered: make(chan struct{}),
			release: make(chan struct{}),
		}
		table := platformtest.NewProcessTable("boot-progress-custom")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			Sink:        sink,
		})
		if err != nil {
			t.Fatal(err)
		}

		unit := progressEmitUnit{
			path: target,
			apply: func(_ context.Context, tx contract.Tx) error {
				var wg sync.WaitGroup
				// Start a slow non-progress emit that blocks in Deliver.
				wg.Add(1)
				go func() {
					defer wg.Done()
					tx.Emit(contract.Event{Kind: contract.EventWarning, Text: "slow non-progress 1"})
				}()
				<-sink.entered

				// Start concurrent non-progress emitters that must queue monotonically.
				for range 4 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						tx.Emit(contract.Event{Kind: contract.EventWarning, Text: "queued non-progress"})
					}()
				}

				// Emit EventProgress concurrently: must NOT block behind the slow non-progress Deliver.
				progDone := make(chan struct{})
				go func() {
					defer close(progDone)
					tx.Emit(contract.Event{Kind: contract.EventProgress, Text: "tick 1"})
				}()
				select {
				case <-progDone:
				case <-time.After(500 * time.Millisecond):
					close(sink.release)
					wg.Wait()
					return errors.New("EventProgress stalled behind blocked non-progress Deliver")
				}
				close(sink.release)
				wg.Wait()
				return nil
			},
		}
		if err := e.Apply(context.Background(), unit, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"}); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		sink.mu.Lock()
		got := append([]contract.Event(nil), sink.got...)
		sink.mu.Unlock()
		var lastNonProgSeq uint64
		nonProgCount := 0
		progCount := 0
		for _, ev := range got {
			if ev.Kind == contract.EventProgress {
				progCount++
				continue
			}
			nonProgCount++
			if ev.Seq <= lastNonProgSeq {
				t.Fatalf("non-progress events out of monotonic order: seq %d after %d (%+v)", ev.Seq, lastNonProgSeq, got)
			}
			lastNonProgSeq = ev.Seq
		}
		if progCount != 1 || nonProgCount != 5 {
			t.Fatalf("got %d progress and %d non-progress events, want 1 and 5", progCount, nonProgCount)
		}
	})

	t.Run("real_events_sink_slow_log_write", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "root")
		target := "/etc/wskit-progress-real.conf"
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
			t.Fatal(err)
		}
		uiCh := make(chan contract.Event, 32)
		logPath := filepath.Join(dir, "events.jsonl")
		logFile, err := os.Create(logPath) //nolint:gosec // test temp log file
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = logFile.Close() })
		slowLog := &blockingLogWriter{
			inner:   logFile,
			entered: make(chan struct{}),
			release: make(chan struct{}),
		}
		sink, err := events.New(events.Options{
			Log:     slowLog,
			OpenLog: func() (io.ReadCloser, error) { return os.Open(logPath) }, //nolint:gosec // test temp log file
			UI:      uiCh,
		})
		if err != nil {
			t.Fatal(err)
		}
		table := platformtest.NewProcessTable("boot-progress-real")
		e, err := New(Options{
			JournalDir:  filepath.Join(dir, "journal"),
			HostLock:    filepath.Join(dir, "host.lock"),
			Root:        root,
			Scope:       contract.ScopeUser,
			Containment: platformtest.NewContainment(table),
			Inspector:   table.Inspector(table.Spawn()),
			Sink:        sink,
		})
		if err != nil {
			t.Fatal(err)
		}

		unit := progressEmitUnit{
			path: target,
			apply: func(_ context.Context, tx contract.Tx) error {
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					tx.Emit(contract.Event{Kind: contract.EventWarning, Text: "slow log write 1"})
				}()
				<-slowLog.entered

				for range 3 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						tx.Emit(contract.Event{Kind: contract.EventWarning, Text: "follow-up non-progress"})
					}()
				}

				progDone := make(chan struct{})
				go func() {
					defer close(progDone)
					tx.Emit(contract.Event{Kind: contract.EventProgress, Text: "progress during blocked write"})
				}()
				select {
				case <-progDone:
				case <-time.After(500 * time.Millisecond):
					close(slowLog.release)
					wg.Wait()
					return errors.New("EventProgress stalled behind blocked events.Sink log write")
				}
				close(slowLog.release)
				wg.Wait()
				return nil
			},
		}
		if err := e.Apply(context.Background(), unit, contract.Plan{ID: "p", Unit: contract.UnitShell, Digest: "d"}); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if err := sink.Close(context.Background()); err != nil {
			t.Fatalf("sink.Close: %v", err)
		}
		close(uiCh)

		var prevSeq uint64
		count := 0
		sawProgress := false
		for ev := range uiCh {
			count++
			if ev.Seq <= prevSeq {
				t.Fatalf("UI events out of monotonic order: seq %d after %d", ev.Seq, prevSeq)
			}
			prevSeq = ev.Seq
			if ev.Kind == contract.EventProgress {
				sawProgress = true
			}
		}
		if !sawProgress || count != 5 {
			t.Fatalf("UI got %d events (sawProgress=%v), want 5 with progress", count, sawProgress)
		}
		if n := validateEventLogAgainstSchema(t, logPath); n != 5 {
			t.Fatalf("validated %d JSONL events, want 5", n)
		}
	})
}
