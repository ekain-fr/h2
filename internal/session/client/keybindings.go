package client

import (
	"os"
	"time"
)

// KeybindingMode indicates which keybinding scheme is active.
type KeybindingMode int

const (
	KeybindingsLegacy KeybindingMode = iota
	KeybindingsKitty
)

// KeybindingHelp holds mode-specific help text.
type KeybindingHelp struct {
	NormalMode      string
	PassthroughMode string
}

var keybindingHelpText = map[KeybindingMode]KeybindingHelp{
	KeybindingsLegacy: {
		NormalMode:      `Enter send | Ctrl+\ menu`,
		PassthroughMode: `Ctrl+\ exit`,
	},
	KeybindingsKitty: {
		NormalMode:      "Enter send | Ctrl+Enter menu",
		PassthroughMode: "Ctrl+Esc exit",
	},
}

func (c *Client) keybindingHelp() KeybindingHelp {
	if h, ok := keybindingHelpText[c.KeybindingMode]; ok {
		return h
	}
	return keybindingHelpText[KeybindingsLegacy]
}

// detectKittyKeyboard probes the terminal for kitty keyboard protocol support.
// Must be called after entering raw mode with stdin available.
func (c *Client) detectKittyKeyboard() {
	// Query current keyboard mode: CSI ? u
	os.Stdout.Write([]byte("\x1b[?u"))

	// Read response with a short timeout. Any CSI response indicates support.
	buf := make([]byte, 64)
	done := make(chan int, 1)
	go func() {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			done <- 0
			return
		}
		done <- n
	}()

	select {
	case n := <-done:
		if n > 0 {
			c.KittyKeyboard = true
			c.KeybindingMode = KeybindingsKitty
			// Enable kitty keyboard protocol (push mode 1 = disambiguate).
			os.Stdout.Write([]byte("\x1b[>1u"))
		}
	case <-time.After(100 * time.Millisecond):
		// No response — legacy terminal.
		c.KeybindingMode = KeybindingsLegacy
	}
}
