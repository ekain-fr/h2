package collector

import "testing"

func TestFormatStateLabel(t *testing.T) {
	tests := []struct {
		state    string
		subState string
		want     string
	}{
		// No sub-state — just capitalized state.
		{"active", "", "Active"},
		{"idle", "", "Idle"},
		{"exited", "", "Exited"},
		{"initialized", "", "Initialized"},
		{"unknown", "", "unknown"},

		// Active with sub-states.
		{"active", "thinking", "Active (thinking)"},
		{"active", "tool_use", "Active (tool use)"},
		{"active", "waiting_for_permission", "Active (permission)"},

		// Unknown sub-state passed through.
		{"active", "something_new", "Active (something_new)"},

		// Non-active state with sub-state (unlikely but handled).
		{"idle", "thinking", "Idle (thinking)"},
	}
	for _, tt := range tests {
		got := FormatStateLabel(tt.state, tt.subState)
		if got != tt.want {
			t.Errorf("FormatStateLabel(%q, %q) = %q, want %q", tt.state, tt.subState, got, tt.want)
		}
	}
}
