package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHookCollector_ProcessEvent_Basic(t *testing.T) {
	hc := NewHookCollector()

	hc.ProcessEvent("UserPromptSubmit", nil)

	state := hc.State()
	if state.LastEvent != "UserPromptSubmit" {
		t.Fatalf("expected LastEvent 'UserPromptSubmit', got %q", state.LastEvent)
	}
	if state.LastEventTime.IsZero() {
		t.Fatal("expected non-zero LastEventTime")
	}
	if state.ToolUseCount != 0 {
		t.Fatalf("expected ToolUseCount 0, got %d", state.ToolUseCount)
	}
}

func TestHookCollector_ProcessEvent_PreToolUse(t *testing.T) {
	hc := NewHookCollector()

	payload := json.RawMessage(`{"tool_name": "Bash", "tool_input": {"command": "ls"}}`)
	hc.ProcessEvent("PreToolUse", payload)

	state := hc.State()
	if state.LastEvent != "PreToolUse" {
		t.Fatalf("expected LastEvent 'PreToolUse', got %q", state.LastEvent)
	}
	if state.LastToolName != "Bash" {
		t.Fatalf("expected LastToolName 'Bash', got %q", state.LastToolName)
	}
	if state.ToolUseCount != 1 {
		t.Fatalf("expected ToolUseCount 1, got %d", state.ToolUseCount)
	}
}

func TestHookCollector_ProcessEvent_PostToolUse(t *testing.T) {
	hc := NewHookCollector()

	payload := json.RawMessage(`{"tool_name": "Read"}`)
	hc.ProcessEvent("PostToolUse", payload)

	state := hc.State()
	if state.LastToolName != "Read" {
		t.Fatalf("expected LastToolName 'Read', got %q", state.LastToolName)
	}
	// PostToolUse does not increment tool count (only PreToolUse does).
	if state.ToolUseCount != 0 {
		t.Fatalf("expected ToolUseCount 0, got %d", state.ToolUseCount)
	}
}

func TestHookCollector_ProcessEvent_ToolCount(t *testing.T) {
	hc := NewHookCollector()

	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
	hc.ProcessEvent("PostToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Read"}`))
	hc.ProcessEvent("PostToolUse", json.RawMessage(`{"tool_name": "Read"}`))
	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Edit"}`))

	state := hc.State()
	if state.ToolUseCount != 3 {
		t.Fatalf("expected ToolUseCount 3, got %d", state.ToolUseCount)
	}
	if state.LastToolName != "Edit" {
		t.Fatalf("expected LastToolName 'Edit', got %q", state.LastToolName)
	}
}

func TestHookCollector_EventCh_Signaled(t *testing.T) {
	hc := NewHookCollector()

	hc.ProcessEvent("Stop", nil)

	select {
	case event := <-hc.EventCh():
		if event != "Stop" {
			t.Fatalf("expected event 'Stop', got %q", event)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("expected event on EventCh")
	}
}

func TestHookCollector_EventCh_NonBlocking(t *testing.T) {
	hc := NewHookCollector()

	// Fill the channel.
	hc.ProcessEvent("UserPromptSubmit", nil)
	// Second event should not block (drops if channel full).
	done := make(chan struct{})
	go func() {
		hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
		close(done)
	}()

	select {
	case <-done:
		// Good — did not block.
	case <-time.After(1 * time.Second):
		t.Fatal("ProcessEvent blocked when channel was full")
	}
}

func TestHookCollector_ExtractToolName_InvalidJSON(t *testing.T) {
	hc := NewHookCollector()

	hc.ProcessEvent("PreToolUse", json.RawMessage(`not json`))

	state := hc.State()
	if state.LastToolName != "" {
		t.Fatalf("expected empty LastToolName for invalid JSON, got %q", state.LastToolName)
	}
}

func TestHookCollector_ExtractToolName_NilPayload(t *testing.T) {
	hc := NewHookCollector()

	hc.ProcessEvent("PreToolUse", nil)

	state := hc.State()
	if state.LastToolName != "" {
		t.Fatalf("expected empty LastToolName for nil payload, got %q", state.LastToolName)
	}
}
