package collector

import (
	"testing"
	"time"
)

func TestOutputCollector_ActiveOnOutput(t *testing.T) {
	c := NewOutputCollector()
	defer c.Stop()

	c.NoteOutput()

	select {
	case s := <-c.StateCh():
		if s != StateActive {
			t.Fatalf("expected StateActive, got %v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateActive")
	}
}

func TestOutputCollector_IdleAfterThreshold(t *testing.T) {
	c := NewOutputCollector()
	defer c.Stop()

	c.NoteOutput()
	// Drain the active signal.
	<-c.StateCh()

	select {
	case s := <-c.StateCh():
		if s != StateIdle {
			t.Fatalf("expected StateIdle, got %v", s)
		}
	case <-time.After(IdleThreshold + time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

func TestOutputCollector_ResetTimerOnOutput(t *testing.T) {
	c := NewOutputCollector()
	defer c.Stop()

	c.NoteOutput()
	<-c.StateCh() // drain active

	// Send another output before idle fires — should reset the timer.
	time.Sleep(IdleThreshold / 2)
	c.NoteOutput()

	select {
	case s := <-c.StateCh():
		if s != StateActive {
			t.Fatalf("expected StateActive from second output, got %v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second StateActive")
	}
}

func TestOutputCollector_Stop(t *testing.T) {
	c := NewOutputCollector()
	c.Stop()

	// After stop, NoteOutput should not panic.
	c.NoteOutput()
}
