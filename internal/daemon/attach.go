package daemon

import (
	"encoding/json"
	"io"
	"net"

	"github.com/creack/pty"

	"h2/internal/message"
	"h2/internal/terminal"
)

// AttachSession represents an active attach client connection.
type AttachSession struct {
	conn net.Conn
}

// Close terminates the attach session.
func (s *AttachSession) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

// handleAttach handles an incoming attach request from a client.
func (d *Daemon) handleAttach(conn net.Conn, req *message.Request) {
	// Only one client at a time (v1).
	if d.attachClient != nil {
		message.SendResponse(conn, &message.Response{
			Error: "another client is already attached",
		})
		conn.Close()
		return
	}

	// Send OK response before switching to framed protocol.
	if err := message.SendResponse(conn, &message.Response{OK: true}); err != nil {
		conn.Close()
		return
	}

	session := &AttachSession{conn: conn}
	d.attachClient = session

	// Swap wrapper I/O to use the attach connection.
	w := d.Wrapper
	w.Mu.Lock()
	w.Output = &frameWriter{conn: conn}
	w.InputSrc = &frameInputReader{conn: conn}

	// Resize PTY to client's terminal size.
	if req.Cols > 0 && req.Rows > 0 {
		w.Rows = req.Rows
		w.Cols = req.Cols
		w.ChildRows = req.Rows - w.ReservedRows()
		w.Vt.Resize(w.ChildRows, req.Cols)
		pty.Setsize(w.Ptm, &pty.Winsize{
			Rows: uint16(w.ChildRows),
			Cols: uint16(req.Cols),
		})
	}

	// Send full screen redraw.
	w.Output.Write([]byte("\033[2J\033[H"))
	w.RenderScreen()
	w.RenderBar()
	w.Mu.Unlock()

	// Read input frames from client until disconnect.
	d.readClientInput(conn)

	// Client disconnected — detach.
	w.Mu.Lock()
	w.Output = io.Discard
	w.InputSrc = &blockingReader{}
	w.Mu.Unlock()

	d.attachClient = nil
}

// readClientInput reads framed input from the attach client and dispatches
// it to the wrapper.
func (d *Daemon) readClientInput(conn net.Conn) {
	for {
		frameType, payload, err := message.ReadFrame(conn)
		if err != nil {
			return // client disconnected
		}

		switch frameType {
		case message.FrameTypeData:
			// Simulate keyboard input by feeding into the wrapper.
			w := d.Wrapper
			w.Mu.Lock()
			if w.DebugKeys && len(payload) > 0 {
				w.AppendDebugBytes(payload)
				w.RenderBar()
			}
			for i := 0; i < len(payload); {
				switch w.Mode {
				case terminal.ModePassthrough:
					i = w.HandlePassthroughBytes(payload, i, len(payload))
				case terminal.ModeMenu:
					i = w.HandleMenuBytes(payload, i, len(payload))
				default:
					i = w.HandleDefaultBytes(payload, i, len(payload))
				}
			}
			w.Mu.Unlock()

		case message.FrameTypeControl:
			var ctrl message.ResizeControl
			if err := json.Unmarshal(payload, &ctrl); err != nil {
				continue
			}
			if ctrl.Type == "resize" {
				w := d.Wrapper
				w.Mu.Lock()
				w.Rows = ctrl.Rows
				w.Cols = ctrl.Cols
				w.ChildRows = ctrl.Rows - w.ReservedRows()
				w.Vt.Resize(w.ChildRows, ctrl.Cols)
				pty.Setsize(w.Ptm, &pty.Winsize{
					Rows: uint16(w.ChildRows),
					Cols: uint16(ctrl.Cols),
				})
				w.Output.Write([]byte("\033[2J"))
				w.RenderScreen()
				w.RenderBar()
				w.Mu.Unlock()
			}
		}
	}
}

// frameWriter wraps a net.Conn for writing attach data frames.
type frameWriter struct {
	conn net.Conn
}

func (fw *frameWriter) Write(p []byte) (int, error) {
	if err := message.WriteFrame(fw.conn, message.FrameTypeData, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// frameInputReader reads data frames from the attach client. It is used
// by the wrapper's ReadInput goroutine when in direct (non-daemon) mode
// where the wrapper reads from InputSrc directly. In attach mode, we
// instead read frames in readClientInput, so this reader blocks forever
// until the connection is closed.
type frameInputReader struct {
	conn net.Conn
}

func (fr *frameInputReader) Read(p []byte) (int, error) {
	// Block until the connection closes. Input is handled by readClientInput.
	buf := make([]byte, 1)
	_, err := fr.conn.Read(buf)
	return 0, err
}

// blockingReader blocks forever on Read. Used when no client is attached.
type blockingReader struct {
	ch chan struct{}
}

func (br *blockingReader) Read(p []byte) (int, error) {
	if br.ch == nil {
		br.ch = make(chan struct{})
	}
	<-br.ch // blocks forever
	return 0, io.EOF
}
