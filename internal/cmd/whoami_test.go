package cmd

import (
	"bytes"
	"testing"
)

func TestWhoamiCmd(t *testing.T) {
	t.Setenv("H2_ACTOR", "test-agent")

	cmd := newWhoamiCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("whoami command failed: %v", err)
	}

	// resolveActor prints to os.Stdout via fmt.Println, so just verify no error.
	// The actor resolution logic is tested in helpers_test.go.
}
