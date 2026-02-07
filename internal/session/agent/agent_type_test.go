package agent

import "testing"

func TestResolveAgentType_Claude(t *testing.T) {
	at := ResolveAgentType("claude")
	if at.Name() != "claude" {
		t.Fatalf("expected name 'claude', got %q", at.Name())
	}
	if at.Command() != "claude" {
		t.Fatalf("expected command 'claude', got %q", at.Command())
	}
	if !at.Collectors().Otel || !at.Collectors().Hooks {
		t.Fatal("expected Otel and Hooks collectors for claude")
	}
	if at.OtelParser() == nil {
		t.Fatal("expected non-nil OtelParser for claude")
	}
}

func TestResolveAgentType_ClaudeFullPath(t *testing.T) {
	at := ResolveAgentType("/usr/local/bin/claude")
	if at.Name() != "claude" {
		t.Fatalf("expected name 'claude' for full path, got %q", at.Name())
	}
}

func TestResolveAgentType_Generic(t *testing.T) {
	at := ResolveAgentType("bash")
	if at.Name() != "generic" {
		t.Fatalf("expected name 'generic', got %q", at.Name())
	}
	if at.Command() != "bash" {
		t.Fatalf("expected command 'bash', got %q", at.Command())
	}
	if at.Collectors().Otel || at.Collectors().Hooks {
		t.Fatal("expected no collectors for generic type")
	}
	if at.OtelParser() != nil {
		t.Fatal("expected nil OtelParser for generic type")
	}
}

func TestClaudeCodeType_PrependArgs_WithSessionID(t *testing.T) {
	ct := NewClaudeCodeType()
	args := ct.PrependArgs("550e8400-e29b-41d4-a716-446655440000")
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[0] != "--session-id" || args[1] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestClaudeCodeType_PrependArgs_NoSessionID(t *testing.T) {
	ct := NewClaudeCodeType()
	args := ct.PrependArgs("")
	if args != nil {
		t.Fatalf("expected nil args for empty session ID, got %v", args)
	}
}

func TestClaudeCodeType_ChildEnv_WithPort(t *testing.T) {
	ct := NewClaudeCodeType()
	env := ct.ChildEnv(&CollectorPorts{OtelPort: 12345})
	if env == nil {
		t.Fatal("expected non-nil env with otel port")
	}
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Fatal("expected CLAUDE_CODE_ENABLE_TELEMETRY=1")
	}
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:12345" {
		t.Fatalf("unexpected endpoint: %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
}

func TestClaudeCodeType_ChildEnv_NoPort(t *testing.T) {
	ct := NewClaudeCodeType()
	env := ct.ChildEnv(&CollectorPorts{OtelPort: 0})
	if env != nil {
		t.Fatalf("expected nil env with no otel port, got %v", env)
	}
}

func TestClaudeCodeType_ChildEnv_NilPorts(t *testing.T) {
	ct := NewClaudeCodeType()
	env := ct.ChildEnv(nil)
	if env != nil {
		t.Fatalf("expected nil env with nil ports, got %v", env)
	}
}

func TestGenericType_PrependArgs_Ignored(t *testing.T) {
	gt := NewGenericType("bash")
	args := gt.PrependArgs("some-uuid")
	if args != nil {
		t.Fatalf("expected nil prepend args for generic, got %v", args)
	}
}

func TestGenericType_ChildEnv_Nil(t *testing.T) {
	gt := NewGenericType("bash")
	env := gt.ChildEnv(&CollectorPorts{OtelPort: 12345})
	if env != nil {
		t.Fatalf("expected nil child env for generic, got %v", env)
	}
}

func TestGenericType_DisplayCommand(t *testing.T) {
	gt := NewGenericType("/usr/bin/python3")
	if gt.DisplayCommand() != "/usr/bin/python3" {
		t.Fatalf("expected display command '/usr/bin/python3', got %q", gt.DisplayCommand())
	}
}
