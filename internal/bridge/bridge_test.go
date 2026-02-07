package bridge

import (
	"testing"
)

func TestParseAgentPrefix(t *testing.T) {
	tests := []struct {
		input     string
		wantAgent string
		wantBody  string
	}{
		{"running-deer: check build", "running-deer", "check build"},
		{"agent_1: hello", "agent_1", "hello"},
		{"MyAgent: test", "MyAgent", "test"},
		{"no prefix here", "", "no prefix here"},
		{"", "", ""},
		{"agent: body: with: colons", "agent", "body: with: colons"},
		{"agent:no space", "agent", "no space"},
		{"agent:  extra spaces", "agent", "extra spaces"},
		{": empty agent", "", ": empty agent"},
	}

	for _, tt := range tests {
		agent, body := ParseAgentPrefix(tt.input)
		if agent != tt.wantAgent || body != tt.wantBody {
			t.Errorf("ParseAgentPrefix(%q) = (%q, %q), want (%q, %q)",
				tt.input, agent, body, tt.wantAgent, tt.wantBody)
		}
	}
}

func TestStripH2Envelope(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			"[h2 message from: concierge] build complete",
			"build complete",
		},
		{
			"[URGENT h2 message from: running-deer] server down",
			"server down",
		},
		{
			"no envelope here",
			"no envelope here",
		},
		{
			"",
			"",
		},
		{
			"[h2 message from: agent] Read /some/path",
			"Read /some/path",
		},
		{
			"[h2 message from: agent]   extra whitespace  ",
			"extra whitespace",
		},
	}

	for _, tt := range tests {
		got := StripH2Envelope(tt.input)
		if got != tt.want {
			t.Errorf("StripH2Envelope(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
