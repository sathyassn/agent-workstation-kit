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
	// ErrReplay wraps the first error replaying the run log to the UI. The
	// UI then missed events, so the step that sees it through Err fails
	// closed.
	ErrReplay = errors.New("replay run log to the UI")
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
	// *os.File does. Required when UI is set.
	OpenLog func() (io.ReadCloser, error)
	// UI is the terminal UI's bounded queue. The Sink only ever sends to it
	// without blocking Deliver; the owner may close it after Close returns.
	// Optional.
	UI chan<- contract.Event
	// PendingProgress bounds the progress events held for the run log, and
	// those held for the UI; zero or less uses DefaultPendingProgress.
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
//   - The UI sees events in increasing Seq order and gets every one:
//     non-progress events as they are, progress events alone or coalesced
//     with a count. Deliver only ever sends to the UI without blocking.
//     Progress that cannot be sent at once waits in a bounded buffer for the
//     UI, coalescing when it is full, and a non-progress event that cannot
//     be sent is replayed later from the run log. Both are sent by one
//     background worker, which merges them in Seq order and is the only
//     sender while it runs. Non-progress events are never coalesced or
//     dropped.
//   - The first run-log write error and the first replay error are kept and
//     returned by Err. After a write error nothing more is written, because
//     the log may end in a torn line. After a replay error the UI has
//     missed events, and later ones are sent directly again.
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
	logErr     error
	uiErr      error
	waiting    []progressBatch // progress waiting for the run log
	lastLogged uint64          // Seq of the last non-progress event in the log
	// inflight counts non-progress events between the start of Deliver and
	// their UI send. Progress delivered meanwhile is newer, so it waits.
	inflight int
	// uiNonProgress is the Seq of the last non-progress event the UI got. It
	// moves only for non-progress events, so progress never hides one.
	uiNonProgress uint64
	uiWaiting     []progressBatch // progress waiting for the UI, in Seq order
	// lastNonProgress is the Seq of the newest non-progress event Deliver
	// has started. Waiting progress coalesces only within the run between
	// two non-progress events, so the UI order stays exact.
	lastNonProgress uint64
	spilled         bool // the UI missed a logged non-progress event
	pumping         bool // the worker is running and is the only sender
	closed          bool

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

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.inflight++
	s.lastNonProgress = max(s.lastNonProgress, e.Seq)
	s.mu.Unlock()

	s.writeLog(&e)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
	if s.closed {
		return
	}
	s.showLocked(e)
}

// Err returns the first run-log write error, wrapping ErrRunLog, or else the
// first replay error, wrapping ErrReplay, or nil. The engine checks it at
// every step boundary and fails the step closed when it is set.
func (s *Sink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logErr != nil {
		return s.logErr
	}
	return s.uiErr
}

// Close writes the progress events still waiting for the log, stops the UI
// worker and returns Err. After Close, Deliver does nothing and nothing more
// is sent to the UI.
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
	if s.logErr != nil {
		return nil
	}
	return batch
}

// writeBatch writes batch and then e, if not nil, in one write. The caller
// holds logMu.
func (s *Sink) writeBatch(batch []progressBatch, e *contract.Event) {
	s.mu.Lock()
	failed := s.logErr != nil
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
		s.logErr = fmt.Errorf("%w: %w", ErrRunLog, err)
		return
	}
	if e != nil {
		s.lastLogged = e.Seq
	}
}

// directLocked reports whether Deliver may send to the UI itself: no worker
// is running and no non-progress event is between Deliver and its send.
func (s *Sink) directLocked() bool {
	return !s.pumping && !s.spilled && s.inflight == 0
}

// showProgressLocked sends a progress event to the UI, or holds it for the
// worker when the UI is full or an older event is still on its way.
func (s *Sink) showProgressLocked(e contract.Event) {
	if s.ui == nil {
		return
	}
	// The UI gets its own copy of Data: the run log still holds e.
	if s.directLocked() && len(s.uiWaiting) == 0 && s.trySend(cloneEvent(e)) {
		return
	}
	if n := len(s.uiWaiting); n > 0 && s.uiWaiting[n-1].ev.Seq > s.lastNonProgress {
		last := &s.uiWaiting[n-1]
		last.ev = e
		last.n++
	} else {
		s.uiWaiting = addProgress(s.uiWaiting, e, s.maxWait)
	}
	if s.inflight == 0 {
		s.startWorkerLocked()
	}
	// Otherwise the non-progress Deliver in flight sends it, or starts the
	// worker.
}

// showLocked sends a non-progress event to the UI after the progress that
// is older than it. When the UI is full, the event is left to the worker,
// which replays it from the run log.
func (s *Sink) showLocked(e contract.Event) {
	if s.ui == nil || e.Seq <= s.uiNonProgress {
		return
	}
	if s.directLocked() && s.sendOlderLocked(e.Seq) && s.trySend(e) {
		s.uiNonProgress = e.Seq
		s.sendOlderLocked(^uint64(0))
		if len(s.uiWaiting) > 0 && s.inflight == 0 {
			s.startWorkerLocked()
		}
		return
	}
	s.spilled = true
	if s.inflight == 0 {
		s.startWorkerLocked()
	}
	// Otherwise the non-progress Deliver still in flight starts it.
}

// sendOlderLocked sends, without blocking, the waiting progress older than
// seq, and reports whether none is left.
func (s *Sink) sendOlderLocked(seq uint64) bool {
	for len(s.uiWaiting) > 0 && s.uiWaiting[0].ev.Seq < seq {
		if !s.trySend(s.uiWaiting[0].event()) {
			return false
		}
		s.uiWaiting = s.uiWaiting[1:]
	}
	return true
}

func (s *Sink) trySend(e contract.Event) bool {
	select {
	case s.ui <- e:
		return true
	default:
		return false
	}
}

func (s *Sink) startWorkerLocked() {
	if s.pumping || s.closed {
		return
	}
	s.pumping = true
	s.wg.Add(1)
	go s.work()
}

// work is the UI worker. It replays missed non-progress events from the
// run log and sends the waiting progress, merged in Seq order, with blocking
// sends; Deliver never waits for it. It stops when nothing is left, or when
// a non-progress event is in flight, whose Deliver starts it again.
func (s *Sink) work() {
	defer s.wg.Done()
	var rc io.ReadCloser
	var r *bufio.Reader
	closeReplay := func() {
		if rc != nil {
			_ = rc.Close()
		}
		rc, r = nil, nil
	}
	defer closeReplay()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if s.spilled && s.uiNonProgress < s.lastLogged {
			from, target := s.uiNonProgress, s.lastLogged
			s.mu.Unlock()
			if r == nil {
				var err error
				if rc, err = s.openLog(); err != nil {
					rc = nil
					s.replayFailed(fmt.Errorf("open: %w", err))
					continue
				}
				r = bufio.NewReader(rc)
			}
			if err := s.replayUntil(r, from, target); err != nil {
				if errors.Is(err, errStopped) {
					return
				}
				closeReplay()
				s.replayFailed(err)
			}
			continue
		}
		if s.inflight > 0 {
			s.pumping = false
			s.mu.Unlock()
			return
		}
		s.spilled = false
		if len(s.uiWaiting) == 0 {
			s.pumping = false
			s.mu.Unlock()
			return
		}
		b := s.uiWaiting[0]
		s.uiWaiting = s.uiWaiting[1:]
		s.mu.Unlock()
		if !s.send(b.event()) {
			return
		}
	}
}

// errStopped ends the worker when the Sink closes.
var errStopped = errors.New("event sink closed")

// replayUntil reads r up to the line of target and sends the UI the
// non-progress events with from < Seq <= target, each after the waiting
// progress older than it. It reads no further than target's line, because
// a later line may still be partly written, so the next pass continues
// where this one stopped.
func (s *Sink) replayUntil(r *bufio.Reader, from, target uint64) error {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("read up to seq %d: %w", target, err)
		}
		var rec logRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("decode a line before seq %d: %w", target, err)
		}
		ev := rec.Event
		if ev.Seq > target {
			return fmt.Errorf("seq %d found before seq %d", ev.Seq, target)
		}
		if ev.Seq > from && !isProgress(ev.Kind) {
			if !s.sendWaitingBefore(ev.Seq) || !s.send(ev) {
				return errStopped
			}
			s.mu.Lock()
			s.uiNonProgress = max(s.uiNonProgress, ev.Seq)
			s.mu.Unlock()
		}
		if ev.Seq == target {
			return nil
		}
	}
}

// sendWaitingBefore sends the waiting progress older than seq.
func (s *Sink) sendWaitingBefore(seq uint64) bool {
	for {
		s.mu.Lock()
		if len(s.uiWaiting) == 0 || s.uiWaiting[0].ev.Seq >= seq {
			s.mu.Unlock()
			return true
		}
		b := s.uiWaiting[0]
		s.uiWaiting = s.uiWaiting[1:]
		s.mu.Unlock()
		if !s.send(b.event()) {
			return false
		}
	}
}

// send blocks until the UI takes e or the Sink closes. Only the worker
// calls it.
func (s *Sink) send(e contract.Event) bool {
	select {
	case s.ui <- e:
		return true
	case <-s.done:
		return false
	}
}

// replayFailed keeps the first replay error for Err and lets Deliver send
// to the UI directly again: the events it missed cannot be recovered, and
// the step fails closed on Err.
func (s *Sink) replayFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uiErr == nil {
		s.uiErr = fmt.Errorf("%w: %w", ErrReplay, err)
	}
	s.spilled = false
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
