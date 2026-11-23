package events

import (
	"bufio"
	"bytes"
	"context"
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
	// ErrUIFlush reports that Close returned before the UI was sent every
	// event.
	ErrUIFlush = errors.New("flush events to the UI")
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
	// without blocking Deliver; the owner keeps reading it until Close
	// returns, and may close it after that. Optional.
	UI chan<- contract.Event
	// PendingProgress bounds the progress events held for the run log
	// between two non-progress events; zero or less uses
	// DefaultPendingProgress.
	PendingProgress int
}

// progressBatch is one progress event, or several coalesced into the latest.
// All of them lie between the same two non-progress events.
type progressBatch struct {
	ev    contract.Event // the newest event; its Seq is the batch's Seq
	first uint64         // Seq of the oldest event in the batch
	n     uint64
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
//     together with the progress events waiting before it. Writes happen in
//     the order their Deliver started, and each takes only the progress
//     older than its event, so the log stays in sequence order when events
//     are delivered concurrently. Deliver blocks only for its own write and
//     the older writes before it.
//   - A progress event never touches the log writer in Deliver. It waits in
//     a buffer for the next log write; past PendingProgress it coalesces
//     into the newest waiting progress event with a count, but never into
//     one older than a non-progress event.
//   - The UI sees events in increasing Seq order and gets every one:
//     non-progress events as they are, progress events alone or coalesced
//     with a count, each between the same non-progress events as when it was
//     delivered. Deliver only ever sends to the UI without blocking.
//     Progress that cannot be sent at once waits for the UI, coalesced within
//     the run between two non-progress events, and a non-progress event that
//     cannot be sent is replayed later from the run log. Both are sent by one
//     background worker, which merges them in Seq order and is the only
//     sender while it runs. While the UI is stalled, the progress waiting for
//     it is one batch per non-progress event it has not been sent. Close
//     waits for the worker to send everything. Non-progress events are never
//     coalesced or dropped.
//   - The first run-log write error and the first replay error are kept and
//     returned by Err. After a write error nothing more is written, because
//     the log may end in a torn line. After a replay error the UI has
//     missed events, and later ones are sent directly again.
//
// Callers deliver events with increasing Seq, as the engine assigns them:
// Deliver for an event starts before Deliver for a newer one, though the
// calls may overlap.
type Sink struct {
	log     io.Writer
	openLog func() (io.ReadCloser, error)
	ui      chan<- contract.Event
	maxWait int

	// mu guards the fields below. It is never held during I/O or a
	// blocking send.
	mu sync.Mutex
	// turn signals, on mu, that a run-log write finished or that inflight
	// went down.
	turn       *sync.Cond
	nextTicket uint64 // the turn the next run-log write takes
	writeTurn  uint64 // the turn of the run-log write that may run now
	logErr     error
	uiErr      error
	waiting    []progressBatch // progress waiting for the run log, in Seq order
	lastLogged uint64          // Seq of the last non-progress event in the log
	// inflight counts non-progress events between the start of Deliver and
	// their UI send. Progress delivered meanwhile is newer, so it waits.
	inflight int
	// uiNonProgress is the Seq of the last non-progress event the UI got. It
	// moves only for non-progress events, so progress never hides one.
	uiNonProgress uint64
	uiWaiting     []progressBatch // progress waiting for the UI, in Seq order
	// lastNonProgress is the Seq of the newest non-progress event Deliver
	// has started. Waiting progress coalesces only within the run after it,
	// so the log and UI order stay exact.
	lastNonProgress uint64
	spilled         bool // the UI missed a logged non-progress event
	pumping         bool // the worker is running and is the only sender
	closed          bool // Close has started; Deliver does nothing
	stopped         bool // Close has stopped the worker for good

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
	s := &Sink{
		log:     opts.Log,
		openLog: opts.OpenLog,
		ui:      opts.UI,
		maxWait: maxWait,
		done:    make(chan struct{}),
	}
	s.turn = sync.NewCond(&s.mu)
	return s, nil
}

// Deliver records e and shows it on the UI. It implements contract.Sink: it
// never waits for the UI, and waits for the run log only to write a
// non-progress event.
func (s *Sink) Deliver(e contract.Event) {
	e = cloneEvent(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if isProgress(e.Kind) {
		s.waiting = addProgress(s.waiting, e, s.lastNonProgress, len(s.waiting) >= s.maxWait)
		s.showProgressLocked(e)
		s.kickLocked()
		return
	}

	s.inflight++
	s.lastNonProgress = max(s.lastNonProgress, e.Seq)
	s.writeLogLocked(&e)
	s.inflight--
	s.showLocked(e)
	s.kickLocked()
	s.turn.Broadcast() // Close waits for inflight to reach zero
}

// Err returns the first run-log write error, wrapping ErrRunLog, or else the
// first replay error, wrapping ErrReplay, or nil. The engine checks it at
// every step boundary and fails the step closed when it is set.
func (s *Sink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errLocked()
}

func (s *Sink) errLocked() error {
	if s.logErr != nil {
		return s.logErr
	}
	return s.uiErr
}

// Close writes the progress events still waiting for the run log, after
// every non-progress event whose Deliver started before it, and waits until
// the UI has been sent every event or ctx is done. It returns Err, joined
// with an error wrapping ErrUIFlush when ctx ended before the UI got every
// event. After Close, Deliver does nothing and nothing more is sent to the
// UI. Close is called once; a later call only returns Err.
func (s *Sink) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		defer s.mu.Unlock()
		return s.errLocked()
	}
	s.closed = true
	s.writeLogLocked(nil)
	for s.inflight > 0 {
		s.turn.Wait()
	}
	s.kickLocked()
	s.mu.Unlock()

	// Nothing starts the worker from here on: Deliver does nothing, and no
	// non-progress Deliver is left to kick it.
	idle := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(idle)
	}()
	var flushErr error
	select {
	case <-idle:
	case <-ctx.Done():
		select {
		case <-idle:
		default:
			flushErr = fmt.Errorf("%w: %w", ErrUIFlush, ctx.Err())
		}
	}
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	close(s.done)
	<-idle
	return errors.Join(s.Err(), flushErr)
}

// writeLogLocked writes the waiting progress older than e and then e, in one
// write, or all the waiting progress when e is nil. It waits for the writes
// of every Deliver that started before it, so the log stays in Seq order.
// It releases mu while it waits and while it writes.
func (s *Sink) writeLogLocked(e *contract.Event) {
	ticket := s.nextTicket
	s.nextTicket++
	for s.writeTurn != ticket {
		s.turn.Wait()
	}
	defer func() {
		s.writeTurn++
		s.turn.Broadcast()
	}()

	before := ^uint64(0)
	if e != nil {
		before = e.Seq
	}
	batch := s.takeWaitingLocked(before)
	if s.logErr != nil {
		return
	}
	s.mu.Unlock()
	var buf bytes.Buffer
	for _, b := range batch {
		encodeLine(&buf, b.event())
	}
	if e != nil {
		encodeLine(&buf, *e)
	}
	var err error
	if buf.Len() > 0 {
		_, err = s.log.Write(buf.Bytes())
	}
	s.mu.Lock()
	if err != nil {
		s.logErr = fmt.Errorf("%w: %w", ErrRunLog, err)
		return
	}
	if e != nil {
		s.lastLogged = e.Seq
	}
}

// takeWaitingLocked removes and returns the progress waiting for the log
// with Seq before before. Newer progress stays for the write after it.
// After a write error it drops everything: the log may end in a torn line,
// so nothing more is written to it.
func (s *Sink) takeWaitingLocked(before uint64) []progressBatch {
	if s.logErr != nil {
		s.waiting = nil
		return nil
	}
	i := 0
	for i < len(s.waiting) && s.waiting[i].ev.Seq < before {
		i++
	}
	batch := s.waiting[:i:i]
	s.waiting = append([]progressBatch(nil), s.waiting[i:]...)
	return batch
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
	s.uiWaiting = addProgress(s.uiWaiting, e, s.lastNonProgress, true)
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
		return
	}
	s.spilled = true
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

// kickLocked starts the UI worker when the UI has events waiting and no
// non-progress event is between its Deliver and its UI send. Every Deliver
// calls it last, so the Deliver that leaves inflight at zero starts the
// worker for anything that waited meanwhile.
func (s *Sink) kickLocked() {
	if s.ui == nil || s.pumping || s.stopped || s.inflight > 0 {
		return
	}
	if len(s.uiWaiting) == 0 && !s.spilled {
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
		if s.stopped {
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

// addProgress adds e to waiting as a new batch, or, when coalesce is set,
// into the newest batch if every event in it is newer than boundary, the
// newest non-progress event Deliver has started. A batch therefore never
// spans a non-progress event, and its Seq stays its newest event's.
func addProgress(waiting []progressBatch, e contract.Event, boundary uint64, coalesce bool) []progressBatch {
	if n := len(waiting); coalesce && n > 0 && waiting[n-1].first > boundary {
		last := &waiting[n-1]
		last.ev = e
		last.n++
		return waiting
	}
	return append(waiting, progressBatch{ev: e, first: e.Seq, n: 1})
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
