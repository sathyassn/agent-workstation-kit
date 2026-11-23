package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

const (
	// DefaultPendingProgress bounds the progress events held for the run
	// log between two non-progress writes.
	DefaultPendingProgress = 64
	// eventSchemaVersion is the schema_version of event.schema.json.
	eventSchemaVersion = 1
)

// Sentinel errors. They are never reassigned.
var (
	// ErrRunLog wraps the first error writing the run log; the step that
	// sees it through Err fails closed.
	ErrRunLog = errors.New("write run log")
	// ErrOptions reports a Sink built without a required option.
	ErrOptions = errors.New("invalid event sink options")
)

// Options configures a Sink.
type Options struct {
	// Log receives the run log, one event.schema.json line per event.
	// Required.
	Log io.Writer
	// OpenLog opens the run log from its start. The UI's missed events are
	// replayed from it, and a replay keeps reading it as the log grows, as an
	// *os.File does; a reader that ends early only delays the replay to the
	// next non-progress event. Required when UI is set.
	OpenLog func() (io.ReadCloser, error)
	// UI is the terminal UI's bounded queue. The Sink only ever sends to it
	// without blocking Deliver; the owner may close it after Close returns.
	// Optional.
	UI chan<- contract.Event
	// PendingProgress bounds the progress events held for the run log;
	// zero or less uses DefaultPendingProgress.
	PendingProgress int
}

// progressBatch is one progress event, or several coalesced into the latest.
type progressBatch struct {
	ev contract.Event
	n  uint64
}

// event returns b as it is written or shown: the event itself, or an
// EventCoalesced event carrying the count.
func (b progressBatch) event() contract.Event {
	ev := cloneEvent(b.ev)
	if b.n > 1 {
		ev.Kind = contract.EventCoalesced
		if ev.Data == nil {
			ev.Data = make(map[contract.DataKey]string, 1)
		}
		ev.Data[contract.DataCount] = strconv.FormatUint(b.n, 10)
	}
	return ev
}

// Sink fans events out to the run log and the UI (SPC-001 "Events"):
//
//   - A non-progress event is written to the run log before Deliver returns,
//     together with the progress events waiting before it, so the log stays
//     in sequence order. Deliver blocks only for that write.
//   - A progress event never touches the log writer in Deliver. It waits in
//     a bounded buffer for the next log write; a full buffer coalesces it
//     into the newest waiting progress event with a count.
//   - The UI gets every event by a non-blocking send. When the queue is
//     full, progress events are coalesced into one waiting event, and a
//     non-progress event starts a replay: a background reader sends the UI
//     every non-progress event after the last one it saw, from the run log.
//     Non-progress events are never coalesced or dropped.
//   - The first log write error is kept and returned by Err; later writes
//     are skipped, because the log may end in a torn line.
//
// Callers deliver events with increasing Seq, as the engine assigns them.
type Sink struct {
	log     io.Writer
	openLog func() (io.ReadCloser, error)
	ui      chan<- contract.Event
	maxWait int

	// logMu serialises run-log writes. It is held only by a non-progress
	// Deliver and by Close, never by a progress Deliver.
	logMu sync.Mutex

	// mu guards the fields below. It is never held during I/O or a
	// blocking send.
	mu         sync.Mutex
	err        error
	waiting    []progressBatch // progress waiting for the run log
	lastLogged uint64          // Seq of the last non-progress event in the log
	uiSeen     uint64          // highest Seq sent to the UI
	uiWaiting  *progressBatch  // coalesced progress waiting for UI room
	spilled    bool            // the UI missed a non-progress event
	replaying  bool
	closed     bool

	done chan struct{}
	wg   sync.WaitGroup
}

// New returns a Sink for opts.
func New(opts Options) (*Sink, error) {
	if opts.Log == nil {
		return nil, fmt.Errorf("%w: Log is nil", ErrOptions)
	}
	if opts.UI != nil && opts.OpenLog == nil {
		return nil, fmt.Errorf("%w: OpenLog is nil with a UI", ErrOptions)
	}
	maxWait := opts.PendingProgress
	if maxWait <= 0 {
		maxWait = DefaultPendingProgress
	}
	return &Sink{
		log:     opts.Log,
		openLog: opts.OpenLog,
		ui:      opts.UI,
		maxWait: maxWait,
		done:    make(chan struct{}),
	}, nil
}

// Deliver records e and shows it on the UI. It implements contract.Sink: it
// never waits for the UI, and waits for the run log only to write a
// non-progress event.
func (s *Sink) Deliver(e contract.Event) {
	e = cloneEvent(e)
	if isProgress(e.Kind) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return
		}
		s.waiting = addProgress(s.waiting, e, s.maxWait)
		s.showProgressLocked(e)
		return
	}

	s.writeLog(&e)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.showLocked(e)
}

// Err returns the first run-log write error, wrapping ErrRunLog and the
// writer's error, or nil. The engine checks it at every step boundary and
// fails the step closed when it is set.
func (s *Sink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close writes the progress events still waiting, stops the replay reader
// and returns Err. After Close, Deliver does nothing and nothing more is
// sent to the UI.
func (s *Sink) Close() error {
	s.logMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.logMu.Unlock()
		return s.Err()
	}
	s.closed = true
	batch := s.takeWaitingLocked()
	s.mu.Unlock()
	s.writeBatch(batch, nil)
	s.logMu.Unlock()

	close(s.done)
	s.wg.Wait()
	return s.Err()
}

// writeLog writes the waiting progress events and then e in one write.
func (s *Sink) writeLog(e *contract.Event) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	batch := s.takeWaitingLocked()
	s.mu.Unlock()
	s.writeBatch(batch, e)
}

// takeWaitingLocked removes and returns the progress waiting for the log.
// After a write error it returns nothing: the log may end in a torn line,
// so nothing more is written to it.
func (s *Sink) takeWaitingLocked() []progressBatch {
	batch := s.waiting
	s.waiting = nil
	if s.err != nil {
		return nil
	}
	return batch
}

// writeBatch writes batch and then e, if not nil, in one write. The caller
// holds logMu.
func (s *Sink) writeBatch(batch []progressBatch, e *contract.Event) {
	s.mu.Lock()
	failed := s.err != nil
	s.mu.Unlock()
	if failed {
		return
	}

	var buf bytes.Buffer
	for _, b := range batch {
		encodeLine(&buf, b.event())
	}
	if e != nil {
		encodeLine(&buf, *e)
	}
	if buf.Len() == 0 {
		return
	}
	_, err := s.log.Write(buf.Bytes())

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.err = fmt.Errorf("%w: %w", ErrRunLog, err)
		return
	}
	if e != nil {
		s.lastLogged = e.Seq
	}
}

// showProgressLocked offers a progress event to the UI, coalescing it when
// the queue is full or a replay is running.
func (s *Sink) showProgressLocked(e contract.Event) {
	if s.ui == nil {
		return
	}
	// The UI gets its own copy of Data: the run log still holds e.
	if !s.spilled && s.flushUIWaitingLocked() && s.trySendLocked(cloneEvent(e)) {
		return
	}
	if s.uiWaiting == nil {
		s.uiWaiting = &progressBatch{ev: e, n: 1}
		return
	}
	s.uiWaiting.ev = e
	s.uiWaiting.n++
}

// showLocked offers a non-progress event to the UI. When the queue is full
// the UI misses it, and a replay from the run log delivers it later.
func (s *Sink) showLocked(e contract.Event) {
	if s.ui == nil || e.Seq <= s.uiSeen {
		return
	}
	if !s.spilled && s.flushUIWaitingLocked() && s.trySendLocked(e) {
		return
	}
	s.spilled = true
	if !s.replaying {
		s.replaying = true
		s.wg.Add(1)
		go s.replay()
	}
}

// flushUIWaitingLocked sends the coalesced progress event, if any, and
// reports whether nothing is left waiting.
func (s *Sink) flushUIWaitingLocked() bool {
	if s.uiWaiting == nil {
		return true
	}
	if !s.trySendLocked(s.uiWaiting.event()) {
		return false
	}
	s.uiWaiting = nil
	return true
}

func (s *Sink) trySendLocked(e contract.Event) bool {
	select {
	case s.ui <- e:
		s.uiSeen = max(s.uiSeen, e.Seq)
		return true
	default:
		return false
	}
}

// replay sends the UI, from the run log, every non-progress event it
// missed, until it has seen the last one logged. It runs in the background
// and may block on a stalled UI; Deliver never waits for it. One replay
// reads the log once, front to back, however many passes it takes.
func (s *Sink) replay() {
	defer s.wg.Done()
	var r *bufio.Reader
	var rc io.ReadCloser
	defer func() {
		if rc != nil {
			_ = rc.Close()
		}
	}()
	for {
		s.mu.Lock()
		from, target := s.uiSeen, s.lastLogged
		if from >= target {
			s.spilled = false
			s.replaying = false
			s.flushUIWaitingLocked()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		if rc == nil {
			var err error
			if rc, err = s.openLog(); err != nil {
				rc = nil
				s.stopReplay()
				return
			}
			r = bufio.NewReader(rc)
		}
		if !s.replayUntil(r, from, target) {
			s.stopReplay()
			return
		}
	}
}

// stopReplay ends a replay that was stopped or could not read the log. A
// later non-progress event starts a new one.
func (s *Sink) stopReplay() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replaying = false
}

// replayUntil reads r up to the line of target and sends the UI the
// non-progress events with from < Seq <= target. It reads no further than
// target's line, because a later line may still be partly written, so the
// next pass continues where this one stopped.
func (s *Sink) replayUntil(r *bufio.Reader, from, target uint64) bool {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// The log ended before target: truncated or unreadable.
			return false
		}
		var rec logRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return false
		}
		ev := rec.Event
		if ev.Seq > target {
			// Out of sequence: target's line was not where it belongs.
			return false
		}
		if ev.Seq > from && !isProgress(ev.Kind) {
			select {
			case s.ui <- ev:
			case <-s.done:
				return false
			}
			s.mu.Lock()
			s.uiSeen = max(s.uiSeen, ev.Seq)
			s.mu.Unlock()
		}
		if ev.Seq == target {
			return true
		}
	}
}

// logRecord is one run-log line (event.schema.json).
type logRecord struct {
	SchemaVersion int `json:"schema_version"`
	contract.Event
}

// encodeLine appends e as one JSON line. Encoding an Event cannot fail: its
// fields are strings, integers, a time and a string map.
func encodeLine(buf *bytes.Buffer, e contract.Event) {
	if e.Data == nil {
		e.Data = map[contract.DataKey]string{}
	}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(logRecord{SchemaVersion: eventSchemaVersion, Event: e})
}

// addProgress appends e to waiting, or, when waiting is full, coalesces it
// into the newest waiting batch.
func addProgress(waiting []progressBatch, e contract.Event, maxWait int) []progressBatch {
	if len(waiting) < maxWait {
		return append(waiting, progressBatch{ev: e, n: 1})
	}
	last := &waiting[len(waiting)-1]
	last.ev = e
	last.n++
	return waiting
}

// isProgress reports whether events of kind k may be coalesced.
func isProgress(k contract.EventKind) bool {
	return k == contract.EventProgress || k == contract.EventCoalesced
}

func cloneEvent(e contract.Event) contract.Event {
	if e.Data == nil {
		return e
	}
	data := make(map[contract.DataKey]string, len(e.Data))
	for k, v := range e.Data {
		data[k] = v
	}
	e.Data = data
	return e
}
