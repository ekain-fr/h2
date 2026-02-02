package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/muesli/termenv"
	"github.com/vito/midterm"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: h2 <command> [args...]\n")
		fmt.Fprintf(os.Stderr, "\nWraps a TUI application with a persistent input bar.\n")
		fmt.Fprintf(os.Stderr, "All keyboard input goes to the bottom bar.\n")
		fmt.Fprintf(os.Stderr, "Press Enter to send the command to the wrapped application.\n")
		os.Exit(1)
	}

	w := &wrapper{}
	err := w.run(os.Args[1], os.Args[2:]...)
	if err != nil {
		if w.quit {
			return
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type wrapper struct {
	ptm     *os.File          // PTY master (connected to child process)
	cmd     *exec.Cmd         // child process
	mu      sync.Mutex        // guards all terminal writes
	restore *term.State       // original terminal state for cleanup
	vt      *midterm.Terminal // virtual terminal for child output
	input   []byte            // current command line buffer
	rows    int               // terminal rows
	cols    int               // terminal cols
	history []string          // command history
	histIdx int               // current position in history (-1 = typing new)
	saved   []byte            // saved input when browsing history
	quit    bool              // true when user selected Quit
	oscFg   string            // cached OSC 10 response (foreground color)
	oscBg   string            // cached OSC 11 response (background color)
	lastOut time.Time         // last time child output updated the screen
	mode    inputMode         // current input mode
	menuIdx int               // selected menu item

	pendingSlash bool        // awaiting second slash for menu
	slashTimer   *time.Timer // timer to promote single slash to passthrough

	pendingEsc     bool        // awaiting more bytes to disambiguate ESC
	escTimer       *time.Timer // timer to treat ESC as passthrough exit
	passthroughEsc []byte      // buffered escape sequence bytes

	childRows   int      // number of rows reserved for the child PTY
	debugKeys   bool     // show debug keystrokes bar
	debugKeyBuf []string // most recent keystrokes
}

type inputMode int

const (
	modeMessage inputMode = iota
	modePassthrough
	modeMenu
)

var menuItems = []string{"Clear input", "Redraw", "Quit"}

func (w *wrapper) run(command string, args ...string) error {
	fd := int(os.Stdin.Fd())

	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return fmt.Errorf("get terminal size (is this a terminal?): %w", err)
	}
	w.debugKeys = isTruthyEnv("H2_DEBUG_KEYS")
	minRows := 3
	if w.debugKeys {
		minRows = 4
	}
	if rows < minRows {
		return fmt.Errorf("terminal too small (need at least %d rows, have %d)", minRows, rows)
	}
	w.rows = rows
	w.cols = cols
	w.histIdx = -1
	w.childRows = rows - w.reservedRows()
	w.vt = midterm.NewTerminal(w.childRows, cols)
	w.lastOut = time.Now()
	w.mode = modeMessage

	// Detect the real terminal's colors before entering raw mode.
	// midterm swallows OSC 10/11 color queries from the child, so we
	// query the real terminal here and cache the responses. When the
	// child later queries OSC 10/11, we respond with the cached values.
	output := termenv.NewOutput(os.Stdout)
	if fg := output.ForegroundColor(); fg != nil {
		w.oscFg = colorToX11(fg)
	}
	if bg := output.BackgroundColor(); bg != nil {
		w.oscBg = colorToX11(bg)
	}
	if os.Getenv("COLORFGBG") == "" {
		colorfgbg := "0;15" // light background default
		if output.HasDarkBackground() {
			colorfgbg = "15;0"
		}
		os.Setenv("COLORFGBG", colorfgbg)
	}

	// Start child in a PTY, reserving rows for the separator + input + debug bar.
	w.cmd = exec.Command(command, args...)
	w.ptm, err = pty.StartWithSize(w.cmd, &pty.Winsize{
		Rows: uint16(w.childRows),
		Cols: uint16(cols),
	})
	if err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	defer w.ptm.Close()

	// Let midterm forward mode-setting sequences (bracketed paste, cursor
	// keys, etc.) to the real terminal and respond to DA queries from the child.
	w.vt.ForwardRequests = os.Stdout
	w.vt.ForwardResponses = w.ptm

	// Put our terminal into raw mode so we get every keystroke.
	w.restore, err = term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	defer func() {
		term.Restore(fd, w.restore)
		os.Stdout.WriteString("\033[?25h\033[0m\r\n")
	}()

	// Handle terminal resize.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go w.watchResize(sigCh)

	// Update status bar every second for idle tracking.
	stopStatus := make(chan struct{})
	go w.tickStatus(stopStatus)

	// Draw initial UI.
	w.mu.Lock()
	os.Stdout.WriteString("\033[2J\033[H")
	w.renderScreen()
	w.renderBar()
	w.mu.Unlock()

	// Pipe child output to our terminal.
	go w.pipeOutput()

	// Process user keyboard input.
	go w.readInput()

	err = w.cmd.Wait()
	close(stopStatus)
	return err
}

// pipeOutput reads the child's terminal output into the virtual terminal,
// then re-renders the screen from midterm's buffer state.
func (w *wrapper) pipeOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := w.ptm.Read(buf)
		if n > 0 {
			// Respond to OSC 10/11 color queries that midterm swallows.
			w.respondOSCColors(buf[:n])

			w.mu.Lock()
			w.lastOut = time.Now()
			w.vt.Write(buf[:n])
			w.renderScreen()
			w.renderBar()
			w.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// respondOSCColors checks if the child output contains OSC 10 or 11 color
// queries and responds with the cached real terminal colors. midterm swallows
// these queries, so we need to handle them ourselves.
func (w *wrapper) respondOSCColors(data []byte) {
	if w.oscFg != "" && bytes.Contains(data, []byte("\033]10;?")) {
		fmt.Fprintf(w.ptm, "\033]10;%s\033\\", w.oscFg)
	}
	if w.oscBg != "" && bytes.Contains(data, []byte("\033]11;?")) {
		fmt.Fprintf(w.ptm, "\033]11;%s\033\\", w.oscBg)
	}
}

// readInput reads keyboard input and either buffers it into the command line
// or handles control characters. Enter sends the buffered text to the child.
func (w *wrapper) readInput() {
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}

		w.mu.Lock()
		if w.debugKeys && n > 0 {
			w.appendDebugBytes(buf[:n])
			w.renderBar()
		}
		for i := 0; i < n; {
			switch w.mode {
			case modePassthrough:
				i = w.handlePassthroughBytes(buf, i, n)
			case modeMenu:
				i = w.handleMenuBytes(buf, i, n)
			default:
				i = w.handleMessageBytes(buf, i, n)
			}
		}
		w.mu.Unlock()
	}
}

func (w *wrapper) startPendingEsc() {
	w.pendingEsc = true
	if w.escTimer != nil {
		w.escTimer.Stop()
	}
	w.escTimer = time.AfterFunc(50*time.Millisecond, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.pendingEsc && w.mode == modePassthrough {
			w.pendingEsc = false
			w.passthroughEsc = w.passthroughEsc[:0]
			w.mode = modeMessage
			w.renderBar()
		}
	})
}

func (w *wrapper) cancelPendingEsc() {
	if w.escTimer != nil {
		w.escTimer.Stop()
	}
	w.pendingEsc = false
}

func (w *wrapper) handlePassthroughBytes(buf []byte, start, n int) int {
	for i := start; i < n; {
		b := buf[i]
		if w.pendingEsc {
			if b != '[' && b != 'O' {
				w.cancelPendingEsc()
				w.passthroughEsc = w.passthroughEsc[:0]
				w.mode = modeMessage
				w.renderBar()
				return i
			}
			w.cancelPendingEsc()
			w.passthroughEsc = append(w.passthroughEsc[:0], 0x1B, b)
			if w.flushPassthroughEscIfComplete() {
				i++
				continue
			}
			i++
			continue
		}
		if len(w.passthroughEsc) > 0 {
			w.passthroughEsc = append(w.passthroughEsc, b)
			if w.flushPassthroughEscIfComplete() {
				i++
				continue
			}
			i++
			continue
		}
		switch b {
		case 0x0D, 0x0A: // Enter ends passthrough
			w.cancelPendingEsc()
			w.passthroughEsc = w.passthroughEsc[:0]
			w.ptm.Write([]byte{'\r'})
			w.mode = modeMessage
			w.renderBar()
			i++
		case 0x1B: // Escape ends passthrough if standalone (after a short delay)
			w.startPendingEsc()
			i++
		case 0x7F, 0x08: // Backspace
			w.ptm.Write([]byte{b})
			i++
		default:
			w.ptm.Write([]byte{b})
			i++
		}
	}
	return n
}

func (w *wrapper) handleMenuBytes(buf []byte, start, n int) int {
	for i := start; i < n; {
		b := buf[i]
		i++
		if b == 0x1B {
			consumed, handled := w.handleEscape(buf[i:n])
			i += consumed
			if handled {
				continue
			}
			if i == n {
				w.mode = modeMessage
				w.renderBar()
			}
			continue
		}
		switch b {
		case 0x0D, 0x0A: // Enter selects
			w.menuSelect()
			w.mode = modeMessage
			w.renderBar()
		}
	}
	return n
}

func (w *wrapper) handleMessageBytes(buf []byte, start, n int) int {
	for i := start; i < n; {
		b := buf[i]
		i++

		if w.pendingSlash {
			w.cancelPendingSlash()
			if b == '/' {
				w.mode = modeMenu
				w.menuIdx = 0
				w.renderBar()
				continue
			}
			w.mode = modePassthrough
			w.ptm.Write([]byte{'/'})
			w.renderBar()
			// Continue handling this byte in passthrough mode.
			switch b {
			case 0x0D, 0x0A:
				w.ptm.Write([]byte{'\r'})
				w.mode = modeMessage
				w.renderBar()
			case 0x1B:
				if i == n {
					w.mode = modeMessage
					w.renderBar()
				} else {
					w.ptm.Write([]byte{0x1B})
				}
			default:
				w.ptm.Write([]byte{b})
			}
			continue
		}

		if b == '/' && len(w.input) == 0 {
			w.startPendingSlash()
			w.renderBar()
			continue
		}

		// Detect and handle escape sequences.
		if b == 0x1B {
			consumed, handled := w.handleEscape(buf[i:n])
			i += consumed
			if handled {
				continue
			}
			// Lone Escape with no recognized sequence: ignore.
			continue
		}

		switch b {
		case 0x09: // Tab: send to child (for completion)
			w.ptm.Write([]byte{'\t'})

		case 0x0D, 0x0A: // Enter: send buffered text to child
			if len(w.input) > 0 {
				cmd := string(w.input)
				// Send text first, then \r after a short delay. The
				// child's UI framework (React/Ink) batches state updates,
				// so the submit handler won't see the typed text unless
				// we let a render cycle complete before sending Enter.
				w.ptm.Write(w.input)
				w.history = append(w.history, cmd)
				w.input = w.input[:0]
				go func() {
					time.Sleep(50 * time.Millisecond)
					w.ptm.Write([]byte{'\r'})
				}()
			} else {
				// Bare enter still forwarded (for confirmation prompts, etc.)
				w.ptm.Write([]byte{'\r'})
			}
			w.histIdx = -1
			w.saved = nil
			w.renderBar()

		case 0x7F, 0x08: // Backspace
			if len(w.input) > 0 {
				_, size := utf8.DecodeLastRune(w.input)
				w.input = w.input[:len(w.input)-size]
				w.renderBar()
			}

		default:
			if b < 0x20 { // Control: forward to child
				w.ptm.Write([]byte{b})
			} else { // Printable
				w.input = append(w.input, b)
				w.renderBar()
			}
		}
	}
	return n
}

func (w *wrapper) reservedRows() int {
	if w.debugKeys {
		return 3
	}
	return 2
}

func (w *wrapper) flushPassthroughEscIfComplete() bool {
	if len(w.passthroughEsc) == 0 {
		return false
	}
	if !isEscSequenceComplete(w.passthroughEsc) {
		return false
	}
	if isShiftEnterSequence(w.passthroughEsc) {
		w.ptm.Write([]byte{'\r'})
	} else {
		w.ptm.Write(w.passthroughEsc)
	}
	w.passthroughEsc = w.passthroughEsc[:0]
	return true
}

func isEscSequenceComplete(seq []byte) bool {
	if len(seq) < 2 {
		return false
	}
	switch seq[1] {
	case '[': // CSI
		final := seq[len(seq)-1]
		return final >= 0x40 && final <= 0x7E
	case 'O': // SS3
		return len(seq) >= 3
	default:
		return true
	}
}

func isShiftEnterSequence(seq []byte) bool {
	if len(seq) < 3 {
		return false
	}
	if seq[1] != '[' {
		return false
	}
	final := seq[len(seq)-1]
	params := string(seq[2 : len(seq)-1])
	switch final {
	case '~':
		return params == "27;2;13" || params == "13;2"
	case 'u':
		return params == "13;2"
	default:
		return false
	}
}

func isTruthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func (w *wrapper) appendDebugBytes(data []byte) {
	for _, b := range data {
		w.debugKeyBuf = append(w.debugKeyBuf, formatDebugKey(b))
		if len(w.debugKeyBuf) > 10 {
			w.debugKeyBuf = w.debugKeyBuf[len(w.debugKeyBuf)-10:]
		}
	}
}

func formatDebugKey(b byte) string {
	switch b {
	case 0x1B:
		return "esc"
	case 0x0D:
		return "cr"
	case 0x0A:
		return "lf"
	case 0x09:
		return "tab"
	case 0x7F:
		return "del"
	}
	if b < 0x20 {
		return fmt.Sprintf("0x%02x", b)
	}
	if b >= 0x20 && b <= 0x7E {
		return string([]byte{b})
	}
	return fmt.Sprintf("0x%02x", b)
}

func trimLeftToWidth(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	start := len(s) - width
	return s[start:]
}

// handleEscape processes bytes following an ESC (0x1B). It returns how many
// bytes were consumed and whether the sequence was handled.
// Must be called with w.mu held.
func (w *wrapper) handleEscape(remaining []byte) (consumed int, handled bool) {
	if len(remaining) == 0 {
		return 0, false
	}

	switch remaining[0] {
	case '[': // CSI sequence
		return w.handleCSI(remaining[1:])
	case 'O': // SS3 sequence (e.g. some function keys)
		if len(remaining) >= 2 {
			return 2, true // skip 'O' + final byte
		}
		return 1, true
	}
	return 0, false
}

// handleCSI processes a CSI sequence (after ESC [).
// Must be called with w.mu held.
func (w *wrapper) handleCSI(remaining []byte) (consumed int, handled bool) {
	if len(remaining) == 0 {
		return 1, true // consumed the '['
	}

	// Find the end of the CSI sequence.
	// Parameter bytes: 0x30-0x3F
	// Intermediate bytes: 0x20-0x2F
	// Final byte: 0x40-0x7E
	i := 0
	for i < len(remaining) && remaining[i] >= 0x30 && remaining[i] <= 0x3F {
		i++ // parameter bytes
	}
	for i < len(remaining) && remaining[i] >= 0x20 && remaining[i] <= 0x2F {
		i++ // intermediate bytes
	}
	if i >= len(remaining) {
		return 1 + i, true // incomplete sequence, skip what we have
	}

	final := remaining[i]
	totalConsumed := 1 + i + 1 // '[' + params/intermediates + final byte

	switch final {
	case 'A', 'B': // Up/Down arrows: forward to child PTY
		if w.mode == modePassthrough {
			w.ptm.Write(append([]byte{0x1B, '['}, remaining[:i+1]...))
			break
		}
		if w.mode == modeMenu {
			if final == 'A' {
				w.menuPrev()
			} else {
				w.menuNext()
			}
			w.renderBar()
			break
		}
		if final == 'A' {
			w.historyUp()
		} else {
			w.historyDown()
		}
		w.renderBar()
	case 'C', 'D': // Left/Right arrows
		if w.mode == modePassthrough {
			w.ptm.Write(append([]byte{0x1B, '['}, remaining[:i+1]...))
			break
		}
		if w.mode == modeMenu {
			if final == 'D' {
				w.menuPrev()
			} else {
				w.menuNext()
			}
			w.renderBar()
		}
	}
	// All other CSI sequences (left, right, etc.) are ignored for now.

	return totalConsumed, true
}

// historyUp navigates to the previous command in history.
func (w *wrapper) historyUp() {
	if len(w.history) == 0 {
		return
	}
	if w.histIdx == -1 {
		// Save current input before browsing history.
		w.saved = make([]byte, len(w.input))
		copy(w.saved, w.input)
		w.histIdx = len(w.history) - 1
	} else if w.histIdx > 0 {
		w.histIdx--
	} else {
		return
	}
	w.input = []byte(w.history[w.histIdx])
}

// historyDown navigates to the next command in history.
func (w *wrapper) historyDown() {
	if w.histIdx == -1 {
		return
	}
	if w.histIdx < len(w.history)-1 {
		w.histIdx++
		w.input = []byte(w.history[w.histIdx])
	} else {
		// Restore saved input.
		w.histIdx = -1
		w.input = w.saved
		w.saved = nil
	}
}

// deleteWord removes the last word from the input buffer.
func (w *wrapper) deleteWord() {
	// Trim trailing spaces.
	for len(w.input) > 0 && w.input[len(w.input)-1] == ' ' {
		w.input = w.input[:len(w.input)-1]
	}
	// Remove until next space or start.
	for len(w.input) > 0 && w.input[len(w.input)-1] != ' ' {
		w.input = w.input[:len(w.input)-1]
	}
}

// colorToX11 converts a termenv.Color to X11 rgb: format for OSC responses.
func colorToX11(c termenv.Color) string {
	switch v := c.(type) {
	case termenv.RGBColor:
		hex := string(v)
		if len(hex) == 7 && hex[0] == '#' {
			r, _ := strconv.ParseUint(hex[1:3], 16, 8)
			g, _ := strconv.ParseUint(hex[3:5], 16, 8)
			b, _ := strconv.ParseUint(hex[5:7], 16, 8)
			// Convert 8-bit to 16-bit by repeating the byte.
			return fmt.Sprintf("rgb:%04x/%04x/%04x", r*0x101, g*0x101, b*0x101)
		}
	}
	return ""
}

// renderScreen renders the virtual terminal's screen buffer to stdout using
// absolute cursor positioning. Must be called with w.mu held.
//
// We use our own line rendering instead of midterm's RenderLine because
// RenderLine doesn't emit SGR resets between format regions. This causes
// background colors to bleed from one region into the next when the new
// region doesn't explicitly set a background.
func (w *wrapper) renderScreen() {
	var buf bytes.Buffer
	buf.WriteString("\033[?25l") // hide cursor during render
	for row := 0; row < w.childRows; row++ {
		fmt.Fprintf(&buf, "\033[%d;1H\033[2K", row+1) // position + clear line
		w.renderLine(&buf, row)
	}
	os.Stdout.Write(buf.Bytes())
}

// renderLine writes one row of the virtual terminal to buf with proper SGR
// resets between format regions to prevent attribute bleeding.
func (w *wrapper) renderLine(buf *bytes.Buffer, row int) {
	if row >= len(w.vt.Content) {
		return
	}
	line := w.vt.Content[row]
	var pos int
	var lastFormat midterm.Format
	for region := range w.vt.Format.Regions(row) {
		f := region.F
		if f != lastFormat {
			// Reset before applying the new format so that attributes
			// (especially background colors) from the previous region
			// don't bleed through.
			buf.WriteString("\033[0m")
			buf.WriteString(f.Render())
			lastFormat = f
		}
		end := pos + region.Size

		// Write content for cells that have actual characters.
		if pos < len(line) {
			contentEnd := end
			if contentEnd > len(line) {
				contentEnd = len(line)
			}
			buf.WriteString(string(line[pos:contentEnd]))
		}

		// Pad with spaces for cells beyond the content slice.
		// This preserves background colors set by erase sequences
		// like \033[K which fill the rest of the line with the
		// current background color.
		padStart := len(line)
		if padStart < pos {
			padStart = pos
		}
		if padStart < end {
			buf.WriteString(strings.Repeat(" ", end-padStart))
		}

		pos = end
	}
	buf.WriteString("\033[0m")
}

// renderBar draws the separator line and input bar at the bottom of the
// terminal, leaving the cursor at the input bar. Must be called with w.mu held.
func (w *wrapper) renderBar() {
	var buf bytes.Buffer

	sepRow := w.rows - 1
	inputRow := w.rows
	debugRow := 0
	if w.debugKeys {
		sepRow = w.rows - 2
		inputRow = w.rows - 1
		debugRow = w.rows
	}

	// --- Separator line ---
	fmt.Fprintf(&buf, "\033[%d;1H\033[2K", sepRow)
	buf.WriteString(w.modeBarStyle())
	help := w.helpLabel()
	status := w.statusLabel()
	label := " " + w.modeLabel() + " | " + status
	if help != "" {
		label += " | " + help
	}
	if len(label) > w.cols {
		// Prefer keeping status visible if space is tight.
		label = " " + status
		if len(label) > w.cols {
			label = label[:w.cols]
		}
	}
	buf.WriteString(label)
	if pad := w.cols - len(label); pad > 0 {
		buf.WriteString(strings.Repeat(" ", pad))
	}
	buf.WriteString("\033[0m")

	// --- Input line ---
	prompt := "> "
	inputStr := string(w.input)
	maxInput := w.cols - len(prompt)

	// If input overflows, show the rightmost portion.
	displayInput := inputStr
	runeCount := utf8.RuneCountInString(displayInput)
	if runeCount > maxInput && maxInput > 0 {
		runes := []rune(displayInput)
		displayInput = string(runes[len(runes)-maxInput:])
		runeCount = maxInput
	}
	fmt.Fprintf(&buf, "\033[%d;1H\033[2K", inputRow)
	fmt.Fprintf(&buf, "\033[36m%s\033[0m%s", prompt, displayInput)

	// Position cursor at end of input.
	cursorCol := len(prompt) + runeCount + 1
	if cursorCol > w.cols {
		cursorCol = w.cols
	}
	fmt.Fprintf(&buf, "\033[%d;%dH", inputRow, cursorCol)

	if w.debugKeys {
		fmt.Fprintf(&buf, "\033[%d;1H\033[2K", debugRow)
		debugLabel := w.debugLabel()
		if len(debugLabel) > w.cols {
			debugLabel = trimLeftToWidth(debugLabel, w.cols)
		}
		buf.WriteString(debugLabel)
		if pad := w.cols - len(debugLabel); pad > 0 {
			buf.WriteString(strings.Repeat(" ", pad))
		}
	}

	// Ensure cursor is visible.
	buf.WriteString("\033[?25h")

	os.Stdout.Write(buf.Bytes())
}

// tickStatus triggers periodic status bar renders for idle tracking.
func (w *wrapper) tickStatus(stop <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.mu.Lock()
			w.renderBar()
			w.mu.Unlock()
		case <-stop:
			return
		}
	}
}

func (w *wrapper) startPendingSlash() {
	w.pendingSlash = true
	if w.slashTimer != nil {
		w.slashTimer.Stop()
	}
	w.slashTimer = time.AfterFunc(250*time.Millisecond, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if !w.pendingSlash || w.mode != modeMessage {
			return
		}
		w.pendingSlash = false
		w.mode = modePassthrough
		w.ptm.Write([]byte{'/'})
		w.renderBar()
	})
}

func (w *wrapper) cancelPendingSlash() {
	w.pendingSlash = false
	if w.slashTimer != nil {
		w.slashTimer.Stop()
	}
}

func (w *wrapper) modeLabel() string {
	switch w.mode {
	case modePassthrough:
		return "Passthrough"
	case modeMenu:
		return w.menuLabel()
	default:
		return "Message"
	}
}

func (w *wrapper) modeBarStyle() string {
	switch w.mode {
	case modePassthrough:
		return "\033[7m\033[33m" // yellow-ish
	case modeMenu:
		return "\033[7m\033[34m" // blue
	default:
		return "\033[7m\033[36m" // cyan
	}
}

func (w *wrapper) debugLabel() string {
	prefix := " debug keystrokes: "
	if len(w.debugKeyBuf) == 0 {
		return prefix
	}
	keys := strings.Join(w.debugKeyBuf, " ")
	available := w.cols - len(prefix)
	if available <= 0 {
		if w.cols > 0 {
			return prefix[:w.cols]
		}
		return ""
	}
	if len(keys) > available {
		keys = keys[len(keys)-available:]
	}
	return prefix + keys
}

func (w *wrapper) helpLabel() string {
	switch w.mode {
	case modePassthrough:
		return "Enter/Esc exit"
	case modeMenu:
		return "Left/Right move | Enter select | Esc exit"
	default:
		return "Up/Down history | Enter send | / passthrough | // menu"
	}
}

func (w *wrapper) menuLabel() string {
	var parts []string
	for i, item := range menuItems {
		if i == w.menuIdx {
			parts = append(parts, ">"+item)
		} else {
			parts = append(parts, item)
		}
	}
	return "Menu: " + strings.Join(parts, " | ")
}

func (w *wrapper) menuPrev() {
	if len(menuItems) == 0 {
		return
	}
	if w.menuIdx == 0 {
		w.menuIdx = len(menuItems) - 1
	} else {
		w.menuIdx--
	}
}

func (w *wrapper) menuNext() {
	if len(menuItems) == 0 {
		return
	}
	if w.menuIdx == len(menuItems)-1 {
		w.menuIdx = 0
	} else {
		w.menuIdx++
	}
}

func (w *wrapper) menuSelect() {
	switch w.menuIdx {
	case 0: // Clear input
		w.input = w.input[:0]
	case 1: // Redraw
		os.Stdout.WriteString("\033[2J\033[H")
		w.renderScreen()
	case 2: // Quit
		w.quit = true
		w.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// statusLabel returns the current activity status string.
func (w *wrapper) statusLabel() string {
	const idleThreshold = 2 * time.Second
	if w.lastOut.IsZero() {
		return "Active"
	}
	idleFor := time.Since(w.lastOut)
	if idleFor <= idleThreshold {
		return "Active"
	}
	return "Idle for: " + formatIdleDuration(idleFor)
}

// formatIdleDuration renders a compact human-friendly duration.
func formatIdleDuration(d time.Duration) string {
	if d < time.Minute {
		secs := int(d.Seconds())
		if secs < 1 {
			secs = 1
		}
		return fmt.Sprintf("%ds", secs)
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		return fmt.Sprintf("%dm", mins)
	}
	if d < 24*time.Hour {
		hrs := int(d.Hours())
		return fmt.Sprintf("%dh", hrs)
	}
	days := int(d.Hours() / 24)
	return fmt.Sprintf("%dd", days)
}

// watchResize handles SIGWINCH by updating the child PTY size and redrawing.
func (w *wrapper) watchResize(sigCh <-chan os.Signal) {
	for range sigCh {
		fd := int(os.Stdin.Fd())
		cols, rows, err := term.GetSize(fd)
		minRows := 3
		if w.debugKeys {
			minRows = 4
		}
		if err != nil || rows < minRows {
			continue
		}

		w.mu.Lock()
		w.rows = rows
		w.cols = cols
		w.childRows = rows - w.reservedRows()
		w.vt.Resize(w.childRows, cols)
		pty.Setsize(w.ptm, &pty.Winsize{
			Rows: uint16(w.childRows),
			Cols: uint16(cols),
		})
		os.Stdout.WriteString("\033[2J")
		w.renderScreen()
		w.renderBar()
		w.mu.Unlock()
	}
}
