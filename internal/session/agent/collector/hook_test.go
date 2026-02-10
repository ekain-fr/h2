package collector

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHookCollector_ProcessEvent_Basic(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("UserPromptSubmit", nil)

	state := hc.Snapshot()
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
	hc := NewHookCollector(nil)
	defer hc.Stop()

	payload := json.RawMessage(`{"tool_name": "Bash", "tool_input": {"command": "ls"}}`)
	hc.ProcessEvent("PreToolUse", payload)

	state := hc.Snapshot()
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
	hc := NewHookCollector(nil)
	defer hc.Stop()

	payload := json.RawMessage(`{"tool_name": "Read"}`)
	hc.ProcessEvent("PostToolUse", payload)

	state := hc.Snapshot()
	if state.LastToolName != "Read" {
		t.Fatalf("expected LastToolName 'Read', got %q", state.LastToolName)
	}
	// PostToolUse does not increment tool count (only PreToolUse does).
	if state.ToolUseCount != 0 {
		t.Fatalf("expected ToolUseCount 0, got %d", state.ToolUseCount)
	}
}

func TestHookCollector_ProcessEvent_ToolCount(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
	hc.ProcessEvent("PostToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Read"}`))
	hc.ProcessEvent("PostToolUse", json.RawMessage(`{"tool_name": "Read"}`))
	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Edit"}`))

	state := hc.Snapshot()
	if state.ToolUseCount != 3 {
		t.Fatalf("expected ToolUseCount 3, got %d", state.ToolUseCount)
	}
	if state.LastToolName != "Edit" {
		t.Fatalf("expected LastToolName 'Edit', got %q", state.LastToolName)
	}
}

func TestHookCollector_StateCh_ActiveOnToolUse(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`))

	select {
	case su := <-hc.StateCh():
		if su.State != StateActive {
			t.Fatalf("expected StateActive, got %v", su.State)
		}
		if su.SubState != SubStateToolUse {
			t.Fatalf("expected SubStateToolUse, got %v", su.SubState)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateActive")
	}
}

func TestHookCollector_StateCh_IdleOnStop(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("Stop", nil)

	select {
	case su := <-hc.StateCh():
		if su.State != StateIdle {
			t.Fatalf("expected StateIdle, got %v", su.State)
		}
		if su.SubState != SubStateNone {
			t.Fatalf("expected SubStateNone, got %v", su.SubState)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

func TestHookCollector_StateCh_ExitedOnSessionEnd(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("SessionEnd", nil)

	select {
	case su := <-hc.StateCh():
		if su.State != StateExited {
			t.Fatalf("expected StateExited, got %v", su.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateExited")
	}
}

func TestHookCollector_StateCh_IdleOnSessionStart(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("SessionStart", nil)

	select {
	case su := <-hc.StateCh():
		if su.State != StateIdle {
			t.Fatalf("expected StateIdle, got %v", su.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateIdle")
	}
}

func TestHookCollector_ProcessEvent_NonBlocking(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	// Fill the activity channel and state channel.
	for i := 0; i < 20; i++ {
		done := make(chan struct{})
		go func() {
			hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ProcessEvent blocked")
		}
	}
}

func TestHookCollector_ExtractToolName_InvalidJSON(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("PreToolUse", json.RawMessage(`not json`))

	state := hc.Snapshot()
	if state.LastToolName != "" {
		t.Fatalf("expected empty LastToolName for invalid JSON, got %q", state.LastToolName)
	}
}

func TestHookCollector_ExtractToolName_NilPayload(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("PreToolUse", nil)

	state := hc.Snapshot()
	if state.LastToolName != "" {
		t.Fatalf("expected empty LastToolName for nil payload, got %q", state.LastToolName)
	}
}

func TestHookCollector_BlockedPermission(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	// Not blocked initially.
	state := hc.Snapshot()
	if state.BlockedOnPermission {
		t.Fatal("should not be blocked initially")
	}

	// Send blocked_permission event.
	payload := json.RawMessage(`{"tool_name": "Bash", "hook_event_name": "blocked_permission"}`)
	hc.ProcessEvent("blocked_permission", payload)

	state = hc.Snapshot()
	if !state.BlockedOnPermission {
		t.Fatal("should be blocked after blocked_permission event")
	}
	if state.BlockedToolName != "Bash" {
		t.Errorf("BlockedToolName = %q, want %q", state.BlockedToolName, "Bash")
	}
}

func TestHookCollector_BlockedPermission_ClearedByPreToolUse(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	// Set blocked.
	hc.ProcessEvent("blocked_permission", json.RawMessage(`{"tool_name": "Bash"}`))
	if !hc.Snapshot().BlockedOnPermission {
		t.Fatal("should be blocked")
	}

	// PreToolUse clears it (tool was approved).
	hc.ProcessEvent("PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`))
	state := hc.Snapshot()
	if state.BlockedOnPermission {
		t.Fatal("should be unblocked after PreToolUse")
	}
	if state.BlockedToolName != "" {
		t.Errorf("BlockedToolName = %q, want empty", state.BlockedToolName)
	}
}

func TestHookCollector_BlockedPermission_ClearedByUserPromptSubmit(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("blocked_permission", json.RawMessage(`{"tool_name": "Bash"}`))
	hc.ProcessEvent("UserPromptSubmit", nil)

	if hc.Snapshot().BlockedOnPermission {
		t.Fatal("should be unblocked after UserPromptSubmit")
	}
}

func TestHookCollector_BlockedPermission_ClearedByStop(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("blocked_permission", json.RawMessage(`{"tool_name": "Bash"}`))
	hc.ProcessEvent("Stop", nil)

	if hc.Snapshot().BlockedOnPermission {
		t.Fatal("should be unblocked after Stop")
	}
}

func TestHookCollector_BlockedPermission_NotClearedByPermissionRequest(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	hc.ProcessEvent("blocked_permission", json.RawMessage(`{"tool_name": "Bash"}`))
	// PermissionRequest should NOT clear the blocked state — it's the event
	// that triggered the block in the first place.
	hc.ProcessEvent("PermissionRequest", json.RawMessage(`{"tool_name": "Bash"}`))

	if !hc.Snapshot().BlockedOnPermission {
		t.Fatal("should still be blocked after PermissionRequest")
	}
}

// --- HookState.SubState() unit tests ---

func TestHookState_SubState_Thinking(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "PostToolUse"} {
		hs := HookState{LastEvent: event}
		if got := hs.SubState(); got != SubStateThinking {
			t.Errorf("SubState() for %q = %v, want SubStateThinking", event, got)
		}
	}
}

func TestHookState_SubState_ToolUse(t *testing.T) {
	hs := HookState{LastEvent: "PreToolUse"}
	if got := hs.SubState(); got != SubStateToolUse {
		t.Fatalf("SubState() for PreToolUse = %v, want SubStateToolUse", got)
	}
}

func TestHookState_SubState_WaitingForPermission_FromEvent(t *testing.T) {
	hs := HookState{LastEvent: "PermissionRequest"}
	if got := hs.SubState(); got != SubStateWaitingForPermission {
		t.Fatalf("SubState() for PermissionRequest = %v, want SubStateWaitingForPermission", got)
	}
}

func TestHookState_SubState_WaitingForPermission_BlockedFlag(t *testing.T) {
	// BlockedOnPermission takes priority over lastEvent-based classification.
	hs := HookState{LastEvent: "PreToolUse", BlockedOnPermission: true}
	if got := hs.SubState(); got != SubStateWaitingForPermission {
		t.Fatalf("SubState() with BlockedOnPermission = %v, want SubStateWaitingForPermission", got)
	}
}

func TestHookState_SubState_None_UnknownEvent(t *testing.T) {
	hs := HookState{LastEvent: "SessionStart"}
	if got := hs.SubState(); got != SubStateNone {
		t.Fatalf("SubState() for SessionStart = %v, want SubStateNone", got)
	}
}

func TestSubState_String(t *testing.T) {
	tests := []struct {
		ss   SubState
		want string
	}{
		{SubStateNone, ""},
		{SubStateThinking, "thinking"},
		{SubStateToolUse, "tool_use"},
		{SubStateWaitingForPermission, "waiting_for_permission"},
		{SubState(99), ""},
	}
	for _, tt := range tests {
		if got := tt.ss.String(); got != tt.want {
			t.Errorf("SubState(%d).String() = %q, want %q", tt.ss, got, tt.want)
		}
	}
}

func TestHookCollector_StateCh_PermissionDecisionEmitsSubState(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	// permission_decision with ask_user should emit WaitingForPermission sub-state.
	payload := json.RawMessage(`{"tool_name": "Bash", "decision": "ask_user"}`)
	hc.ProcessEvent("permission_decision", payload)

	select {
	case su := <-hc.StateCh():
		// State stays Initialized (permission_decision doesn't change State),
		// but SubState should be WaitingForPermission.
		if su.SubState != SubStateWaitingForPermission {
			t.Fatalf("expected SubStateWaitingForPermission, got %v", su.SubState)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for StateUpdate from permission_decision")
	}
}

func TestHookCollector_StateCh_EventSequence(t *testing.T) {
	hc := NewHookCollector(nil)
	defer hc.Stop()

	// Simulate a typical sequence and verify each emitted StateUpdate.
	type expected struct {
		event    string
		payload  json.RawMessage
		state    State
		subState SubState
	}
	steps := []expected{
		{"UserPromptSubmit", nil, StateActive, SubStateThinking},
		{"PreToolUse", json.RawMessage(`{"tool_name": "Bash"}`), StateActive, SubStateToolUse},
		{"PostToolUse", json.RawMessage(`{"tool_name": "Bash"}`), StateActive, SubStateThinking},
	}
	for _, step := range steps {
		hc.ProcessEvent(step.event, step.payload)
		select {
		case su := <-hc.StateCh():
			if su.State != step.state {
				t.Fatalf("after %s: State = %v, want %v", step.event, su.State, step.state)
			}
			if su.SubState != step.subState {
				t.Fatalf("after %s: SubState = %v, want %v", step.event, su.SubState, step.subState)
			}
		case <-time.After(time.Second):
			t.Fatalf("after %s: timed out waiting for StateUpdate", step.event)
		}
	}
}
