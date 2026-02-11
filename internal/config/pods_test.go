package config

import "testing"

func TestValidatePodName(t *testing.T) {
	valid := []string{
		"backend",
		"my-pod",
		"pod-123",
		"a",
		"123",
		"a-b-c",
	}
	for _, name := range valid {
		if err := ValidatePodName(name); err != nil {
			t.Errorf("ValidatePodName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"My-Pod",
		"UPPER",
		"has space",
		"under_score",
		"has.dot",
		"has/slash",
		"café",
	}
	for _, name := range invalid {
		if err := ValidatePodName(name); err == nil {
			t.Errorf("ValidatePodName(%q) = nil, want error", name)
		}
	}
}
