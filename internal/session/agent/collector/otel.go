package collector

import "time"

// OtelCollector derives state from OTEL log events.
// It goes active on each NoteEvent signal and idle after IdleThreshold
// with no further events.
type OtelCollector struct {
	notifyCh chan struct{}
	stateCh  chan State
	stopCh   chan struct{}
}

// NewOtelCollector creates and starts an OtelCollector.
func NewOtelCollector() *OtelCollector {
	c := &OtelCollector{
		notifyCh: make(chan struct{}, 1),
		stateCh:  make(chan State, 1),
		stopCh:   make(chan struct{}),
	}
	go c.run()
	return c
}

// NoteEvent signals that an OTEL event was received.
func (c *OtelCollector) NoteEvent() {
	select {
	case c.notifyCh <- struct{}{}:
	default:
	}
}

// StateCh returns the channel that receives state transitions.
func (c *OtelCollector) StateCh() <-chan State {
	return c.stateCh
}

// Stop stops the internal goroutine.
func (c *OtelCollector) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

func (c *OtelCollector) run() {
	idleTimer := time.NewTimer(IdleThreshold)
	defer idleTimer.Stop()

	for {
		select {
		case <-c.notifyCh:
			c.send(StateActive)
			resetTimer(idleTimer, IdleThreshold)
		case <-idleTimer.C:
			c.send(StateIdle)
		case <-c.stopCh:
			return
		}
	}
}

func (c *OtelCollector) send(s State) {
	select {
	case c.stateCh <- s:
	default:
	}
}
