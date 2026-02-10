package cmd

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"h2/internal/session/message"
	"h2/internal/socketdir"
)

func TestStopCmd_RequiresArg(t *testing.T) {
	cmd := newStopCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no args provided")
	}
}

func TestStopCmd_NoSocket(t *testing.T) {
	// Point socket dir at an empty temp dir so Find fails.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	os.MkdirAll(filepath.Join(tmpDir, ".h2", "sockets"), 0o700)

	cmd := newStopCmd()
	cmd.SetArgs([]string{"nonexistent"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing socket")
	}
}

func TestStopCmd_SendsStopRequest(t *testing.T) {
	// Use a short path to stay under macOS's ~104 byte socket path limit.
	tmpDir, err := os.MkdirTemp("/tmp", "h2t-stop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	sockDir := filepath.Join(tmpDir, "s")
	os.MkdirAll(sockDir, 0o700)
	t.Setenv("HOME", tmpDir)

	// Symlink ~/.h2/sockets -> our short sockDir so socketdir.Find works.
	h2Dir := filepath.Join(tmpDir, ".h2", "sockets")
	os.MkdirAll(filepath.Dir(h2Dir), 0o700)
	os.Symlink(sockDir, h2Dir)

	// Create a mock socket that handles the stop request.
	sockPath := filepath.Join(sockDir, socketdir.Format(socketdir.TypeAgent, "a"))
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var received *message.Request
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := message.ReadRequest(conn)
		if err != nil {
			return
		}
		received = req
		message.SendResponse(conn, &message.Response{OK: true})
	}()

	cmd := newStopCmd()
	cmd.SetArgs([]string{"a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	<-done
	if received == nil {
		t.Fatal("expected to receive a request")
	}
	if received.Type != "stop" {
		t.Errorf("expected type=stop, got %q", received.Type)
	}
}
