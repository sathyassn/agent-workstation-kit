package events_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	wskit "github.com/sathyassn/agent-workstation-kit"
	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/engine/events"
)

const (
	waitShort = 2 * time.Second
	waitLong  = 10 * time.Second
)

func event(seq uint64, kind contract.EventKind) contract.Event {
	return contract.Event{
		Seq:   seq,
		Time:  time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC),
		RunID: "run-events-test",
		Unit:  contract.UnitShell,
		Step:  contract.StepApply,
		Kind:  kind,
		Text:  "event " + strconv.FormatUint(seq, 10),
	}
}

// fileLog returns a run log in a temporary folder and its reopen function.
func fileLog(t *testing.T) (*os.File, func() (io.ReadCloser, error), string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("create run log: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	open := func() (io.ReadCloser, error) { return os.Open(path) } //nolint:gosec // test path
	return f, open, path
}

func newSink(t *testing.T, opts events.Options) *events.Sink {
	t.Helper()
	s, err := events.New(opts)
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	t.Cleanup(func() {
		// Close in the background, so a sink stuck in Deliver fails the
		// test instead of hanging it.
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.Close()
		}()
		select {
		case <-done:
		case <-time.After(waitShort):
			t.Error("Close did not return")
		}
	})
	return s
}

// within fails the test when fn does not return within waitShort.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(waitShort):
		t.Fatalf("%s did not return within %v", what, waitShort)
	}
}

func readLog(t *testing.T, path string) []contract.Event {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	schema := compileEventSchema(t)
	var out []contract.Event
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(sc.Bytes()))
		if err != nil {
			t.Fatalf("run log line is not JSON: %v", err)
		}
		if err := schema.Validate(inst); err != nil {
			t.Fatalf("run log line %s fails event.schema.json: %v", sc.Bytes(), err)
		}
		var e contract.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("decode run log line: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func compileEventSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := fs.ReadFile(wskit.SchemaFS(), "schema/json/event.schema.json")
	if err != nil {
		t.Fatalf("read event schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse event schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	const url = "https://example.invalid/event.schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatalf("add event schema: %v", err)
	}
	s, err := c.Compile(url)
	if err != nil {
		t.Fatalf("compile event schema: %v", err)
	}
	return s
}

func count(t *testing.T, e contract.Event) uint64 {
	t.Helper()
	if e.Kind != contract.EventCoalesced {
		return 1
	}
	n, err := strconv.ParseUint(e.Data[contract.DataCount], 10, 64)
	if err != nil || n < 2 {
		t.Fatalf("coalesced event %d has count %q, want an integer of at least 2", e.Seq, e.Data[contract.DataCount])
	}
	return n
}

// sameEvent compares events, treating nil and empty Data as equal: the run
// log always writes Data as an object.
func sameEvent(a, b contract.Event) bool {
	if len(a.Data) == 0 && len(b.Data) == 0 {
		a.Data, b.Data = nil, nil
	}
	return reflect.DeepEqual(a, b)
}

func TestNewRejectsMissingOptions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts events.Options
	}{
		{name: "no log", opts: events.Options{}},
		{name: "UI without OpenLog", opts: events.Options{Log: io.Discard, UI: make(chan contract.Event, 1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := events.New(tc.opts); !errors.Is(err, events.ErrOptions) {
				t.Fatalf("events.New error = %v, want ErrOptions", err)
			}
		})
	}
}

// memLog is an in-memory run log. The contract test times Deliver, and a
// file on a loaded disk could exceed its budget on the log write SPC-001
// allows, which is not what the test measures.
type memLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *memLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *memLog) open() (io.ReadCloser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return io.NopCloser(bytes.NewReader(bytes.Clone(l.buf.Bytes()))), nil
}

func TestSinkSatisfiesContract(t *testing.T) {
	t.Parallel()
	runLog := &memLog{}
	ui := make(chan contract.Event, 4) // never read: the contract test stalls the UI
	s := newSink(t, events.Options{Log: runLog, OpenLog: runLog.open, UI: ui})
	contracttest.TestSink(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestDeliverNeverBlocksOnStalledUI delivers a burst to a UI nobody reads,
// then starts reading and expects every non-progress event once, in order,
// replayed from the run log.
func TestDeliverNeverBlocksOnStalledUI(t *testing.T) {
	t.Parallel()
	const total = 2000
	log, open, path := fileLog(t)
	ui := make(chan contract.Event, 2)
	s := newSink(t, events.Options{Log: log, OpenLog: open, UI: ui, PendingProgress: 8})

	var want []uint64 // non-progress sequence numbers
	var wantProgress uint64
	within(t, "Deliver to a stalled UI", func() {
		for seq := uint64(1); seq <= total; seq++ {
			kind := contract.EventProgress
			if seq%5 != 0 {
				wantProgress++
			} else {
				kind = contract.EventOutput
				want = append(want, seq)
			}
			s.Deliver(event(seq, kind))
		}
	})

	var got []uint64
	deadline := time.After(waitLong)
	for len(got) < len(want) {
		select {
		case e := <-ui:
			switch e.Kind {
			case contract.EventProgress, contract.EventCoalesced:
				count(t, e)
			default:
				if !sameEvent(e, event(e.Seq, contract.EventOutput)) {
					t.Fatalf("non-progress event %d changed: %+v", e.Seq, e)
				}
				got = append(got, e.Seq)
			}
		case <-deadline:
			t.Fatalf("UI got %d of %d non-progress events", len(got), len(want))
		}
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UI non-progress event %d = seq %d, want %d", i, got[i], want[i])
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The run log holds every event: non-progress ones as written, progress
	// ones alone or coalesced, accounting for all of them.
	var progress uint64
	var logged []uint64
	var prev uint64
	for _, e := range readLog(t, path) {
		if e.Seq <= prev {
			t.Fatalf("run log out of order: seq %d after %d", e.Seq, prev)
		}
		prev = e.Seq
		switch e.Kind {
		case contract.EventProgress, contract.EventCoalesced:
			progress += count(t, e)
		default:
			logged = append(logged, e.Seq)
		}
	}
	if len(logged) != len(want) {
		t.Fatalf("run log has %d non-progress events, want %d", len(logged), len(want))
	}
	if progress != wantProgress {
		t.Fatalf("run log accounts for %d progress events, want %d", progress, wantProgress)
	}
}

// TestFullUICoalescesOnlyProgress fills the UI queue, then checks that the
// progress events became one coalesced event and the non-progress event that
// followed arrived unchanged after it.
func TestFullUICoalescesOnlyProgress(t *testing.T) {
	t.Parallel()
	log, open, _ := fileLog(t)
	ui := make(chan contract.Event, 1)
	s := newSink(t, events.Options{Log: log, OpenLog: open, UI: ui})

	for seq := uint64(1); seq <= 10; seq++ {
		s.Deliver(event(seq, contract.EventProgress))
	}
	if first := <-ui; first.Seq != 1 || first.Kind != contract.EventProgress {
		t.Fatalf("first UI event = %+v, want progress 1", first)
	}
	s.Deliver(event(11, contract.EventWarning))

	recv := func() contract.Event {
		select {
		case e := <-ui:
			return e
		case <-time.After(waitShort):
			t.Fatal("UI got no event")
			return contract.Event{}
		}
	}
	co := recv()
	if co.Kind != contract.EventCoalesced || co.Seq != 10 || count(t, co) != 9 {
		t.Fatalf("second UI event = %+v, want progress 2-10 coalesced into seq 10 with count 9", co)
	}
	warn := recv()
	if !sameEvent(warn, event(11, contract.EventWarning)) {
		t.Fatalf("third UI event = %+v, want the warning unchanged", warn)
	}
}

// blockingWriter blocks every write until release is closed.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

func TestDeliverBlocksOnlyForNonProgressLogWrite(t *testing.T) {
	t.Parallel()
	w := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	s, err := events.New(events.Options{Log: w})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		s.Deliver(event(1, contract.EventChange))
	}()
	<-w.started

	within(t, "progress Deliver during a run-log write", func() {
		for seq := uint64(2); seq <= 200; seq++ {
			s.Deliver(event(seq, contract.EventProgress))
		}
	})
	select {
	case <-returned:
		t.Fatal("non-progress Deliver returned before its run-log write finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(w.release)
	within(t, "non-progress Deliver after the write", func() { <-returned })
	within(t, "Close", func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

// failingWriter fails every write after the first ok writes.
type failingWriter struct {
	mu     sync.Mutex
	ok     int
	writes int
	err    error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.writes > w.ok {
		return 0, w.err
	}
	return len(p), nil
}

func (w *failingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func TestFullDiskFailsClosed(t *testing.T) {
	t.Parallel()
	enospc := &fs.PathError{Op: "write", Path: "run.jsonl", Err: syscall.ENOSPC}
	w := &failingWriter{ok: 1, err: enospc}
	s, err := events.New(events.Options{Log: w})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}

	s.Deliver(event(1, contract.EventStepStart))
	if err := s.Err(); err != nil {
		t.Fatalf("Err after a good write = %v, want nil", err)
	}
	within(t, "Deliver on a full disk", func() {
		s.Deliver(event(2, contract.EventProgress))
		s.Deliver(event(3, contract.EventChange))
		s.Deliver(event(4, contract.EventStepEnd))
	})

	for name, err := range map[string]error{"Err": s.Err(), "Close": s.Close()} {
		if !errors.Is(err, events.ErrRunLog) || !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("%s = %v, want ErrRunLog wrapping ENOSPC", name, err)
		}
	}
	if got := w.count(); got != 2 {
		t.Fatalf("run log got %d writes, want 2: none after the first failure", got)
	}
}
