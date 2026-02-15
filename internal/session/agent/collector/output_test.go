package collector

import (
	"testing"
	"time"
)

func setFastIdle(t *testing.T) {
	t.Helper()
	old := IdleThreshold
	IdleThreshold = 10 * time.Millisecond
	t.Cleanup(func() { IdleThreshold = old })
}

func TestOutputCollector_ActiveOnOutput(t *testing.T) {
	setFastIdle(t)
	c := NewOutputCollector()
	defer c.Stop()

	c.NoteOutput()

	select {
	case su := <-c.StateCh():
		if su.State != StateActive {
			t.Fatalf("expected StateActive, got %v", su.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateActive")
	}
}

func TestOutputCollector_IdleAfterThreshold(t *testing.T) {
	setFastIdle(t)
	c := NewOutputCollector()
	defer c.Stop()

	c.NoteOutput()
	// Drain the active signal.
	<-c.StateCh()

	select {
	case su := <-c.StateCh():
		if su.State != StateIdle {
			t.Fatalf("expected StateIdle, got %v", su.State)
		}
	case <-time.After(IdleThreshold + time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

func TestOutputCollector_ResetTimerOnOutput(t *testing.T) {
	setFastIdle(t)
	c := NewOutputCollector()
	defer c.Stop()

	c.NoteOutput()
	<-c.StateCh() // drain active

	// Send another output before idle fires — should reset the timer.
	time.Sleep(IdleThreshold / 2)
	c.NoteOutput()

	select {
	case su := <-c.StateCh():
		if su.State != StateActive {
			t.Fatalf("expected StateActive from second output, got %v", su.State)
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
