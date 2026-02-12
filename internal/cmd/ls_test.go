package cmd

import (
	"testing"

	"h2/internal/session/message"
)

func makeAgent(name, pod string) *message.AgentInfo {
	return &message.AgentInfo{
		Name:    name,
		Command: "claude",
		State:   "idle",
		Pod:     pod,
	}
}

func TestGroupAgentsByPod_NoPods(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", ""),
		makeAgent("a2", ""),
	}
	groups := groupAgentsByPod(agents, "")
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Pod != "" {
		t.Errorf("expected empty pod name, got %q", groups[0].Pod)
	}
	if len(groups[0].Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(groups[0].Agents))
	}
}

func TestGroupAgentsByPod_MixedPods(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", "backend"),
		makeAgent("a2", "frontend"),
		makeAgent("a3", ""),
		makeAgent("a4", "backend"),
	}
	groups := groupAgentsByPod(agents, "")

	// Should have 3 groups: backend, frontend, no-pod (alphabetical order for named pods).
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if groups[0].Pod != "backend" {
		t.Errorf("group[0] pod = %q, want %q", groups[0].Pod, "backend")
	}
	if len(groups[0].Agents) != 2 {
		t.Errorf("group[0] agents = %d, want 2", len(groups[0].Agents))
	}
	if groups[1].Pod != "frontend" {
		t.Errorf("group[1] pod = %q, want %q", groups[1].Pod, "frontend")
	}
	if len(groups[1].Agents) != 1 {
		t.Errorf("group[1] agents = %d, want 1", len(groups[1].Agents))
	}
	if groups[2].Pod != "" {
		t.Errorf("group[2] pod = %q, want empty", groups[2].Pod)
	}
	if len(groups[2].Agents) != 1 {
		t.Errorf("group[2] agents = %d, want 1", len(groups[2].Agents))
	}
}

func TestGroupAgentsByPod_FilterByName(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", "backend"),
		makeAgent("a2", "frontend"),
		makeAgent("a3", "backend"),
		makeAgent("a4", ""),
	}
	groups := groupAgentsByPod(agents, "backend")

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Pod != "backend" {
		t.Errorf("group[0] pod = %q, want %q", groups[0].Pod, "backend")
	}
	if len(groups[0].Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(groups[0].Agents))
	}
}

func TestGroupAgentsByPod_FilterByNameNoMatch(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", "backend"),
		makeAgent("a2", ""),
	}
	groups := groupAgentsByPod(agents, "nonexistent")

	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(groups))
	}
}

func TestGroupAgentsByPod_StarShowsAll(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", "backend"),
		makeAgent("a2", ""),
	}
	groups := groupAgentsByPod(agents, "*")

	// Star should show all grouped even if some have no pod.
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].Pod != "backend" {
		t.Errorf("group[0] pod = %q, want %q", groups[0].Pod, "backend")
	}
	if groups[1].Pod != "" {
		t.Errorf("group[1] pod = %q, want empty", groups[1].Pod)
	}
}

func TestGroupAgentsByPod_StarAllNoPod(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", ""),
		makeAgent("a2", ""),
	}
	groups := groupAgentsByPod(agents, "*")

	// Star with no pods still groups them.
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Pod != "" {
		t.Errorf("group[0] pod = %q, want empty", groups[0].Pod)
	}
}

func TestGroupAgentsByPod_Empty(t *testing.T) {
	groups := groupAgentsByPod(nil, "")
	if groups != nil {
		t.Errorf("expected nil, got %v", groups)
	}
}

func TestGroupAgentsByPod_OnlyPoddedAgents(t *testing.T) {
	agents := []*message.AgentInfo{
		makeAgent("a1", "backend"),
		makeAgent("a2", "frontend"),
	}
	groups := groupAgentsByPod(agents, "")

	// All agents have pods, so should be grouped.
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].Pod != "backend" {
		t.Errorf("group[0] pod = %q, want %q", groups[0].Pod, "backend")
	}
	if groups[1].Pod != "frontend" {
		t.Errorf("group[1] pod = %q, want %q", groups[1].Pod, "frontend")
	}
}
