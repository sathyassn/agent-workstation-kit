package events

// MaxUIWaiting exports the cap on progress batches waiting for the UI.
const MaxUIWaiting = maxUIWaiting

// UIWaitingLen returns the number of progress batches waiting for the UI.
func (s *Sink) UIWaitingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uiWaiting)
}
